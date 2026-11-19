// Package scheduler decides which host runs a VM and whether a host has room
// (spec section 10.6). The arithmetic here is pure; the callers do the
// database locking around it.
package scheduler

import (
	"sort"
	"time"

	"github.com/google/uuid"
)

// Host is what the scheduler needs to know about one host.
type Host struct {
	ID            uuid.UUID
	AdminState    string // active | draining | disabled
	Online        bool
	TotalRAMMB    int
	TotalVCPUs    int
	TotalDiskGB   int
	ReservedRAMMB int
	CPUOvercommit float64
	// HasImage reports whether the image version a new VM needs is on the host.
	HasImage bool

	// Usage, summed over the host's VMs.
	UsedRAMMB       int   // RAM of VMs that are running or about to run
	UsedVCPUs       int   // vCPUs of the same VMs
	CommittedDiskMB int64 // disk plus saved-memory room of every VM not deleted
}

// IsOnline is the computed "online" rule: a heartbeat arrived recently.
func IsOnline(lastHeartbeat *time.Time, offlineAfter time.Duration, now time.Time) bool {
	return lastHeartbeat != nil && now.Sub(*lastHeartbeat) < offlineAfter
}

// FreeRAMMB is the RAM still available for starting VMs. RAM is never
// overcommitted.
func FreeRAMMB(h Host) int {
	return h.TotalRAMMB - h.ReservedRAMMB - h.UsedRAMMB
}

// FreeVCPUs is the vCPU count still available, with the host's overcommit
// factor applied.
func FreeVCPUs(h Host) float64 {
	return float64(h.TotalVCPUs)*h.CPUOvercommit - float64(h.UsedVCPUs)
}

// DiskNeedMB is the disk a VM commits: its disk plus room for its saved
// memory image.
func DiskNeedMB(diskGB, ramMB int) int64 {
	return int64(diskGB)*1024 + int64(ramMB)
}

// FreeDiskMB is the disk still available for new VMs, after keeping
// headroomPercent of the host's disk untouched.
func FreeDiskMB(h Host, headroomPercent int) int64 {
	usable := int64(h.TotalDiskGB) * 1024 * int64(100-headroomPercent) / 100
	return usable - h.CommittedDiskMB
}

// CanStart reports whether the VM's own host can run it now.
func CanStart(h Host, ramMB, vcpus int) bool {
	return FreeRAMMB(h) >= ramMB && FreeVCPUs(h) >= float64(vcpus)
}

// CanGrow reports whether a stopped VM on this host may grow by the given
// amounts of disk and RAM (which both commit disk).
func CanGrow(h Host, extraDiskGB, extraRAMMB, headroomPercent int) bool {
	return FreeDiskMB(h, headroomPercent) >= DiskNeedMB(extraDiskGB, extraRAMMB)
}

// CanPlace reports whether a new VM may be created on this host. Only
// active, online hosts that have the image and enough free disk qualify. A
// draining host takes no new VMs.
func CanPlace(h Host, diskGB, ramMB, headroomPercent int) bool {
	return h.Online && h.AdminState == "active" && h.HasImage &&
		FreeDiskMB(h, headroomPercent) >= DiskNeedMB(diskGB, ramMB)
}

// Candidates returns the hosts a new VM could go on, best first: the one
// with the most free disk leads, and ties are broken by id so the order is
// stable.
func Candidates(hosts []Host, diskGB, ramMB, headroomPercent int) []Host {
	var out []Host
	for _, h := range hosts {
		if CanPlace(h, diskGB, ramMB, headroomPercent) {
			out = append(out, h)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		fi, fj := FreeDiskMB(out[i], headroomPercent), FreeDiskMB(out[j], headroomPercent)
		if fi != fj {
			return fi > fj
		}
		return out[i].ID.String() < out[j].ID.String()
	})
	return out
}

// Limits are a user's ceilings, summed over all their VMs.
type Limits struct {
	MaxVMs    int
	MaxRAMMB  int
	MaxVCPUs  int
	MaxDiskGB int
}

// Totals are what a user's VMs currently add up to.
type Totals struct {
	VMs    int
	RAMMB  int
	VCPUs  int
	DiskGB int
}

// OverLimit returns the name of the first limit the totals would exceed
// after adding the given amounts, or "" when everything fits.
func OverLimit(l Limits, current, add Totals) string {
	switch {
	case current.VMs+add.VMs > l.MaxVMs:
		return "vms"
	case current.RAMMB+add.RAMMB > l.MaxRAMMB:
		return "ram_mb"
	case current.VCPUs+add.VCPUs > l.MaxVCPUs:
		return "vcpus"
	case current.DiskGB+add.DiskGB > l.MaxDiskGB:
		return "disk_gb"
	}
	return ""
}
