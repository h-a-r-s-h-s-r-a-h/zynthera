package libvirt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"zynthera/internal/agent/driver/drivertest"
	"zynthera/internal/agent/netalloc"
	"zynthera/internal/agent/run"
	"zynthera/internal/agent/storage/qcow2"
	"zynthera/internal/shared/vmtypes"
)

// These tests create real KVM virtual machines. They run only when asked:
//
//	ZYN_TEST_LIBVIRT=1 go test ./internal/agent/driver/libvirt
//
// Requirements: the `zynthera` libvirt network is active
// (infra/host-setup/network.xml) and the user may use qemu:///system.
//
// Optional settings:
//
//	ZYN_TEST_LIBVIRT_DIR    data directory; QEMU must be able to reach it,
//	                        so it cannot be under a private home or /tmp
//	                        directory. Default: /var/tmp/zynthera-test
//	ZYN_TEST_LIBVIRT_IMAGE  a bootable golden image (slug/version.qcow2 is
//	                        derived from its path). Without it a blank disk
//	                        is used and the clean-shutdown check is skipped.
func testDriver(t *testing.T) (*Driver, drivertest.Config) {
	t.Helper()
	if os.Getenv("ZYN_TEST_LIBVIRT") != "1" {
		t.Skip("set ZYN_TEST_LIBVIRT=1 on a KVM host to run the libvirt driver tests")
	}
	dir := os.Getenv("ZYN_TEST_LIBVIRT_DIR")
	if dir == "" {
		dir = "/var/tmp/zynthera-test"
	}
	if err := os.MkdirAll(dir, 0o711); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o711); err != nil {
		t.Fatal(err)
	}
	store, err := qcow2.New(dir, run.Exec{})
	if err != nil {
		t.Fatal(err)
	}

	image := vmtypes.ImageRef{Slug: "blank-test", Version: "1"}
	cfg := drivertest.Config{OpTimeout: 2 * time.Minute, ShutdownTimeout: 90 * time.Second}
	if real := os.Getenv("ZYN_TEST_LIBVIRT_IMAGE"); real != "" {
		// .../images/<slug>/<version>.qcow2
		image = vmtypes.ImageRef{
			Slug:    filepath.Base(filepath.Dir(real)),
			Version: strings.TrimSuffix(filepath.Base(real), ".qcow2"),
		}
		target := store.ImagePath(image)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(target); err != nil {
			if out, err := exec.Command("cp", "--reflink=auto", real, target).CombinedOutput(); err != nil {
				t.Fatalf("copy image: %v: %s", err, out)
			}
		}
	} else {
		cfg.SkipGuestShutdown = true
		target := store.ImagePath(image)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(target); err != nil {
			if out, err := exec.Command("qemu-img", "create", "-q", "-f", "qcow2", target, "1G").CombinedOutput(); err != nil {
				t.Fatalf("create blank image: %v: %s", err, out)
			}
			// Readable by the user QEMU runs as. Only done on creation:
			// once a VM has used the image, libvirt may own the file.
			if err := os.Chmod(target, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	// A range of its own, far from the one a real agent on this host uses.
	alloc, err := netalloc.New(dir, "10.210.200.10", "10.210.200.250")
	if err != nil {
		t.Fatal(err)
	}
	drv, err := New(Options{
		URI: "qemu:///system", Network: "zynthera", CPUMode: "host-model",
		BandwidthKB: 12500, BridgePort: 7070, Store: store, Alloc: alloc, Runner: run.Exec{},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Driver, cfg.Image = drv, image
	return drv, cfg
}

func TestConformance(t *testing.T) {
	_, cfg := testDriver(t)
	drivertest.Run(t, cfg)
}

// What a deleted VM must not leave behind on the host (acceptance step 12).
func TestDeleteLeavesNothingBehind(t *testing.T) {
	drv, cfg := testDriver(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const id = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	spec := vmtypes.VMSpec{
		ID: id, ImageSlug: cfg.Image.Slug, ImageVersion: cfg.Image.Version, RAMMB: 1024, VCPUs: 1, DiskGB: 10,
		Hostname: "zynthera-dddddd", Username: "tester", BridgeToken: strings.Repeat("d", 40),
		PasswordHash: "$6$zyntheratestsalt$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789./abcdefghijklmnopqrstuv",
	}
	_ = drv.Delete(ctx, id)
	if err := drv.Create(ctx, spec); err != nil {
		t.Fatal(err)
	}
	lease, found, err := drv.o.Alloc.Get(id)
	if err != nil || !found {
		t.Fatalf("no lease after create: %v", err)
	}
	netXML := func() string {
		out, err := drv.virsh(ctx, "net-dumpxml", "zynthera")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if !strings.Contains(netXML(), lease.MAC) {
		t.Fatal("no static DHCP entry after create")
	}
	// The seed is there; the rendered cloud-init files are not.
	entries, _ := os.ReadDir(drv.o.Store.VMDir(id))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if got := strings.Join(names, ","); got != "disk.qcow2,meta.json,seed.iso" {
		t.Errorf("vm dir contains %s", got)
	}
	if st, _ := os.Stat(drv.metaPath(id)); st == nil || st.Mode().Perm() != 0o600 {
		t.Error("meta.json is not mode 0600")
	}

	if err := drv.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := drv.Suspend(ctx, id); err != nil {
		t.Fatal(err)
	}
	// While suspended no QEMU process exists for this VM.
	if out, _ := exec.Command("pgrep", "-f", "guest=zyn-"+id).Output(); strings.TrimSpace(string(out)) != "" {
		t.Errorf("a QEMU process is still running for a suspended vm: %s", out)
	}

	if err := drv.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(drv.o.Store.VMDir(id)); !os.IsNotExist(err) {
		t.Error("vm directory still exists")
	}
	if state, _ := drv.domstate(ctx, id); state != stateMissing {
		t.Errorf("domain still defined: %q", state)
	}
	if strings.Contains(netXML(), lease.MAC) {
		t.Error("static DHCP entry still present")
	}
	if _, found, _ := drv.o.Alloc.Get(id); found {
		t.Error("address reservation still present")
	}
	if out, _ := drv.virsh(ctx, "list", "--all", "--name", "--with-managed-save"); strings.Contains(out, id) {
		t.Error("saved memory image still registered")
	}
}
