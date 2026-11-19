// Package netalloc reserves a MAC address and an IP address for each VM.
//
// A VM keeps one fixed IP for its whole life. That matters because a
// suspended VM still believes it has its address while asleep; with ordinary
// DHCP leases the address could be handed to another VM in the meantime.
package netalloc

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// Lease is the network identity of one VM.
type Lease struct {
	VMID string `json:"vm_id"`
	MAC  string `json:"mac"`
	IP   string `json:"ip"`
}

// ErrExhausted means every address in the range is taken.
var ErrExhausted = errors.New("netalloc: no free IP address left")

// Allocator hands out leases from one address range. Its state lives in a
// JSON file. Every change happens under a file lock, so the lock is
// host-wide: two agent processes cannot hand out the same address.
type Allocator struct {
	path  string
	first netip.Addr
	last  netip.Addr
	mu    sync.Mutex
}

// New returns an allocator for the inclusive range first..last, keeping its
// state in dir/leases.json.
func New(dir, first, last string) (*Allocator, error) {
	f, err := netip.ParseAddr(first)
	if err != nil {
		return nil, fmt.Errorf("netalloc: first address: %w", err)
	}
	l, err := netip.ParseAddr(last)
	if err != nil {
		return nil, fmt.Errorf("netalloc: last address: %w", err)
	}
	if !f.Is4() || !l.Is4() || l.Less(f) {
		return nil, errors.New("netalloc: the range must be IPv4 with first <= last")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("netalloc: create state dir: %w", err)
	}
	return &Allocator{path: filepath.Join(dir, "leases.json"), first: f, last: l}, nil
}

// withLock runs fn on the current leases while holding the host-wide lock,
// and saves them afterwards when fn asks for it.
func (a *Allocator) withLock(fn func(leases map[string]Lease) (save bool, err error)) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	lock, err := os.OpenFile(a.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("netalloc: open lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil { //nolint:gosec // a file descriptor fits in an int
		return fmt.Errorf("netalloc: lock: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }() //nolint:gosec // as above

	leases := map[string]Lease{}
	raw, err := os.ReadFile(a.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return fmt.Errorf("netalloc: read state: %w", err)
	default:
		if err := json.Unmarshal(raw, &leases); err != nil {
			return fmt.Errorf("netalloc: parse state: %w", err)
		}
	}

	save, err := fn(leases)
	if err != nil || !save {
		return err
	}
	out, err := json.MarshalIndent(leases, "", "  ")
	if err != nil {
		return fmt.Errorf("netalloc: encode state: %w", err)
	}
	// Write to a temporary file and rename, so a crash never leaves a
	// half-written state file.
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("netalloc: write state: %w", err)
	}
	if err := os.Rename(tmp, a.path); err != nil {
		return fmt.Errorf("netalloc: replace state: %w", err)
	}
	return nil
}

func randomMAC() (string, error) {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("netalloc: random mac: %w", err)
	}
	// 52:54:00 is the prefix QEMU and libvirt use for virtual machines.
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", b[0], b[1], b[2]), nil
}

// Reserve returns the VM's lease, creating it if needed. A new lease gets
// the lowest free address and a MAC no other VM has. Calling it again for
// the same VM returns the same lease.
func (a *Allocator) Reserve(vmID string) (Lease, error) {
	var lease Lease
	err := a.withLock(func(leases map[string]Lease) (bool, error) {
		if existing, ok := leases[vmID]; ok {
			lease = existing
			return false, nil
		}
		usedIP := make(map[string]bool, len(leases))
		usedMAC := make(map[string]bool, len(leases))
		for _, l := range leases {
			usedIP[l.IP] = true
			usedMAC[l.MAC] = true
		}

		ip := netip.Addr{}
		for candidate := a.first; ; candidate = candidate.Next() {
			if !usedIP[candidate.String()] {
				ip = candidate
				break
			}
			if candidate == a.last {
				return false, ErrExhausted
			}
		}

		mac := ""
		for mac == "" || usedMAC[mac] {
			m, err := randomMAC()
			if err != nil {
				return false, err
			}
			mac = m
		}

		lease = Lease{VMID: vmID, MAC: mac, IP: ip.String()}
		leases[vmID] = lease
		return true, nil
	})
	return lease, err
}

// Get returns the VM's lease, if it has one.
func (a *Allocator) Get(vmID string) (Lease, bool, error) {
	var lease Lease
	var found bool
	err := a.withLock(func(leases map[string]Lease) (bool, error) {
		lease, found = leases[vmID]
		return false, nil
	})
	return lease, found, err
}

// Release frees the VM's lease. Releasing a VM without a lease is fine.
func (a *Allocator) Release(vmID string) error {
	return a.withLock(func(leases map[string]Lease) (bool, error) {
		if _, ok := leases[vmID]; !ok {
			return false, nil
		}
		delete(leases, vmID)
		return true, nil
	})
}

// All returns every lease.
func (a *Allocator) All() ([]Lease, error) {
	var out []Lease
	err := a.withLock(func(leases map[string]Lease) (bool, error) {
		for _, l := range leases {
			out = append(out, l)
		}
		return false, nil
	})
	return out, err
}
