// Package libvirt is the Driver for real KVM virtual machines. It works by
// running virsh and qemu-img, so every action can be reproduced by hand on
// the host (spec section 12).
package libvirt

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"zynthera/internal/agent/cloudinit"
	"zynthera/internal/agent/domainxml"
	"zynthera/internal/agent/driver"
	"zynthera/internal/agent/netalloc"
	"zynthera/internal/agent/run"
	"zynthera/internal/agent/storage"
	"zynthera/internal/shared/vmtypes"
)

// DiskStore is the storage a libvirt VM lives on: the Storage interface plus
// the directory that holds a VM's seed and metadata.
type DiskStore interface {
	storage.Storage
	VMDir(vmID string) string
}

// Options configures the driver.
type Options struct {
	URI         string // libvirt connection, normally qemu:///system
	Network     string // libvirt network the VMs attach to
	CPUMode     string // host-model, or a named CPU model
	BandwidthKB int    // per-VM network cap in kilobytes per second
	BridgePort  int    // port of the file bridge inside guests
	Store       DiskStore
	Alloc       *netalloc.Allocator
	Runner      run.Runner
}

// Driver implements driver.Driver with libvirt.
type Driver struct {
	o Options
}

var _ driver.Driver = (*Driver)(nil)

// New returns the driver.
func New(o Options) (*Driver, error) {
	switch {
	case o.URI == "" || o.Network == "" || o.CPUMode == "":
		return nil, errors.New("libvirt: URI, network and CPU mode are required")
	case o.Store == nil || o.Alloc == nil || o.Runner == nil:
		return nil, errors.New("libvirt: store, allocator and runner are required")
	case o.BandwidthKB < 1 || o.BridgePort < 1:
		return nil, errors.New("libvirt: bandwidth and bridge port must be positive")
	}
	return &Driver{o: o}, nil
}

// meta is what the agent remembers about a VM, in <vm dir>/meta.json
// (mode 0600). The agent has no database; this file and libvirt are all it
// knows.
type meta struct {
	MAC          string `json:"mac"`
	IP           string `json:"ip"`
	BridgeToken  string `json:"bridge_token"`
	ImageSlug    string `json:"image_slug"`
	ImageVersion string `json:"image_version"`
	RAMMB        int    `json:"ram_mb"`
	VCPUs        int    `json:"vcpus"`
	DiskGB       int    `json:"disk_gb"`
	Hostname     string `json:"hostname"`
	Username     string `json:"username"`
}

func (d *Driver) metaPath(id string) string { return filepath.Join(d.o.Store.VMDir(id), "meta.json") }
func (d *Driver) seedPath(id string) string { return filepath.Join(d.o.Store.VMDir(id), "seed.iso") }

func (d *Driver) readMeta(id string) (meta, error) {
	raw, err := os.ReadFile(d.metaPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return meta{}, fmt.Errorf("vm %s: %w", id, driver.ErrNotFound)
	}
	if err != nil {
		return meta{}, fmt.Errorf("libvirt: read meta: %w", err)
	}
	var m meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return meta{}, fmt.Errorf("libvirt: parse meta: %w", err)
	}
	return m, nil
}

func (d *Driver) writeMeta(id string, m meta) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("libvirt: encode meta: %w", err)
	}
	tmp := d.metaPath(id) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("libvirt: write meta: %w", err)
	}
	if err := os.Rename(tmp, d.metaPath(id)); err != nil {
		return fmt.Errorf("libvirt: replace meta: %w", err)
	}
	return nil
}

// virsh runs one virsh command against the configured connection.
func (d *Driver) virsh(ctx context.Context, args ...string) (string, error) {
	out, err := d.o.Runner.Output(ctx, "virsh", append([]string{"--connect", d.o.URI}, args...)...)
	return strings.TrimSpace(out), err
}

// isNoDomain recognises virsh's answer for a domain that is not defined.
func isNoDomain(err error) bool {
	return run.StderrContains(err, "failed to get domain", "domain not found", "no domain with matching")
}

// Domain states as printed by `virsh domstate`.
const (
	stateRunning = "running"
	stateShutOff = "shut off"
	statePaused  = "paused"
	stateCrashed = "crashed"
	stateMissing = "" // not defined
)

// domstate returns the domain's state, or stateMissing when it is not defined.
func (d *Driver) domstate(ctx context.Context, id string) (string, error) {
	out, err := d.virsh(ctx, "domstate", domainxml.Name(id))
	if isNoDomain(err) {
		return stateMissing, nil
	}
	if err != nil {
		return "", fmt.Errorf("libvirt: domstate: %w", err)
	}
	return out, nil
}

// knownState is domstate for a VM that must exist.
func (d *Driver) knownState(ctx context.Context, id string) (string, error) {
	if !vmtypes.IsUUID(id) {
		return "", fmt.Errorf("vm id %q: %w", id, driver.ErrNotFound)
	}
	state, err := d.domstate(ctx, id)
	if err != nil {
		return "", err
	}
	if state == stateMissing {
		return "", fmt.Errorf("vm %s: %w", id, driver.ErrNotFound)
	}
	return state, nil
}

// hasManagedSave reads the "Managed save" line of `virsh dominfo`.
func (d *Driver) hasManagedSave(ctx context.Context, id string) (bool, error) {
	out, err := d.virsh(ctx, "dominfo", domainxml.Name(id))
	if err != nil {
		return false, fmt.Errorf("libvirt: dominfo: %w", err)
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), ":")
		if ok && strings.TrimSpace(key) == "Managed save" {
			return strings.TrimSpace(value) == "yes", nil
		}
	}
	return false, nil
}

func (d *Driver) removeManagedSave(ctx context.Context, id string) error {
	saved, err := d.hasManagedSave(ctx, id)
	if err != nil || !saved {
		return err
	}
	if _, err := d.virsh(ctx, "managedsave-remove", domainxml.Name(id)); err != nil {
		return fmt.Errorf("libvirt: remove saved state: %w", err)
	}
	return nil
}

// define (re)writes the persistent domain definition from the metadata.
func (d *Driver) define(ctx context.Context, id string, m meta) error {
	disk, err := d.o.Store.Disk(ctx, id)
	if err != nil {
		return fmt.Errorf("libvirt: find disk: %w", err)
	}
	doc, err := domainxml.Build(domainxml.Params{
		ID: id, RAMMB: m.RAMMB, VCPUs: m.VCPUs, Disk: disk, SeedPath: d.seedPath(id),
		MAC: m.MAC, IP: m.IP, Network: d.o.Network, CPUMode: d.o.CPUMode, BandwidthKB: d.o.BandwidthKB,
	})
	if err != nil {
		return err
	}
	path := filepath.Join(d.o.Store.VMDir(id), "domain.xml")
	if err := os.WriteFile(path, doc, 0o600); err != nil {
		return fmt.Errorf("libvirt: write domain xml: %w", err)
	}
	defer func() { _ = os.Remove(path) }()
	if _, err := d.virsh(ctx, "define", "--validate", path); err != nil {
		return fmt.Errorf("libvirt: define domain: %w", err)
	}
	return nil
}

// dhcpEntry adds or removes the static DHCP entry that pins the VM's IP.
func (d *Driver) dhcpEntry(ctx context.Context, action string, lease netalloc.Lease) error {
	host, err := domainxml.DHCPHost(lease.MAC, lease.IP)
	if err != nil {
		return err
	}
	_, err = d.virsh(ctx, "net-update", d.o.Network, action, "ip-dhcp-host", host, "--live", "--config")
	switch {
	case err == nil:
		return nil
	case action == "add" && run.StderrContains(err, "existing dhcp host entry", "already exists"):
		return nil
	case action == "delete" && run.StderrContains(err, "couldn't locate", "no matching", "not found"):
		return nil
	}
	return fmt.Errorf("libvirt: dhcp entry %s: %w", action, err)
}

// Create implements driver.Driver. A VM that is already fully created is
// left as it is. Anything a failed earlier attempt left behind is removed
// and the VM is built from scratch.
func (d *Driver) Create(ctx context.Context, spec vmtypes.VMSpec) error {
	if msg := spec.Validate(); msg != "" {
		return fmt.Errorf("libvirt: invalid spec: %s", msg)
	}
	id := spec.ID
	state, err := d.domstate(ctx, id)
	if err != nil {
		return err
	}
	if _, metaErr := d.readMeta(id); state != stateMissing && metaErr == nil {
		return nil
	}
	if err := d.cleanup(ctx, id, false); err != nil {
		return fmt.Errorf("libvirt: clean up earlier attempt: %w", err)
	}

	lease, err := d.o.Alloc.Reserve(id)
	if err != nil {
		return fmt.Errorf("libvirt: reserve address: %w", err)
	}
	if err := d.dhcpEntry(ctx, "add", lease); err != nil {
		return err
	}
	image := vmtypes.ImageRef{Slug: spec.ImageSlug, Version: spec.ImageVersion}
	if _, err := d.o.Store.CloneImage(ctx, image, id, spec.DiskGB); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return fmt.Errorf("libvirt: image %s version %s is not on this host: %w", spec.ImageSlug, spec.ImageVersion, err)
		}
		return fmt.Errorf("libvirt: clone disk: %w", err)
	}
	_, err = cloudinit.BuildSeed(ctx, d.o.Runner, d.o.Store.VMDir(id), cloudinit.Params{
		VMID: id, Hostname: spec.Hostname, Username: spec.Username,
		PasswordHash: spec.PasswordHash, BridgeToken: spec.BridgeToken, BridgePort: d.o.BridgePort,
	})
	if err != nil {
		return err
	}
	m := meta{
		MAC: lease.MAC, IP: lease.IP, BridgeToken: spec.BridgeToken,
		ImageSlug: spec.ImageSlug, ImageVersion: spec.ImageVersion,
		RAMMB: spec.RAMMB, VCPUs: spec.VCPUs, DiskGB: spec.DiskGB,
		Hostname: spec.Hostname, Username: spec.Username,
	}
	if err := d.writeMeta(id, m); err != nil {
		return err
	}
	return d.define(ctx, id, m)
}

// cleanup removes a VM from the host. Every step tolerates "already gone".
// With keepLease the address reservation stays (used when a create retries).
func (d *Driver) cleanup(ctx context.Context, id string, releaseLease bool) error {
	state, err := d.domstate(ctx, id)
	if err != nil {
		return err
	}
	name := domainxml.Name(id)
	if state == stateRunning || state == statePaused {
		if _, err := d.virsh(ctx, "destroy", name); err != nil && !run.StderrContains(err, "not running", "domain is not running") && !isNoDomain(err) {
			return fmt.Errorf("libvirt: destroy: %w", err)
		}
	}
	if state != stateMissing {
		if _, err := d.virsh(ctx, "undefine", name, "--managed-save", "--snapshots-metadata"); err != nil && !isNoDomain(err) {
			return fmt.Errorf("libvirt: undefine: %w", err)
		}
	}
	if releaseLease {
		lease, found, err := d.o.Alloc.Get(id)
		if err != nil {
			return err
		}
		if found {
			if err := d.dhcpEntry(ctx, "delete", lease); err != nil {
				return err
			}
			if err := d.o.Alloc.Release(id); err != nil {
				return err
			}
		}
	}
	if err := os.RemoveAll(d.o.Store.VMDir(id)); err != nil {
		return fmt.Errorf("libvirt: remove vm dir: %w", err)
	}
	return nil
}

// Start implements driver.Driver. libvirt restores the saved state by
// itself when there is one.
func (d *Driver) Start(ctx context.Context, id string) error {
	state, err := d.knownState(ctx, id)
	if err != nil {
		return err
	}
	if state == stateRunning {
		return nil
	}
	if state == statePaused {
		if _, err := d.virsh(ctx, "resume", domainxml.Name(id)); err != nil {
			return fmt.Errorf("libvirt: resume paused domain: %w", err)
		}
		return nil
	}
	if _, err := d.virsh(ctx, "start", domainxml.Name(id)); err != nil {
		if run.StderrContains(err, "already active", "is already running") {
			return nil
		}
		// A failed restore leaves the VM shut off with its saved state.
		// It is not removed here: the user decides (spec 12.4).
		return fmt.Errorf("libvirt: start: %w", err)
	}
	return nil
}

// Suspend implements driver.Driver: memory goes to disk and QEMU exits.
func (d *Driver) Suspend(ctx context.Context, id string) error {
	state, err := d.knownState(ctx, id)
	if err != nil {
		return err
	}
	if state != stateRunning && state != statePaused {
		return nil
	}
	if _, err := d.virsh(ctx, "managedsave", domainxml.Name(id)); err != nil {
		if run.StderrContains(err, "not running", "domain is not running") {
			return nil
		}
		return fmt.Errorf("libvirt: managedsave: %w", err)
	}
	return nil
}

// requestShutdown asks the guest to power off: through the guest agent
// first, and with the ACPI power button if the agent does not answer.
func (d *Driver) requestShutdown(ctx context.Context, name string) error {
	if _, err := d.virsh(ctx, "shutdown", name, "--mode", "agent"); err == nil {
		return nil
	}
	_, err := d.virsh(ctx, "shutdown", name, "--mode", "acpi")
	if err != nil && !run.StderrContains(err, "not running") {
		return fmt.Errorf("libvirt: shutdown: %w", err)
	}
	return nil
}

// Shutdown implements driver.Driver: ask the guest to power off and wait for
// the domain to switch off.
//
// The request is repeated every few seconds. A guest that is still booting
// has no agent running yet and ignores the power button, so a single
// request sent too early would simply be lost.
func (d *Driver) Shutdown(ctx context.Context, id string, timeout time.Duration) error {
	state, err := d.knownState(ctx, id)
	if err != nil {
		return err
	}
	if state != stateRunning && state != statePaused {
		return nil
	}
	name := domainxml.Name(id)
	deadline := time.Now().Add(timeout)
	const resendEvery = 5 * time.Second
	var lastRequest time.Time
	for {
		if time.Since(lastRequest) >= resendEvery {
			if err := d.requestShutdown(ctx, name); err != nil {
				return err
			}
			lastRequest = time.Now()
		}
		state, err := d.domstate(ctx, id)
		if err != nil {
			return err
		}
		if state == stateShutOff || state == stateMissing {
			return nil
		}
		if time.Now().After(deadline) {
			return driver.ErrShutdownTimeout
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// ForceStop implements driver.Driver.
func (d *Driver) ForceStop(ctx context.Context, id string) error {
	state, err := d.knownState(ctx, id)
	if err != nil {
		return err
	}
	if state == stateRunning || state == statePaused {
		if _, err := d.virsh(ctx, "destroy", domainxml.Name(id)); err != nil && !run.StderrContains(err, "not running") {
			return fmt.Errorf("libvirt: destroy: %w", err)
		}
	}
	return d.removeManagedSave(ctx, id)
}

// Reboot implements driver.Driver.
func (d *Driver) Reboot(ctx context.Context, id string) error {
	state, err := d.knownState(ctx, id)
	if err != nil {
		return err
	}
	if state != stateRunning {
		return fmt.Errorf("reboot needs a running vm: %w", driver.ErrInvalidState)
	}
	if _, err := d.virsh(ctx, "reboot", domainxml.Name(id)); err != nil {
		return fmt.Errorf("libvirt: reboot: %w", err)
	}
	return nil
}

// DiscardSavedState implements driver.Driver.
func (d *Driver) DiscardSavedState(ctx context.Context, id string) error {
	if _, err := d.knownState(ctx, id); err != nil {
		return err
	}
	return d.removeManagedSave(ctx, id)
}

// requireOff reports ErrInvalidState unless the VM is shut off, and tells
// whether it has a saved state.
func (d *Driver) requireOff(ctx context.Context, id, what string) (saved bool, err error) {
	state, err := d.knownState(ctx, id)
	if err != nil {
		return false, err
	}
	if state != stateShutOff {
		return false, fmt.Errorf("%s needs a vm that is not running: %w", what, driver.ErrInvalidState)
	}
	return d.hasManagedSave(ctx, id)
}

// Resize implements driver.Driver. RAM, CPU count and devices must never
// change while a saved state exists: it could not be restored afterwards.
func (d *Driver) Resize(ctx context.Context, id string, ramMB, vcpus, diskGB int) error {
	saved, err := d.requireOff(ctx, id, "resize")
	if err != nil {
		return err
	}
	if saved {
		return fmt.Errorf("resize is not possible while a saved state exists: %w", driver.ErrInvalidState)
	}
	m, err := d.readMeta(id)
	if err != nil {
		return err
	}
	if diskGB < m.DiskGB {
		return fmt.Errorf("a disk can only grow: %w", driver.ErrInvalidState)
	}
	if err := d.o.Store.Grow(ctx, id, diskGB); err != nil {
		if errors.Is(err, storage.ErrInvalid) {
			return fmt.Errorf("%w: %w", driver.ErrInvalidState, err)
		}
		return fmt.Errorf("libvirt: grow disk: %w", err)
	}
	m.RAMMB, m.VCPUs, m.DiskGB = ramMB, vcpus, diskGB
	if err := d.define(ctx, id, m); err != nil {
		return err
	}
	return d.writeMeta(id, m)
}

// Delete implements driver.Driver: domain, disk, seed, saved state,
// snapshots and the address reservation all go.
func (d *Driver) Delete(ctx context.Context, id string) error {
	if !vmtypes.IsUUID(id) {
		return nil
	}
	return d.cleanup(ctx, id, true)
}

// Status implements driver.Driver (the mapping in spec section 12.3).
func (d *Driver) Status(ctx context.Context, id string) (vmtypes.VMStatus, error) {
	st := vmtypes.VMStatus{ID: id, Status: vmtypes.StatusMissing}
	if !vmtypes.IsUUID(id) {
		return st, nil
	}
	state, err := d.domstate(ctx, id)
	if err != nil {
		return st, err
	}
	switch state {
	case stateMissing:
		return st, nil
	case stateRunning:
		st.Status = vmtypes.StatusRunning
	case statePaused:
		st.Status = vmtypes.StatusPaused
	case stateCrashed:
		st.Status = vmtypes.StatusCrashed
	default:
		// "shut off", and the short-lived "in shutdown" and "pmsuspended".
		st.Status = vmtypes.StatusStopped
		if state != stateShutOff {
			st.Status = vmtypes.StatusRunning
		}
	}
	if state == stateShutOff {
		saved, err := d.hasManagedSave(ctx, id)
		if err != nil {
			return st, err
		}
		st.HasSavedState = saved
		if saved {
			st.Status = vmtypes.StatusSuspended
		}
	}
	if used, err := d.o.Store.UsedBytes(ctx, id); err == nil {
		st.DiskUsedBytes = used
	}
	if m, err := d.readMeta(id); err == nil {
		st.PrivateIP = m.IP
	}
	return st, nil
}

// List implements driver.Driver: every domain whose name starts with "zyn-".
func (d *Driver) List(ctx context.Context) ([]vmtypes.VMStatus, error) {
	out, err := d.virsh(ctx, "list", "--all", "--name")
	if err != nil {
		return nil, fmt.Errorf("libvirt: list: %w", err)
	}
	list := []vmtypes.VMStatus{}
	for _, name := range strings.Fields(out) {
		id, ok := strings.CutPrefix(name, "zyn-")
		if !ok || !vmtypes.IsUUID(id) {
			continue
		}
		st, err := d.Status(ctx, id)
		if err != nil {
			return nil, err
		}
		if st.Status != vmtypes.StatusMissing {
			list = append(list, st)
		}
	}
	return list, nil
}

// Display implements driver.Driver: the VNC port QEMU picked on loopback.
func (d *Driver) Display(ctx context.Context, id string) (vmtypes.DisplayEndpoint, error) {
	state, err := d.knownState(ctx, id)
	if err != nil {
		return vmtypes.DisplayEndpoint{}, err
	}
	if state != stateRunning {
		return vmtypes.DisplayEndpoint{}, fmt.Errorf("vm is not running: %w", driver.ErrInvalidState)
	}
	out, err := d.virsh(ctx, "vncdisplay", domainxml.Name(id))
	if err != nil {
		return vmtypes.DisplayEndpoint{}, fmt.Errorf("libvirt: vncdisplay: %w", err)
	}
	addr, err := domainxml.VNCPort(out)
	if err != nil {
		return vmtypes.DisplayEndpoint{}, err
	}
	return vmtypes.DisplayEndpoint{Network: "tcp", Address: addr}, nil
}

// SyncClock implements driver.Driver. After a resume the guest clock is
// behind by the time the VM spent asleep.
//
// The normal way is the guest agent's set-time command (`virsh domtime`).
// That command needs the hwclock program inside the guest, which some
// distributions no longer install by default (Ubuntu 24.04 moved it to the
// util-linux-extra package). So if it fails, the driver runs `date` inside
// the guest through the agent's exec command and sets the clock to the
// host's time. (Asking chrony to step the clock does not help at this
// point: chrony only learns that the clock jumped at its next poll.)
//
// Both ways need the guest agent, which may not answer yet right after a
// resume; the caller retries.
func (d *Driver) SyncClock(ctx context.Context, id string) error {
	if _, err := d.knownState(ctx, id); err != nil {
		return err
	}
	name := domainxml.Name(id)
	_, domtimeErr := d.virsh(ctx, "domtime", name, "--now")
	if domtimeErr == nil {
		return nil
	}
	now := time.Now()
	stamp := fmt.Sprintf("@%d.%09d", now.Unix(), now.Nanosecond())
	cmd, err := json.Marshal(map[string]any{
		"execute":   "guest-exec",
		"arguments": map[string]any{"path": "/bin/date", "arg": []string{"-u", "-s", stamp}},
	})
	if err != nil {
		return fmt.Errorf("libvirt: encode agent command: %w", err)
	}
	if _, err := d.virsh(ctx, "qemu-agent-command", name, string(cmd)); err != nil {
		return fmt.Errorf("libvirt: sync clock: domtime failed (%w) and setting the date failed (%w)", domtimeErr, err)
	}
	return nil
}

// GuestAddress implements driver.Driver: the IP reserved at creation.
func (d *Driver) GuestAddress(_ context.Context, id string) (string, error) {
	m, err := d.readMeta(id)
	if err != nil {
		return "", err
	}
	return m.IP, nil
}

// BridgeToken returns the secret the agent presents to the VM's file bridge.
func (d *Driver) BridgeToken(id string) (string, error) {
	m, err := d.readMeta(id)
	if err != nil {
		return "", err
	}
	return m.BridgeToken, nil
}

func mapStorageErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrNotFound):
		return fmt.Errorf("%w: %w", driver.ErrNotFound, err)
	case errors.Is(err, storage.ErrInvalid):
		return fmt.Errorf("%w: %w", driver.ErrInvalidState, err)
	}
	return err
}

// SnapshotCreate implements driver.Driver. V1 snapshots are disk-only and
// are taken while no QEMU process has the disk open.
func (d *Driver) SnapshotCreate(ctx context.Context, id, snapshotID string) error {
	if _, err := d.requireOff(ctx, id, "a snapshot"); err != nil {
		return err
	}
	return mapStorageErr(d.o.Store.SnapshotCreate(ctx, id, snapshotID))
}

// SnapshotRestore implements driver.Driver. The saved memory state is
// removed first: memory from now must never meet a disk from earlier.
func (d *Driver) SnapshotRestore(ctx context.Context, id, snapshotID string) error {
	if _, err := d.requireOff(ctx, id, "restoring a snapshot"); err != nil {
		return err
	}
	// Check that the snapshot exists before throwing the saved state away.
	list, err := d.o.Store.SnapshotList(ctx, id)
	if err != nil {
		return mapStorageErr(err)
	}
	found := false
	for _, s := range list {
		found = found || s.ID == snapshotID
	}
	if !found {
		return fmt.Errorf("snapshot %s: %w", snapshotID, driver.ErrNotFound)
	}
	if err := d.removeManagedSave(ctx, id); err != nil {
		return err
	}
	return mapStorageErr(d.o.Store.SnapshotRestore(ctx, id, snapshotID))
}

// SnapshotDelete implements driver.Driver.
func (d *Driver) SnapshotDelete(ctx context.Context, id, snapshotID string) error {
	if _, err := d.requireOff(ctx, id, "deleting a snapshot"); err != nil {
		return err
	}
	return mapStorageErr(d.o.Store.SnapshotDelete(ctx, id, snapshotID))
}

// SnapshotList implements driver.Driver.
func (d *Driver) SnapshotList(ctx context.Context, id string) ([]vmtypes.SnapshotInfo, error) {
	if _, err := d.knownState(ctx, id); err != nil {
		return nil, err
	}
	list, err := d.o.Store.SnapshotList(ctx, id)
	return list, mapStorageErr(err)
}

// hostRAMMB reads MemTotal from /proc/meminfo.
func hostRAMMB() (int, error) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, fmt.Errorf("libvirt: read meminfo: %w", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.Atoi(fields[1])
			if err != nil {
				break
			}
			return kb / 1024, nil
		}
	}
	return 0, errors.New("libvirt: MemTotal not found in /proc/meminfo")
}

// Capacity implements driver.Driver.
func (d *Driver) Capacity(ctx context.Context) (vmtypes.Capacity, error) {
	ram, err := hostRAMMB()
	if err != nil {
		return vmtypes.Capacity{}, err
	}
	total, free, err := d.o.Store.HostDisk(ctx)
	if err != nil {
		return vmtypes.Capacity{}, err
	}
	return vmtypes.Capacity{TotalRAMMB: ram, TotalVCPUs: runtime.NumCPU(), TotalDiskGB: total, FreeDiskGB: free}, nil
}

// Images implements driver.Driver.
func (d *Driver) Images(ctx context.Context) ([]vmtypes.ImageRef, error) {
	return d.o.Store.Images(ctx)
}
