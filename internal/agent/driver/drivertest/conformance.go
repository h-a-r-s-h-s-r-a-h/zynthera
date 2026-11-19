// Package drivertest is the conformance suite every Driver must pass. It runs
// against the mock driver in normal test runs and against the libvirt driver
// when ZYN_TEST_LIBVIRT=1 is set on a KVM host.
package drivertest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"zynthera/internal/agent/driver"
	"zynthera/internal/shared/vmtypes"
)

// Config tells the suite how to talk to the driver under test.
type Config struct {
	Driver driver.Driver
	// Image is a golden image that exists on the host.
	Image vmtypes.ImageRef
	// OpTimeout bounds each single operation. Zero means one minute.
	OpTimeout time.Duration
	// ShutdownTimeout is how long a clean guest shutdown may take.
	ShutdownTimeout time.Duration
	// SkipGuestShutdown skips the clean-shutdown check, for images without
	// a guest agent or ACPI support.
	SkipGuestShutdown bool
}

// A fixed, valid SHA-512 crypt hash (of a throwaway password) for test VMs.
const testPasswordHash = "$6$zyntheratestsalt$" +
	"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789./abcdefghijklmnopqrstuv"

func newUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

type suite struct {
	t   *testing.T
	cfg Config
}

func (s *suite) ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.OpTimeout)
	s.t.Cleanup(cancel)
	return ctx
}

func (s *suite) newSpec() vmtypes.VMSpec {
	id := newUUID(s.t)
	return vmtypes.VMSpec{
		ID:           id,
		ImageSlug:    s.cfg.Image.Slug,
		ImageVersion: s.cfg.Image.Version,
		RAMMB:        2048,
		VCPUs:        1,
		DiskGB:       40,
		Hostname:     "zynthera-" + id[:6],
		Username:     "tester",
		PasswordHash: testPasswordHash,
		BridgeToken:  hex.EncodeToString([]byte(id))[:48],
	}
}

// create makes a VM and registers its removal.
func (s *suite) create() vmtypes.VMSpec {
	s.t.Helper()
	spec := s.newSpec()
	if msg := spec.Validate(); msg != "" {
		s.t.Fatalf("test spec is invalid: %s", msg)
	}
	s.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), s.cfg.OpTimeout)
		defer cancel()
		if err := s.cfg.Driver.Delete(ctx, spec.ID); err != nil {
			s.t.Errorf("cleanup: delete %s: %v", spec.ID, err)
		}
	})
	if err := s.cfg.Driver.Create(s.ctx(), spec); err != nil {
		s.t.Fatalf("Create: %v", err)
	}
	return spec
}

func (s *suite) must(what string, err error) {
	s.t.Helper()
	if err != nil {
		s.t.Fatalf("%s: %v", what, err)
	}
}

func (s *suite) wantStatus(id, status string, savedState bool) {
	s.t.Helper()
	got, err := s.cfg.Driver.Status(s.ctx(), id)
	if err != nil {
		s.t.Fatalf("Status: %v", err)
	}
	if got.ID != id {
		s.t.Errorf("Status.ID = %q, want %q", got.ID, id)
	}
	if got.Status != status || got.HasSavedState != savedState {
		s.t.Fatalf("status = %s (saved state %v), want %s (saved state %v)",
			got.Status, got.HasSavedState, status, savedState)
	}
}

func (s *suite) wantErr(what string, err, target error) {
	s.t.Helper()
	if !errors.Is(err, target) {
		s.t.Fatalf("%s: error = %v, want %v", what, err, target)
	}
}

// Run executes the whole suite.
func Run(t *testing.T, cfg Config) {
	if cfg.OpTimeout == 0 {
		cfg.OpTimeout = time.Minute
	}
	if cfg.ShutdownTimeout == 0 {
		cfg.ShutdownTimeout = 30 * time.Second
	}
	run := func(name string, fn func(s *suite)) {
		t.Run(name, func(t *testing.T) { fn(&suite{t: t, cfg: cfg}) })
	}
	d := cfg.Driver

	run("create leaves the vm stopped and is repeatable", func(s *suite) {
		spec := s.create()
		s.wantStatus(spec.ID, vmtypes.StatusStopped, false)
		s.must("second Create", d.Create(s.ctx(), spec))
		s.wantStatus(spec.ID, vmtypes.StatusStopped, false)
	})

	run("start and suspend and resume", func(s *suite) {
		spec := s.create()
		s.must("Start", d.Start(s.ctx(), spec.ID))
		s.wantStatus(spec.ID, vmtypes.StatusRunning, false)
		s.must("Start again", d.Start(s.ctx(), spec.ID))
		s.wantStatus(spec.ID, vmtypes.StatusRunning, false)

		s.must("Suspend", d.Suspend(s.ctx(), spec.ID))
		s.wantStatus(spec.ID, vmtypes.StatusSuspended, true)
		s.must("Suspend again", d.Suspend(s.ctx(), spec.ID))
		s.wantStatus(spec.ID, vmtypes.StatusSuspended, true)

		s.must("resume", d.Start(s.ctx(), spec.ID))
		s.wantStatus(spec.ID, vmtypes.StatusRunning, false)
	})

	run("suspending a stopped vm changes nothing", func(s *suite) {
		spec := s.create()
		s.must("Suspend", d.Suspend(s.ctx(), spec.ID))
		s.wantStatus(spec.ID, vmtypes.StatusStopped, false)
	})

	run("force stop powers off and discards saved state", func(s *suite) {
		spec := s.create()
		s.must("Start", d.Start(s.ctx(), spec.ID))
		s.must("ForceStop running", d.ForceStop(s.ctx(), spec.ID))
		s.wantStatus(spec.ID, vmtypes.StatusStopped, false)
		s.must("ForceStop again", d.ForceStop(s.ctx(), spec.ID))

		s.must("Start", d.Start(s.ctx(), spec.ID))
		s.must("Suspend", d.Suspend(s.ctx(), spec.ID))
		s.must("ForceStop suspended", d.ForceStop(s.ctx(), spec.ID))
		s.wantStatus(spec.ID, vmtypes.StatusStopped, false)
	})

	run("discard saved state", func(s *suite) {
		spec := s.create()
		s.must("Start", d.Start(s.ctx(), spec.ID))
		s.must("Suspend", d.Suspend(s.ctx(), spec.ID))
		s.must("DiscardSavedState", d.DiscardSavedState(s.ctx(), spec.ID))
		s.wantStatus(spec.ID, vmtypes.StatusStopped, false)
		s.must("DiscardSavedState again", d.DiscardSavedState(s.ctx(), spec.ID))
	})

	run("clean shutdown", func(s *suite) {
		if cfg.SkipGuestShutdown {
			s.t.Skip("guest shutdown not supported by this image")
		}
		spec := s.create()
		s.must("Shutdown of a stopped vm", d.Shutdown(s.ctx(), spec.ID, cfg.ShutdownTimeout))
		s.must("Start", d.Start(s.ctx(), spec.ID))
		s.must("Shutdown", d.Shutdown(s.ctx(), spec.ID, cfg.ShutdownTimeout))
		s.wantStatus(spec.ID, vmtypes.StatusStopped, false)
	})

	run("reboot keeps the vm running and needs a running vm", func(s *suite) {
		spec := s.create()
		s.wantErr("Reboot of a stopped vm", d.Reboot(s.ctx(), spec.ID), driver.ErrInvalidState)
		s.must("Start", d.Start(s.ctx(), spec.ID))
		s.must("Reboot", d.Reboot(s.ctx(), spec.ID))
		s.wantStatus(spec.ID, vmtypes.StatusRunning, false)
	})

	run("display only while running", func(s *suite) {
		spec := s.create()
		_, err := d.Display(s.ctx(), spec.ID)
		s.wantErr("Display of a stopped vm", err, driver.ErrInvalidState)

		s.must("Start", d.Start(s.ctx(), spec.ID))
		ep, err := d.Display(s.ctx(), spec.ID)
		s.must("Display", err)
		if (ep.Network != "tcp" && ep.Network != "unix") || ep.Address == "" {
			s.t.Errorf("display endpoint = %+v", ep)
		}

		s.must("Suspend", d.Suspend(s.ctx(), spec.ID))
		_, err = d.Display(s.ctx(), spec.ID)
		s.wantErr("Display of a suspended vm", err, driver.ErrInvalidState)
	})

	run("resize only when off with no saved state, disk only grows", func(s *suite) {
		spec := s.create()
		s.must("Resize", d.Resize(s.ctx(), spec.ID, 4096, 2, 80))
		s.must("Resize to the same size", d.Resize(s.ctx(), spec.ID, 4096, 2, 80))
		s.wantErr("shrinking the disk", d.Resize(s.ctx(), spec.ID, 4096, 2, 40), driver.ErrInvalidState)

		s.must("Start", d.Start(s.ctx(), spec.ID))
		s.wantErr("Resize while running", d.Resize(s.ctx(), spec.ID, 2048, 1, 80), driver.ErrInvalidState)
		s.must("Suspend", d.Suspend(s.ctx(), spec.ID))
		s.wantErr("Resize with saved state", d.Resize(s.ctx(), spec.ID, 2048, 1, 80), driver.ErrInvalidState)
	})

	run("snapshots", func(s *suite) {
		spec := s.create()
		snapA, snapB := newUUID(s.t), newUUID(s.t)

		s.must("SnapshotCreate A", d.SnapshotCreate(s.ctx(), spec.ID, snapA))
		s.must("SnapshotCreate A again", d.SnapshotCreate(s.ctx(), spec.ID, snapA))
		s.must("SnapshotCreate B", d.SnapshotCreate(s.ctx(), spec.ID, snapB))
		list, err := d.SnapshotList(s.ctx(), spec.ID)
		s.must("SnapshotList", err)
		if len(list) != 2 {
			s.t.Fatalf("snapshots = %+v, want 2", list)
		}

		s.must("Start", d.Start(s.ctx(), spec.ID))
		s.wantErr("SnapshotCreate while running", d.SnapshotCreate(s.ctx(), spec.ID, newUUID(s.t)), driver.ErrInvalidState)
		s.wantErr("SnapshotRestore while running", d.SnapshotRestore(s.ctx(), spec.ID, snapA), driver.ErrInvalidState)

		// A snapshot can be taken while suspended, and restoring one throws
		// the saved memory away.
		s.must("Suspend", d.Suspend(s.ctx(), spec.ID))
		snapC := newUUID(s.t)
		s.must("SnapshotCreate C while suspended", d.SnapshotCreate(s.ctx(), spec.ID, snapC))
		s.wantStatus(spec.ID, vmtypes.StatusSuspended, true)
		s.must("SnapshotRestore C", d.SnapshotRestore(s.ctx(), spec.ID, snapC))
		s.wantStatus(spec.ID, vmtypes.StatusStopped, false)

		s.wantErr("restoring an unknown snapshot", d.SnapshotRestore(s.ctx(), spec.ID, newUUID(s.t)), driver.ErrNotFound)

		s.must("SnapshotDelete C", d.SnapshotDelete(s.ctx(), spec.ID, snapC))
		s.must("SnapshotDelete C again", d.SnapshotDelete(s.ctx(), spec.ID, snapC))
		list, err = d.SnapshotList(s.ctx(), spec.ID)
		s.must("SnapshotList", err)
		if len(list) != 2 {
			s.t.Fatalf("snapshots after delete = %+v, want 2", list)
		}

		// The VM still boots after a restore.
		s.must("Start after restore", d.Start(s.ctx(), spec.ID))
		s.wantStatus(spec.ID, vmtypes.StatusRunning, false)
	})

	run("list contains created vms", func(s *suite) {
		a, b := s.create(), s.create()
		s.must("Start", d.Start(s.ctx(), a.ID))
		list, err := d.List(s.ctx())
		s.must("List", err)
		found := map[string]string{}
		for _, v := range list {
			found[v.ID] = v.Status
		}
		if found[a.ID] != vmtypes.StatusRunning || found[b.ID] != vmtypes.StatusStopped {
			s.t.Fatalf("list = %v", found)
		}
	})

	run("delete removes a vm in any state and is repeatable", func(s *suite) {
		for _, prepare := range []string{"stopped", "running", "suspended"} {
			spec := s.create()
			if prepare != "stopped" {
				s.must("Start", d.Start(s.ctx(), spec.ID))
			}
			if prepare == "suspended" {
				s.must("Suspend", d.Suspend(s.ctx(), spec.ID))
			}
			s.must("Delete "+prepare, d.Delete(s.ctx(), spec.ID))
			s.wantStatus(spec.ID, vmtypes.StatusMissing, false)
			s.must("Delete again", d.Delete(s.ctx(), spec.ID))
		}
	})

	run("operations on an unknown vm", func(s *suite) {
		id := newUUID(s.t)
		s.wantStatus(id, vmtypes.StatusMissing, false)
		s.must("Delete", d.Delete(s.ctx(), id))
		s.wantErr("Start", d.Start(s.ctx(), id), driver.ErrNotFound)
		s.wantErr("Suspend", d.Suspend(s.ctx(), id), driver.ErrNotFound)
		s.wantErr("ForceStop", d.ForceStop(s.ctx(), id), driver.ErrNotFound)
		s.wantErr("Reboot", d.Reboot(s.ctx(), id), driver.ErrNotFound)
		s.wantErr("DiscardSavedState", d.DiscardSavedState(s.ctx(), id), driver.ErrNotFound)
		s.wantErr("Resize", d.Resize(s.ctx(), id, 2048, 1, 40), driver.ErrNotFound)
		s.wantErr("SnapshotCreate", d.SnapshotCreate(s.ctx(), id, newUUID(s.t)), driver.ErrNotFound)
		_, err := d.Display(s.ctx(), id)
		s.wantErr("Display", err, driver.ErrNotFound)
		_, err = d.SnapshotList(s.ctx(), id)
		s.wantErr("SnapshotList", err, driver.ErrNotFound)
	})

	run("capacity and images", func(s *suite) {
		c, err := d.Capacity(s.ctx())
		s.must("Capacity", err)
		if c.TotalRAMMB <= 0 || c.TotalVCPUs <= 0 || c.TotalDiskGB <= 0 || c.FreeDiskGB < 0 || c.FreeDiskGB > c.TotalDiskGB {
			s.t.Errorf("capacity = %+v", c)
		}
		images, err := d.Images(s.ctx())
		s.must("Images", err)
		found := false
		for _, img := range images {
			if img == cfg.Image {
				found = true
			}
		}
		if !found {
			s.t.Errorf("images %v do not include %v", images, cfg.Image)
		}
	})
}
