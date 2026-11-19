package mock

import (
	"context"
	"strings"
	"testing"
	"time"

	"zynthera/internal/agent/driver/drivertest"
	"zynthera/internal/shared/vmtypes"
)

var testImage = vmtypes.ImageRef{Slug: "ubuntu-24.04-xfce", Version: "2026.11.1"}

const testHash = "$6$zyntheratestsalt$" +
	"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789./abcdefghijklmnopqrstuv"

func newDriver(t *testing.T, dir string, fail ...string) *Driver {
	t.Helper()
	d, err := New(Options{DataDir: dir, VNCAddr: "127.0.0.1:5901", Images: []vmtypes.ImageRef{testImage}, Fail: fail})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func spec(id string) vmtypes.VMSpec {
	return vmtypes.VMSpec{
		ID: id, ImageSlug: testImage.Slug, ImageVersion: testImage.Version,
		RAMMB: 2048, VCPUs: 1, DiskGB: 40, Hostname: "zynthera-abc123", Username: "tester",
		PasswordHash: testHash, BridgeToken: strings.Repeat("a", 40),
	}
}

func TestConformance(t *testing.T) {
	drivertest.Run(t, drivertest.Config{Driver: newDriver(t, t.TempDir()), Image: testImage})
}

func TestStateSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	const id = "11111111-1111-4111-8111-111111111111"

	d := newDriver(t, dir)
	if err := d.Create(ctx, spec(id)); err != nil {
		t.Fatal(err)
	}
	if err := d.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := d.Suspend(ctx, id); err != nil {
		t.Fatal(err)
	}

	// A new driver on the same directory is what an agent restart looks like.
	st, err := newDriver(t, dir).Status(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != vmtypes.StatusSuspended || !st.HasSavedState {
		t.Fatalf("after restart: %+v", st)
	}
}

func TestFailureInjection(t *testing.T) {
	ctx := context.Background()
	const id = "22222222-2222-4222-8222-222222222222"
	d := newDriver(t, t.TempDir(), "suspend")

	if err := d.Create(ctx, spec(id)); err != nil {
		t.Fatal(err)
	}
	if err := d.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := d.Suspend(ctx, id); err == nil {
		t.Fatal("suspend was told to fail but succeeded")
	}
	// A failed operation leaves the VM as it was.
	st, _ := d.Status(ctx, id)
	if st.Status != vmtypes.StatusRunning {
		t.Fatalf("status after failed suspend = %s", st.Status)
	}
}

func TestDelaysRespectCancellation(t *testing.T) {
	d, err := New(Options{DataDir: t.TempDir(), Delays: Delays{Create: time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := d.Create(ctx, spec("33333333-3333-4333-8333-333333333333")); err == nil {
		t.Fatal("create ignored the cancelled context")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("create did not stop when the context ended")
	}
}

func TestSpecValidation(t *testing.T) {
	good := spec("44444444-4444-4444-8444-444444444444")
	if msg := good.Validate(); msg != "" {
		t.Fatalf("valid spec rejected: %s", msg)
	}
	bad := map[string]func(*vmtypes.VMSpec){
		"path in id":         func(s *vmtypes.VMSpec) { s.ID = "../../etc/passwd" },
		"upper-case id":      func(s *vmtypes.VMSpec) { s.ID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA" },
		"slug with slash":    func(s *vmtypes.VMSpec) { s.ImageSlug = "ubuntu/../x" },
		"version with slash": func(s *vmtypes.VMSpec) { s.ImageVersion = "1/2" },
		"shell in username":  func(s *vmtypes.VMSpec) { s.Username = "a;reboot" },
		"root-like username": func(s *vmtypes.VMSpec) { s.Username = "Root" },
		"yaml in hostname":   func(s *vmtypes.VMSpec) { s.Hostname = "x\nrunc: y" },
		"plain password":     func(s *vmtypes.VMSpec) { s.PasswordHash = "hunter2hunter2" },
		"newline in hash":    func(s *vmtypes.VMSpec) { s.PasswordHash += "\nx" },
		"short bridge token": func(s *vmtypes.VMSpec) { s.BridgeToken = "abc" },
		"zero ram":           func(s *vmtypes.VMSpec) { s.RAMMB = 0 },
		"zero vcpus":         func(s *vmtypes.VMSpec) { s.VCPUs = 0 },
		"zero disk":          func(s *vmtypes.VMSpec) { s.DiskGB = 0 },
	}
	for name, mutate := range bad {
		s := good
		mutate(&s)
		if s.Validate() == "" {
			t.Errorf("%s: accepted", name)
		}
	}
}
