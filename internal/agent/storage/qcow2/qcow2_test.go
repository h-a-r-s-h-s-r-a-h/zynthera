package qcow2

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"zynthera/internal/agent/run"
	"zynthera/internal/agent/storage"
	"zynthera/internal/shared/vmtypes"
)

const (
	vmID  = "11111111-1111-4111-8111-111111111111"
	snapA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	snapB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

var image = vmtypes.ImageRef{Slug: "ubuntu-24.04-xfce", Version: "2026.11.1"}

// newStore builds a store with a small, empty golden image, using the real
// qemu-img. The tests are skipped where qemu-img is not installed.
func newStore(t *testing.T) *Storage {
	t.Helper()
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img is not installed")
	}
	s, err := New(t.TempDir(), run.Exec{})
	if err != nil {
		t.Fatal(err)
	}
	golden := s.ImagePath(image)
	if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("qemu-img", "create", "-q", "-f", "qcow2", golden, "1G").CombinedOutput(); err != nil {
		t.Fatalf("create golden image: %v: %s", err, out)
	}
	return s
}

func TestCloneGrowAndInspect(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	disk, err := s.CloneImage(ctx, image, vmID, 40)
	if err != nil {
		t.Fatal(err)
	}
	if disk.Kind != "file" || disk.Format != "qcow2" || disk.Path != filepath.Join(s.VMDir(vmID), "disk.qcow2") {
		t.Fatalf("disk ref = %+v", disk)
	}
	got, err := s.Disk(ctx, vmID)
	if err != nil || got != disk {
		t.Fatalf("Disk = %+v, %v", got, err)
	}

	// The clone is thin: 40 GB virtual, a few hundred KB real.
	i, err := s.info(ctx, disk.Path)
	if err != nil {
		t.Fatal(err)
	}
	if i.VirtualSize != 40*gib {
		t.Errorf("virtual size = %d", i.VirtualSize)
	}
	used, err := s.UsedBytes(ctx, vmID)
	if err != nil || used <= 0 || used > 64<<20 {
		t.Errorf("used bytes = %d, %v", used, err)
	}

	if err := s.Grow(ctx, vmID, 40); err != nil {
		t.Errorf("grow to the same size: %v", err)
	}
	if err := s.Grow(ctx, vmID, 80); err != nil {
		t.Fatalf("grow: %v", err)
	}
	if i, _ := s.info(ctx, disk.Path); i.VirtualSize != 80*gib {
		t.Errorf("virtual size after grow = %d", i.VirtualSize)
	}
	if err := s.Grow(ctx, vmID, 40); !errors.Is(err, storage.ErrInvalid) {
		t.Errorf("shrink: %v, want ErrInvalid", err)
	}

	// Cloning again replaces a disk left by a failed attempt.
	if _, err := s.CloneImage(ctx, image, vmID, 40); err != nil {
		t.Fatalf("second clone: %v", err)
	}
	if i, _ := s.info(ctx, disk.Path); i.VirtualSize != 40*gib {
		t.Errorf("second clone did not replace the disk: %d", i.VirtualSize)
	}
}

func TestSnapshots(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.CloneImage(ctx, image, vmID, 40); err != nil {
		t.Fatal(err)
	}
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	must("create A", s.SnapshotCreate(ctx, vmID, snapA))
	must("create A again", s.SnapshotCreate(ctx, vmID, snapA))
	must("create B", s.SnapshotCreate(ctx, vmID, snapB))
	list, err := s.SnapshotList(ctx, vmID)
	must("list", err)
	if len(list) != 2 || list[0].ID != snapA || list[1].ID != snapB {
		t.Fatalf("snapshots = %+v", list)
	}

	// Restoring an older snapshot keeps the newer one (unlike ZFS).
	must("restore A", s.SnapshotRestore(ctx, vmID, snapA))
	if list, _ := s.SnapshotList(ctx, vmID); len(list) != 2 {
		t.Fatalf("restore removed snapshots: %+v", list)
	}
	unknown := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	if err := s.SnapshotRestore(ctx, vmID, unknown); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("restore unknown: %v, want ErrNotFound", err)
	}

	must("delete A", s.SnapshotDelete(ctx, vmID, snapA))
	must("delete A again", s.SnapshotDelete(ctx, vmID, snapA))
	if list, _ := s.SnapshotList(ctx, vmID); len(list) != 1 || list[0].ID != snapB {
		t.Fatalf("after delete: %+v", list)
	}

	// Spec 12.5 asks to check this: can a disk with snapshots be grown?
	growErr := s.Grow(ctx, vmID, 80)
	t.Logf("growing a disk that has internal snapshots: %v", growErr)
}

func TestMissingThings(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if _, err := s.Disk(ctx, vmID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Disk of an unknown vm: %v", err)
	}
	if err := s.Grow(ctx, vmID, 80); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Grow of an unknown vm: %v", err)
	}
	if err := s.SnapshotCreate(ctx, vmID, snapA); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("SnapshotCreate on an unknown vm: %v", err)
	}
	if err := s.Delete(ctx, vmID); err != nil {
		t.Errorf("Delete of an unknown vm: %v", err)
	}
	missing := vmtypes.ImageRef{Slug: "debian-12-xfce", Version: "2026.11.1"}
	if _, err := s.CloneImage(ctx, missing, vmID, 40); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("clone of a missing image: %v", err)
	}
}

func TestIDsNeverBecomePaths(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for _, bad := range []string{"../../etc", "", "a/b", "11111111-1111-4111-8111-11111111111", "..", "x.qcow2"} {
		if _, err := s.CloneImage(ctx, image, bad, 40); !errors.Is(err, storage.ErrInvalid) {
			t.Errorf("CloneImage with vm id %q: %v", bad, err)
		}
		if err := s.Delete(ctx, bad); !errors.Is(err, storage.ErrInvalid) {
			t.Errorf("Delete with vm id %q: %v", bad, err)
		}
	}
	if _, err := s.CloneImage(ctx, image, vmID, 40); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../x", "-d", "snap one", ""} {
		if err := s.SnapshotCreate(ctx, vmID, bad); !errors.Is(err, storage.ErrInvalid) {
			t.Errorf("SnapshotCreate with id %q: %v", bad, err)
		}
	}
	evil := vmtypes.ImageRef{Slug: "../../etc", Version: "passwd"}
	if _, err := s.CloneImage(ctx, evil, vmID, 40); !errors.Is(err, storage.ErrInvalid) {
		t.Errorf("clone with a path as image slug: %v", err)
	}
}

func TestImagesAndHostDisk(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	// Things that are not images are ignored.
	other := filepath.Join(filepath.Dir(s.ImagePath(image)), "notes.txt")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	images, err := s.Images(ctx)
	if err != nil || len(images) != 1 || images[0] != image {
		t.Fatalf("images = %+v, %v", images, err)
	}
	total, free, err := s.HostDisk(ctx)
	if err != nil || total <= 0 || free < 0 || free > total {
		t.Fatalf("host disk = %d total, %d free, %v", total, free, err)
	}
}
