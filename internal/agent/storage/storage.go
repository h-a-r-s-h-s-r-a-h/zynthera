// Package storage defines how the agent stores VM disks. Implementations:
// qcow2 files (milestone 5) and ZFS volumes (milestone 7).
package storage

import (
	"context"
	"errors"

	"zynthera/internal/shared/vmtypes"
)

// Errors every storage driver uses.
var (
	// ErrNotFound means the disk, image or snapshot does not exist.
	ErrNotFound = errors.New("storage: not found")
	// ErrInvalid means the request cannot be carried out, for example
	// shrinking a disk.
	ErrInvalid = errors.New("storage: invalid request")
)

// DiskRef tells the domain XML builder what kind of disk a VM has.
type DiskRef struct {
	// Kind is "file" for a qcow2 file or "block" for a raw block device.
	Kind string
	// Path is the file or device path on the host.
	Path string
	// Format is the QEMU driver type: "qcow2" or "raw".
	Format string
}

// Storage manages VM disks on one host.
type Storage interface {
	CloneImage(ctx context.Context, image vmtypes.ImageRef, vmID string, sizeGB int) (DiskRef, error)
	Disk(ctx context.Context, vmID string) (DiskRef, error)
	Delete(ctx context.Context, vmID string) error
	Grow(ctx context.Context, vmID string, sizeGB int) error
	SnapshotCreate(ctx context.Context, vmID, snapshotID string) error
	SnapshotRestore(ctx context.Context, vmID, snapshotID string) error
	SnapshotDelete(ctx context.Context, vmID, snapshotID string) error
	SnapshotList(ctx context.Context, vmID string) ([]vmtypes.SnapshotInfo, error)
	UsedBytes(ctx context.Context, vmID string) (int64, error)
	HostDisk(ctx context.Context) (totalGB, freeGB int, err error)
	Images(ctx context.Context) ([]vmtypes.ImageRef, error)
}
