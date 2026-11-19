// Package mock is a Driver with fake VMs. It lets the whole product run on a
// machine without KVM: state lives in memory and in a JSON file, operations
// take realistic time, and chosen operations can be made to fail.
package mock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"zynthera/internal/agent/driver"
	"zynthera/internal/shared/vmtypes"
)

// Delays are how long each fake operation takes.
type Delays struct {
	Create  time.Duration
	Start   time.Duration
	Suspend time.Duration
	Delete  time.Duration
	Other   time.Duration
}

// RealisticDelays are the defaults from spec section 11.4.
var RealisticDelays = Delays{
	Create:  5 * time.Second,
	Start:   3 * time.Second,
	Suspend: 2 * time.Second,
	Delete:  1 * time.Second,
	Other:   500 * time.Millisecond,
}

// DefaultImages is what a development mock host reports. The slugs and the
// version match the rows `zynthera-api seed` creates.
var DefaultImages = []vmtypes.ImageRef{
	{Slug: "ubuntu-24.04-xfce", Version: "2026.11.2"},
	{Slug: "debian-12-xfce", Version: "2026.11.2"},
}

// Options configures the mock driver.
type Options struct {
	// DataDir holds state.json and one "files" directory per VM.
	DataDir string
	// VNCAddr is the desktop every running mock VM shows.
	VNCAddr string
	// Fail lists operations that should fail: create, start, suspend,
	// shutdown, force_stop, reboot, discard, resize, delete,
	// snapshot_create, snapshot_restore, snapshot_delete.
	Fail []string
	// Delays for each operation. Zero values mean "no delay".
	Delays Delays
	// Images reported as present on this host.
	Images []vmtypes.ImageRef
	// Capacity reported for this host. Zero means the default fake host.
	Capacity vmtypes.Capacity
}

type vm struct {
	Spec          vmtypes.VMSpec `json:"spec"`
	Status        string         `json:"status"`
	HasSavedState bool           `json:"has_saved_state"`
	Snapshots     []string       `json:"snapshots"`
}

// Driver is the mock implementation of driver.Driver.
type Driver struct {
	opts Options
	fail map[string]bool

	mu  sync.Mutex
	vms map[string]*vm
}

var _ driver.Driver = (*Driver)(nil)

// New loads any saved state from opts.DataDir and returns the driver.
func New(opts Options) (*Driver, error) {
	if opts.DataDir == "" {
		return nil, errors.New("mock: DataDir is required")
	}
	// Absolute, because the stand-in file bridges need an absolute home
	// directory (the development default is the relative ./.data/agent).
	abs, err := filepath.Abs(opts.DataDir)
	if err != nil {
		return nil, fmt.Errorf("mock: data dir: %w", err)
	}
	opts.DataDir = abs
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("mock: create data dir: %w", err)
	}
	if opts.Capacity == (vmtypes.Capacity{}) {
		opts.Capacity = vmtypes.Capacity{TotalRAMMB: 32768, TotalVCPUs: 16, TotalDiskGB: 500, FreeDiskGB: 450}
	}
	d := &Driver{opts: opts, fail: map[string]bool{}, vms: map[string]*vm{}}
	for _, op := range opts.Fail {
		d.fail[op] = true
	}

	raw, err := os.ReadFile(d.statePath())
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("mock: read state: %w", err)
	default:
		if err := json.Unmarshal(raw, &d.vms); err != nil {
			return nil, fmt.Errorf("mock: parse state: %w", err)
		}
	}
	return d, nil
}

func (d *Driver) statePath() string { return filepath.Join(d.opts.DataDir, "mock-state.json") }

// FilesDir is the directory that stands in for a mock VM's home directory.
func (d *Driver) FilesDir(id string) string {
	return filepath.Join(d.opts.DataDir, "mock-files", id)
}

// save writes the state file atomically. Callers hold d.mu.
func (d *Driver) save() error {
	raw, err := json.MarshalIndent(d.vms, "", "  ")
	if err != nil {
		return fmt.Errorf("mock: encode state: %w", err)
	}
	tmp := d.statePath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("mock: write state: %w", err)
	}
	if err := os.Rename(tmp, d.statePath()); err != nil {
		return fmt.Errorf("mock: replace state: %w", err)
	}
	return nil
}

// begin simulates the time an operation takes and applies failure injection.
func (d *Driver) begin(ctx context.Context, op string, delay time.Duration) error {
	if delay > 0 {
		t := time.NewTimer(delay)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	failing := d.fail[op]
	d.mu.Unlock()
	if failing {
		return fmt.Errorf("mock: %s failed on purpose (ZYN_MOCK_FAIL)", op)
	}
	return nil
}

// SetFail switches failure injection for one operation on or off while the
// driver runs. Tests use it to make an operation fail and then recover.
func (d *Driver) SetFail(op string, failing bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.fail[op] = failing
}

// get returns the VM or driver.ErrNotFound. Callers hold d.mu.
func (d *Driver) get(id string) (*vm, error) {
	v, ok := d.vms[id]
	if !ok {
		return nil, fmt.Errorf("vm %s: %w", id, driver.ErrNotFound)
	}
	return v, nil
}

// Create implements driver.Driver.
func (d *Driver) Create(ctx context.Context, spec vmtypes.VMSpec) error {
	if err := d.begin(ctx, "create", d.opts.Delays.Create); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if existing, ok := d.vms[spec.ID]; ok {
		// A repeated create keeps a VM that is already in use as it is and
		// only refreshes the parts provisioning may retry with.
		existing.Spec.BridgeToken = spec.BridgeToken
		return d.save()
	}
	if err := os.MkdirAll(d.FilesDir(spec.ID), 0o700); err != nil {
		return fmt.Errorf("mock: create files dir: %w", err)
	}
	d.vms[spec.ID] = &vm{Spec: spec, Status: vmtypes.StatusStopped, Snapshots: []string{}}
	return d.save()
}

// Start implements driver.Driver.
func (d *Driver) Start(ctx context.Context, id string) error {
	if err := d.exists(id); err != nil {
		return err
	}
	if err := d.begin(ctx, "start", d.opts.Delays.Start); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	v, err := d.get(id)
	if err != nil {
		return err
	}
	v.Status = vmtypes.StatusRunning
	v.HasSavedState = false
	return d.save()
}

// exists is a quick check done before the simulated delay, so a call for an
// unknown VM fails immediately like a real hypervisor would.
func (d *Driver) exists(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.get(id)
	return err
}

// Suspend implements driver.Driver.
func (d *Driver) Suspend(ctx context.Context, id string) error {
	if err := d.exists(id); err != nil {
		return err
	}
	if err := d.begin(ctx, "suspend", d.opts.Delays.Suspend); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	v, err := d.get(id)
	if err != nil {
		return err
	}
	if v.Status == vmtypes.StatusRunning {
		v.Status = vmtypes.StatusStopped
		v.HasSavedState = true
	}
	return d.save()
}

// Shutdown implements driver.Driver.
func (d *Driver) Shutdown(ctx context.Context, id string, _ time.Duration) error {
	if err := d.exists(id); err != nil {
		return err
	}
	if err := d.begin(ctx, "shutdown", d.opts.Delays.Other); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fail["shutdown_timeout"] {
		return driver.ErrShutdownTimeout
	}
	v, err := d.get(id)
	if err != nil {
		return err
	}
	if v.Status == vmtypes.StatusRunning {
		v.Status = vmtypes.StatusStopped
	}
	return d.save()
}

// ForceStop implements driver.Driver.
func (d *Driver) ForceStop(ctx context.Context, id string) error {
	if err := d.exists(id); err != nil {
		return err
	}
	if err := d.begin(ctx, "force_stop", d.opts.Delays.Other); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	v, err := d.get(id)
	if err != nil {
		return err
	}
	v.Status = vmtypes.StatusStopped
	v.HasSavedState = false
	return d.save()
}

// Reboot implements driver.Driver.
func (d *Driver) Reboot(ctx context.Context, id string) error {
	if err := d.exists(id); err != nil {
		return err
	}
	if err := d.begin(ctx, "reboot", d.opts.Delays.Other); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	v, err := d.get(id)
	if err != nil {
		return err
	}
	if v.Status != vmtypes.StatusRunning {
		return fmt.Errorf("reboot needs a running vm: %w", driver.ErrInvalidState)
	}
	return nil
}

// DiscardSavedState implements driver.Driver.
func (d *Driver) DiscardSavedState(ctx context.Context, id string) error {
	if err := d.exists(id); err != nil {
		return err
	}
	if err := d.begin(ctx, "discard", d.opts.Delays.Other); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	v, err := d.get(id)
	if err != nil {
		return err
	}
	v.HasSavedState = false
	return d.save()
}

// offWithoutSavedState reports whether the VM's disk and settings may be changed.
func offWithoutSavedState(v *vm) bool {
	return v.Status == vmtypes.StatusStopped && !v.HasSavedState
}

// Resize implements driver.Driver.
func (d *Driver) Resize(ctx context.Context, id string, ramMB, vcpus, diskGB int) error {
	if err := d.exists(id); err != nil {
		return err
	}
	if err := d.begin(ctx, "resize", d.opts.Delays.Other); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	v, err := d.get(id)
	if err != nil {
		return err
	}
	if !offWithoutSavedState(v) {
		return fmt.Errorf("resize needs a vm that is off with no saved state: %w", driver.ErrInvalidState)
	}
	if diskGB < v.Spec.DiskGB {
		return fmt.Errorf("a disk can only grow: %w", driver.ErrInvalidState)
	}
	v.Spec.RAMMB, v.Spec.VCPUs, v.Spec.DiskGB = ramMB, vcpus, diskGB
	return d.save()
}

// Delete implements driver.Driver.
func (d *Driver) Delete(ctx context.Context, id string) error {
	if err := d.begin(ctx, "delete", d.opts.Delays.Delete); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.vms, id)
	if err := os.RemoveAll(d.FilesDir(id)); err != nil {
		return fmt.Errorf("mock: remove files dir: %w", err)
	}
	return d.save()
}

func (d *Driver) statusOf(id string, v *vm) vmtypes.VMStatus {
	status := v.Status
	if status == vmtypes.StatusStopped && v.HasSavedState {
		status = vmtypes.StatusSuspended
	}
	return vmtypes.VMStatus{
		ID:            id,
		Status:        status,
		HasSavedState: v.HasSavedState,
		// A believable, stable number: about 9 GB for the base system.
		DiskUsedBytes: 9 << 30,
		PrivateIP:     "127.0.0.1",
	}
}

// Status implements driver.Driver.
func (d *Driver) Status(_ context.Context, id string) (vmtypes.VMStatus, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v, ok := d.vms[id]
	if !ok {
		return vmtypes.VMStatus{ID: id, Status: vmtypes.StatusMissing}, nil
	}
	return d.statusOf(id, v), nil
}

// List implements driver.Driver.
func (d *Driver) List(_ context.Context) ([]vmtypes.VMStatus, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]vmtypes.VMStatus, 0, len(d.vms))
	for id, v := range d.vms {
		out = append(out, d.statusOf(id, v))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Display implements driver.Driver. Every mock VM shows the same desktop,
// and only while it is running, so the stream closes on suspend like it does
// for a real VM.
func (d *Driver) Display(_ context.Context, id string) (vmtypes.DisplayEndpoint, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v, err := d.get(id)
	if err != nil {
		return vmtypes.DisplayEndpoint{}, err
	}
	if v.Status != vmtypes.StatusRunning {
		return vmtypes.DisplayEndpoint{}, fmt.Errorf("vm is not running: %w", driver.ErrInvalidState)
	}
	return vmtypes.DisplayEndpoint{Network: "tcp", Address: d.opts.VNCAddr}, nil
}

// SyncClock implements driver.Driver.
func (d *Driver) SyncClock(_ context.Context, id string) error { return d.exists(id) }

// GuestAddress implements driver.Driver. Mock VMs have no network; file
// operations use FilesDir instead.
func (d *Driver) GuestAddress(_ context.Context, id string) (string, error) {
	if err := d.exists(id); err != nil {
		return "", err
	}
	return "127.0.0.1", nil
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

// SnapshotCreate implements driver.Driver.
func (d *Driver) SnapshotCreate(ctx context.Context, id, snapshotID string) error {
	if err := d.exists(id); err != nil {
		return err
	}
	if err := d.begin(ctx, "snapshot_create", d.opts.Delays.Other); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	v, err := d.get(id)
	if err != nil {
		return err
	}
	if v.Status == vmtypes.StatusRunning {
		return fmt.Errorf("snapshots need a vm that is suspended or stopped: %w", driver.ErrInvalidState)
	}
	if indexOf(v.Snapshots, snapshotID) < 0 {
		v.Snapshots = append(v.Snapshots, snapshotID)
	}
	return d.save()
}

// SnapshotRestore implements driver.Driver.
func (d *Driver) SnapshotRestore(ctx context.Context, id, snapshotID string) error {
	if err := d.exists(id); err != nil {
		return err
	}
	if err := d.begin(ctx, "snapshot_restore", d.opts.Delays.Other); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	v, err := d.get(id)
	if err != nil {
		return err
	}
	if v.Status == vmtypes.StatusRunning {
		return fmt.Errorf("restore needs a vm that is suspended or stopped: %w", driver.ErrInvalidState)
	}
	if indexOf(v.Snapshots, snapshotID) < 0 {
		return fmt.Errorf("snapshot %s: %w", snapshotID, driver.ErrNotFound)
	}
	// Memory from now must never be combined with a disk from earlier.
	v.HasSavedState = false
	return d.save()
}

// SnapshotDelete implements driver.Driver.
func (d *Driver) SnapshotDelete(ctx context.Context, id, snapshotID string) error {
	if err := d.exists(id); err != nil {
		return err
	}
	if err := d.begin(ctx, "snapshot_delete", d.opts.Delays.Other); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	v, err := d.get(id)
	if err != nil {
		return err
	}
	if v.Status == vmtypes.StatusRunning {
		return fmt.Errorf("deleting a snapshot needs a vm that is suspended or stopped: %w", driver.ErrInvalidState)
	}
	if i := indexOf(v.Snapshots, snapshotID); i >= 0 {
		v.Snapshots = append(v.Snapshots[:i], v.Snapshots[i+1:]...)
	}
	return d.save()
}

// SnapshotList implements driver.Driver.
func (d *Driver) SnapshotList(_ context.Context, id string) ([]vmtypes.SnapshotInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v, err := d.get(id)
	if err != nil {
		return nil, err
	}
	out := make([]vmtypes.SnapshotInfo, 0, len(v.Snapshots))
	for _, s := range v.Snapshots {
		out = append(out, vmtypes.SnapshotInfo{ID: s, SizeBytes: 1 << 30})
	}
	return out, nil
}

// Capacity implements driver.Driver.
func (d *Driver) Capacity(_ context.Context) (vmtypes.Capacity, error) {
	return d.opts.Capacity, nil
}

// Images implements driver.Driver.
func (d *Driver) Images(_ context.Context) ([]vmtypes.ImageRef, error) {
	return append([]vmtypes.ImageRef{}, d.opts.Images...), nil
}

// MockBridgeToken is the token of the in-process file bridges that stand in
// for the bridges inside real guests.
const MockBridgeToken = "mock-bridge-token"

// FileBridgeHandler serves a file bridge for every mock VM under
// /<vm id>/v1/..., each confined to that VM's files directory. The agent
// mounts it on a loopback listener in mock mode, so the file proxy talks
// HTTP to it exactly as it would to a bridge inside a guest.
func (d *Driver) FileBridgeHandler(newBridge func(home, token string) (http.Handler, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/")
		id, sub, ok := strings.Cut(rest, "/")
		if !ok || !vmtypes.IsUUID(id) || d.exists(id) != nil {
			http.NotFound(w, r)
			return
		}
		if err := os.MkdirAll(d.FilesDir(id), 0o700); err != nil {
			http.Error(w, "mock bridge: cannot create files dir", http.StatusInternalServerError)
			return
		}
		h, err := newBridge(d.FilesDir(id), MockBridgeToken)
		if err != nil {
			http.Error(w, "mock bridge: "+err.Error(), http.StatusInternalServerError)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + sub
		h.ServeHTTP(w, r2)
	})
}
