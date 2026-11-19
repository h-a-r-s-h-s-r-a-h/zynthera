package netalloc

import (
	"errors"
	"fmt"
	"regexp"
	"sync"
	"testing"
)

func newAlloc(t *testing.T, dir, first, last string) *Allocator {
	t.Helper()
	a, err := New(dir, first, last)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

var macRe = regexp.MustCompile(`^52:54:00:[0-9a-f]{2}:[0-9a-f]{2}:[0-9a-f]{2}$`)

func TestReserveLowestFreeAndStable(t *testing.T) {
	a := newAlloc(t, t.TempDir(), "10.210.0.10", "10.210.0.20")
	l1, err := a.Reserve("vm-1")
	if err != nil {
		t.Fatal(err)
	}
	l2, _ := a.Reserve("vm-2")
	if l1.IP != "10.210.0.10" || l2.IP != "10.210.0.11" {
		t.Fatalf("ips = %s, %s", l1.IP, l2.IP)
	}
	if !macRe.MatchString(l1.MAC) || l1.MAC == l2.MAC {
		t.Fatalf("macs = %s, %s", l1.MAC, l2.MAC)
	}
	// The same VM always gets the same lease.
	again, _ := a.Reserve("vm-1")
	if again != l1 {
		t.Fatalf("second reserve = %+v, want %+v", again, l1)
	}
	// A freed address is the lowest free one and is reused first.
	if err := a.Release("vm-1"); err != nil {
		t.Fatal(err)
	}
	l3, _ := a.Reserve("vm-3")
	if l3.IP != "10.210.0.10" {
		t.Fatalf("reused ip = %s, want 10.210.0.10", l3.IP)
	}
	if err := a.Release("never-existed"); err != nil {
		t.Fatalf("releasing an unknown vm: %v", err)
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	l1, _ := newAlloc(t, dir, "10.210.0.10", "10.210.0.20").Reserve("vm-1")

	b := newAlloc(t, dir, "10.210.0.10", "10.210.0.20")
	got, found, err := b.Get("vm-1")
	if err != nil || !found || got != l1 {
		t.Fatalf("after restart: %+v found=%v err=%v", got, found, err)
	}
	l2, _ := b.Reserve("vm-2")
	if l2.IP == l1.IP {
		t.Fatal("an address was handed out twice after a restart")
	}
	if _, found, _ := b.Get("vm-9"); found {
		t.Fatal("found a lease that was never made")
	}
}

func TestExhaustion(t *testing.T) {
	a := newAlloc(t, t.TempDir(), "10.210.0.10", "10.210.0.11")
	if _, err := a.Reserve("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Reserve("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Reserve("c"); !errors.Is(err, ErrExhausted) {
		t.Fatalf("error = %v, want ErrExhausted", err)
	}
	// The failed attempt changed nothing.
	if all, _ := a.All(); len(all) != 2 {
		t.Fatalf("leases = %d", len(all))
	}
}

func TestRangeCrossesAnOctet(t *testing.T) {
	a := newAlloc(t, t.TempDir(), "10.210.0.254", "10.210.1.1")
	var got []string
	for i := 0; i < 4; i++ {
		l, err := a.Reserve(fmt.Sprint("vm-", i))
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, l.IP)
	}
	want := []string{"10.210.0.254", "10.210.0.255", "10.210.1.0", "10.210.1.1"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ips = %v, want %v", got, want)
		}
	}
}

// Two allocators on the same directory stand for two agent processes.
func TestConcurrentReservationsNeverCollide(t *testing.T) {
	dir := t.TempDir()
	allocs := []*Allocator{
		newAlloc(t, dir, "10.210.0.10", "10.210.1.250"),
		newAlloc(t, dir, "10.210.0.10", "10.210.1.250"),
	}
	const n = 120
	leases := make([]Lease, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l, err := allocs[i%2].Reserve(fmt.Sprint("vm-", i))
			if err != nil {
				t.Error(err)
			}
			leases[i] = l
		}(i)
	}
	wg.Wait()
	ips, macs := map[string]bool{}, map[string]bool{}
	for _, l := range leases {
		if ips[l.IP] || macs[l.MAC] {
			t.Fatalf("duplicate lease: %+v", l)
		}
		ips[l.IP], macs[l.MAC] = true, true
	}
	if len(ips) != n {
		t.Fatalf("unique ips = %d, want %d", len(ips), n)
	}
}

func TestBadRange(t *testing.T) {
	for _, r := range [][2]string{{"10.210.0.20", "10.210.0.10"}, {"nonsense", "10.210.0.10"}, {"fd00::1", "fd00::9"}} {
		if _, err := New(t.TempDir(), r[0], r[1]); err == nil {
			t.Errorf("range %v accepted", r)
		}
	}
}
