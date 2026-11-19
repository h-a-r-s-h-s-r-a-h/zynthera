// Package qcow2 stores VM disks as qcow2 files. A VM's disk is a thin clone
// of a golden image: it only holds what the VM changed, and reads everything
// else from the image. That is why a golden image must never be modified or
// deleted while any VM was cloned from it.
package qcow2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"zynthera/internal/agent/run"
	"zynthera/internal/agent/storage"
	"zynthera/internal/shared/vmtypes"
)

// Storage implements storage.Storage with qemu-img.
type Storage struct {
	dataDir string
	run     run.Runner
}

var _ storage.Storage = (*Storage)(nil)

// New returns a qcow2 store rooted at dataDir (spec section 5.2):
//
//	<dataDir>/images/<slug>/<version>.qcow2   golden images
//	<dataDir>/vms/<vm id>/disk.qcow2          VM disks
func New(dataDir string, runner run.Runner) (*Storage, error) {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("qcow2: data dir: %w", err)
	}
	for _, sub := range []string{"images", "vms"} {
		// 0711: QEMU runs as another user and must be able to reach the
		// disk files, but nobody may list the directory.
		if err := os.MkdirAll(filepath.Join(abs, sub), 0o711); err != nil { //nolint:gosec // see comment
			return nil, fmt.Errorf("qcow2: create %s dir: %w", sub, err)
		}
	}
	return &Storage{dataDir: abs, run: runner}, nil
}

// VMDir is the directory that holds everything belonging to one VM.
func (s *Storage) VMDir(vmID string) string { return filepath.Join(s.dataDir, "vms", vmID) }

// ImagePath is where a golden image lives.
func (s *Storage) ImagePath(image vmtypes.ImageRef) string {
	return filepath.Join(s.dataDir, "images", image.Slug, image.Version+".qcow2")
}

func (s *Storage) diskPath(vmID string) string { return filepath.Join(s.VMDir(vmID), "disk.qcow2") }

// checkID refuses anything that is not a UUID before it becomes part of a
// path. Callers validate too; this is the last line of defence.
func checkID(kind, id string) error {
	if !vmtypes.IsUUID(id) {
		return fmt.Errorf("qcow2: %s id %q is not a UUID: %w", kind, id, storage.ErrInvalid)
	}
	return nil
}

func (s *Storage) existingDisk(vmID string) (string, error) {
	if err := checkID("vm", vmID); err != nil {
		return "", err
	}
	path := s.diskPath(vmID)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("disk of vm %s: %w", vmID, storage.ErrNotFound)
	} else if err != nil {
		return "", fmt.Errorf("qcow2: stat disk: %w", err)
	}
	return path, nil
}

func ref(path string) storage.DiskRef {
	return storage.DiskRef{Kind: "file", Path: path, Format: "qcow2"}
}

// CloneImage creates the VM's disk as a clone of a golden image. An existing
// disk left by an earlier, failed attempt is replaced.
func (s *Storage) CloneImage(ctx context.Context, image vmtypes.ImageRef, vmID string, sizeGB int) (storage.DiskRef, error) {
	if err := checkID("vm", vmID); err != nil {
		return storage.DiskRef{}, err
	}
	if !vmtypes.IsImageSlug(image.Slug) || !vmtypes.IsImageVersion(image.Version) || sizeGB < 1 {
		return storage.DiskRef{}, fmt.Errorf("qcow2: bad image or size: %w", storage.ErrInvalid)
	}
	golden := s.ImagePath(image)
	if _, err := os.Stat(golden); errors.Is(err, fs.ErrNotExist) {
		return storage.DiskRef{}, fmt.Errorf("image %s version %s: %w", image.Slug, image.Version, storage.ErrNotFound)
	} else if err != nil {
		return storage.DiskRef{}, fmt.Errorf("qcow2: stat image: %w", err)
	}
	if err := os.MkdirAll(s.VMDir(vmID), 0o711); err != nil { //nolint:gosec // QEMU must reach the disk; see New
		return storage.DiskRef{}, fmt.Errorf("qcow2: create vm dir: %w", err)
	}
	disk := s.diskPath(vmID)
	if err := os.Remove(disk); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return storage.DiskRef{}, fmt.Errorf("qcow2: remove old disk: %w", err)
	}
	_, err := s.run.Output(ctx, "qemu-img", "create", "-q", "-f", "qcow2", "-F", "qcow2",
		"-b", golden, disk, strconv.Itoa(sizeGB)+"G")
	if err != nil {
		return storage.DiskRef{}, fmt.Errorf("qcow2: clone image: %w", err)
	}
	return ref(disk), nil
}

// Disk returns the VM's disk.
func (s *Storage) Disk(_ context.Context, vmID string) (storage.DiskRef, error) {
	path, err := s.existingDisk(vmID)
	if err != nil {
		return storage.DiskRef{}, err
	}
	return ref(path), nil
}

// Delete removes the VM's disk. A disk that is already gone is fine.
func (s *Storage) Delete(_ context.Context, vmID string) error {
	if err := checkID("vm", vmID); err != nil {
		return err
	}
	if err := os.Remove(s.diskPath(vmID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("qcow2: delete disk: %w", err)
	}
	return nil
}

// info is the part of `qemu-img info --output=json` that is used here.
type info struct {
	VirtualSize int64 `json:"virtual-size"`
	ActualSize  int64 `json:"actual-size"`
	Snapshots   []struct {
		Name string `json:"name"`
	} `json:"snapshots"`
}

func (s *Storage) info(ctx context.Context, path string) (info, error) {
	// -U reads without taking the image lock, so this also works while
	// QEMU has the disk open (needed for the usage figure in heartbeats).
	out, err := s.run.Output(ctx, "qemu-img", "info", "-U", "--output=json", path)
	if err != nil {
		return info{}, fmt.Errorf("qcow2: inspect disk: %w", err)
	}
	var i info
	if err := json.Unmarshal([]byte(out), &i); err != nil {
		return info{}, fmt.Errorf("qcow2: parse qemu-img output: %w", err)
	}
	return i, nil
}

const gib = int64(1) << 30

// Grow enlarges the disk to sizeGB. The same size is a no-op; a smaller
// size is refused, because shrinking would cut off data.
func (s *Storage) Grow(ctx context.Context, vmID string, sizeGB int) error {
	path, err := s.existingDisk(vmID)
	if err != nil {
		return err
	}
	i, err := s.info(ctx, path)
	if err != nil {
		return err
	}
	want := int64(sizeGB) * gib
	switch {
	case want == i.VirtualSize:
		return nil
	case want < i.VirtualSize:
		return fmt.Errorf("a disk can only grow (it is %d GB): %w", i.VirtualSize/gib, storage.ErrInvalid)
	}
	if _, err := s.run.Output(ctx, "qemu-img", "resize", "-q", "-f", "qcow2", path, strconv.Itoa(sizeGB)+"G"); err != nil {
		return fmt.Errorf("qcow2: grow disk: %w", err)
	}
	return nil
}

func (s *Storage) hasSnapshot(ctx context.Context, path, snapshotID string) (bool, error) {
	i, err := s.info(ctx, path)
	if err != nil {
		return false, err
	}
	for _, snap := range i.Snapshots {
		if snap.Name == snapshotID {
			return true, nil
		}
	}
	return false, nil
}

// snapshotArgs checks the ids and finds the disk for a snapshot operation.
func (s *Storage) snapshotArgs(vmID, snapshotID string) (string, error) {
	if err := checkID("snapshot", snapshotID); err != nil {
		return "", err
	}
	return s.existingDisk(vmID)
}

// SnapshotCreate takes an internal qcow2 snapshot. The disk must not be in
// use by a running VM. Creating a snapshot that exists is a no-op.
func (s *Storage) SnapshotCreate(ctx context.Context, vmID, snapshotID string) error {
	path, err := s.snapshotArgs(vmID, snapshotID)
	if err != nil {
		return err
	}
	if exists, err := s.hasSnapshot(ctx, path, snapshotID); err != nil || exists {
		return err
	}
	if _, err := s.run.Output(ctx, "qemu-img", "snapshot", "-q", "-c", snapshotID, path); err != nil {
		return fmt.Errorf("qcow2: create snapshot: %w", err)
	}
	return nil
}

// SnapshotRestore rolls the disk back to a snapshot. The snapshot stays.
func (s *Storage) SnapshotRestore(ctx context.Context, vmID, snapshotID string) error {
	path, err := s.snapshotArgs(vmID, snapshotID)
	if err != nil {
		return err
	}
	exists, err := s.hasSnapshot(ctx, path, snapshotID)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("snapshot %s: %w", snapshotID, storage.ErrNotFound)
	}
	if _, err := s.run.Output(ctx, "qemu-img", "snapshot", "-q", "-a", snapshotID, path); err != nil {
		return fmt.Errorf("qcow2: restore snapshot: %w", err)
	}
	return nil
}

// SnapshotDelete removes a snapshot. A snapshot that is already gone is fine.
func (s *Storage) SnapshotDelete(ctx context.Context, vmID, snapshotID string) error {
	path, err := s.snapshotArgs(vmID, snapshotID)
	if err != nil {
		return err
	}
	if exists, err := s.hasSnapshot(ctx, path, snapshotID); err != nil || !exists {
		return err
	}
	if _, err := s.run.Output(ctx, "qemu-img", "snapshot", "-q", "-d", snapshotID, path); err != nil {
		return fmt.Errorf("qcow2: delete snapshot: %w", err)
	}
	return nil
}

// SnapshotList returns the disk's snapshots. qcow2 does not record how much
// space one internal snapshot takes, so the size is reported as zero.
func (s *Storage) SnapshotList(ctx context.Context, vmID string) ([]vmtypes.SnapshotInfo, error) {
	path, err := s.existingDisk(vmID)
	if err != nil {
		return nil, err
	}
	i, err := s.info(ctx, path)
	if err != nil {
		return nil, err
	}
	out := make([]vmtypes.SnapshotInfo, 0, len(i.Snapshots))
	for _, snap := range i.Snapshots {
		out = append(out, vmtypes.SnapshotInfo{ID: snap.Name})
	}
	return out, nil
}

// UsedBytes is the space the disk file really takes on the host.
func (s *Storage) UsedBytes(ctx context.Context, vmID string) (int64, error) {
	path, err := s.existingDisk(vmID)
	if err != nil {
		return 0, err
	}
	i, err := s.info(ctx, path)
	if err != nil {
		return 0, err
	}
	return i.ActualSize, nil
}

// HostDisk reports the size and free space of the filesystem that holds
// the data directory.
func (s *Storage) HostDisk(_ context.Context) (totalGB, freeGB int, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(s.dataDir, &st); err != nil {
		return 0, 0, fmt.Errorf("qcow2: statfs: %w", err)
	}
	block := int64(st.Bsize)
	total := int64(st.Blocks) * block //nolint:gosec // block counts of a real filesystem fit in int64
	free := int64(st.Bavail) * block  //nolint:gosec // as above
	return int(total / gib), int(free / gib), nil
}

// Images lists the golden images present: images/<slug>/<version>.qcow2.
func (s *Storage) Images(_ context.Context) ([]vmtypes.ImageRef, error) {
	root := filepath.Join(s.dataDir, "images")
	slugs, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("qcow2: list images: %w", err)
	}
	out := []vmtypes.ImageRef{}
	for _, slug := range slugs {
		if !slug.IsDir() || !vmtypes.IsImageSlug(slug.Name()) {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, slug.Name()))
		if err != nil {
			return nil, fmt.Errorf("qcow2: list image versions: %w", err)
		}
		for _, f := range files {
			version, ok := strings.CutSuffix(f.Name(), ".qcow2")
			if f.IsDir() || !ok || !vmtypes.IsImageVersion(version) {
				continue
			}
			out = append(out, vmtypes.ImageRef{Slug: slug.Name(), Version: version})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Slug != out[j].Slug {
			return out[i].Slug < out[j].Slug
		}
		return out[i].Version < out[j].Version
	})
	return out, nil
}
