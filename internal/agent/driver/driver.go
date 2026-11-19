// Package driver defines the interface between the agent and whatever runs
// the VMs. There are two implementations: libvirt (real KVM virtual machines)
// and mock (fake VMs for development and tests).
package driver

import (
	"context"
	"errors"
	"time"

	"zynthera/internal/shared/vmtypes"
)

// Errors every driver uses, so the agent's HTTP layer can map them to
// response codes without knowing which driver is active.
var (
	// ErrNotFound means the VM (or snapshot) does not exist on this host.
	ErrNotFound = errors.New("not found")
	// ErrInvalidState means the operation is not possible in the VM's
	// current state, for example resizing a VM that has a saved state.
	ErrInvalidState = errors.New("invalid state")
	// ErrShutdownTimeout means the guest did not shut down in time. The VM
	// is still running.
	ErrShutdownTimeout = errors.New("guest did not shut down in time")
)

// Driver performs VM operations on one host.
//
// Every method is idempotent: calling it when the VM is already in the
// target condition succeeds. Methods on a VM that does not exist return
// ErrNotFound, except Delete (which succeeds) and Status (which reports
// vmtypes.StatusMissing).
type Driver interface {
	Create(ctx context.Context, spec vmtypes.VMSpec) error // finishes or redoes a partial create
	Start(ctx context.Context, id string) error            // boots, or resumes if a saved state exists
	Suspend(ctx context.Context, id string) error          // saves memory to disk and stops the VM
	// Shutdown asks the guest to shut down cleanly and waits up to timeout.
	Shutdown(ctx context.Context, id string, timeout time.Duration) error
	ForceStop(ctx context.Context, id string) error // power off now; discards saved state
	Reboot(ctx context.Context, id string) error
	DiscardSavedState(ctx context.Context, id string) error
	// Resize changes the VM's size. The VM must be off with no saved state,
	// and the disk can only grow.
	Resize(ctx context.Context, id string, ramMB, vcpus, diskGB int) error
	// Delete removes the domain, disk, seed, saved state, snapshots and IP reservation.
	Delete(ctx context.Context, id string) error

	Status(ctx context.Context, id string) (vmtypes.VMStatus, error)
	List(ctx context.Context) ([]vmtypes.VMStatus, error)
	Display(ctx context.Context, id string) (vmtypes.DisplayEndpoint, error)
	SyncClock(ctx context.Context, id string) error
	GuestAddress(ctx context.Context, id string) (string, error) // for the file bridge

	// Snapshots are disk-only and need the VM to be suspended or stopped.
	SnapshotCreate(ctx context.Context, id, snapshotID string) error
	SnapshotRestore(ctx context.Context, id, snapshotID string) error
	SnapshotDelete(ctx context.Context, id, snapshotID string) error
	SnapshotList(ctx context.Context, id string) ([]vmtypes.SnapshotInfo, error)

	Capacity(ctx context.Context) (vmtypes.Capacity, error)
	Images(ctx context.Context) ([]vmtypes.ImageRef, error)
}
