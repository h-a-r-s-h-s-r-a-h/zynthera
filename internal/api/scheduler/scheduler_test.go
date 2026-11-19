package scheduler

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func host(mutate func(*Host)) Host {
	h := Host{
		ID:            uuid.MustParse("00000000-0000-4000-8000-000000000001"),
		AdminState:    "active",
		Online:        true,
		TotalRAMMB:    32768,
		TotalVCPUs:    16,
		TotalDiskGB:   500,
		ReservedRAMMB: 4096,
		CPUOvercommit: 4,
		HasImage:      true,
	}
	if mutate != nil {
		mutate(&h)
	}
	return h
}

func TestFreeRAMIsNeverOvercommitted(t *testing.T) {
	h := host(func(h *Host) { h.UsedRAMMB = 20480 })
	if got := FreeRAMMB(h); got != 32768-4096-20480 {
		t.Fatalf("FreeRAMMB = %d", got)
	}
	if !CanStart(h, 8192, 2) {
		t.Error("8 GB should fit exactly")
	}
	if CanStart(h, 8193, 2) {
		t.Error("1 MB more than free RAM was allowed")
	}
}

func TestVCPUsUseTheOvercommitFactor(t *testing.T) {
	h := host(func(h *Host) { h.UsedVCPUs = 60 }) // 16 * 4 = 64 schedulable
	if !CanStart(h, 2048, 4) {
		t.Error("4 vCPUs should fit exactly")
	}
	if CanStart(h, 2048, 5) {
		t.Error("5 vCPUs fit although only 4 are free")
	}
	h.CPUOvercommit = 1
	if CanStart(h, 2048, 1) {
		t.Error("with no overcommit the host is already over capacity")
	}
}

func TestDiskIncludesSavedMemoryAndHeadroom(t *testing.T) {
	if got := DiskNeedMB(80, 4096); got != 80*1024+4096 {
		t.Fatalf("DiskNeedMB = %d", got)
	}
	h := host(nil) // 500 GB, 10% headroom leaves 450 GB
	if got := FreeDiskMB(h, 10); got != 450*1024 {
		t.Fatalf("FreeDiskMB = %d", got)
	}
	h.CommittedDiskMB = 450*1024 - DiskNeedMB(40, 2048)
	if !CanPlace(h, 40, 2048, 10) {
		t.Error("a VM that fits exactly was refused")
	}
	if CanPlace(h, 40, 2049, 10) {
		t.Error("a VM 1 MB too large was placed")
	}
}

func TestCanPlaceRules(t *testing.T) {
	cases := map[string]struct {
		mutate func(*Host)
		want   bool
	}{
		"healthy host":  {nil, true},
		"offline":       {func(h *Host) { h.Online = false }, false},
		"draining":      {func(h *Host) { h.AdminState = "draining" }, false},
		"disabled":      {func(h *Host) { h.AdminState = "disabled" }, false},
		"missing image": {func(h *Host) { h.HasImage = false }, false},
		"disk full":     {func(h *Host) { h.CommittedDiskMB = 450 * 1024 }, false},
		// RAM in use does not stop creation: a VM only needs RAM to start.
		"ram fully used": {func(h *Host) { h.UsedRAMMB = 28672 }, true},
	}
	for name, tc := range cases {
		if got := CanPlace(host(tc.mutate), 40, 2048, 10); got != tc.want {
			t.Errorf("%s: CanPlace = %v, want %v", name, got, tc.want)
		}
	}
}

func TestCandidatesPreferTheMostFreeDisk(t *testing.T) {
	id := func(n byte) uuid.UUID {
		return uuid.MustParse("00000000-0000-4000-8000-00000000000" + string('0'+rune(n)))
	}
	hosts := []Host{
		host(func(h *Host) { h.ID = id(1); h.CommittedDiskMB = 300 * 1024 }),
		host(func(h *Host) { h.ID = id(2); h.CommittedDiskMB = 100 * 1024 }),
		host(func(h *Host) { h.ID = id(3); h.CommittedDiskMB = 0; h.AdminState = "draining" }),
		host(func(h *Host) { h.ID = id(4); h.CommittedDiskMB = 100 * 1024 }),
		host(func(h *Host) { h.ID = id(5); h.Online = false }),
	}
	got := Candidates(hosts, 40, 2048, 10)
	if len(got) != 3 {
		t.Fatalf("candidates = %d, want 3", len(got))
	}
	// 2 and 4 tie on free disk; the lower id wins. 1 has the least room.
	want := []uuid.UUID{id(2), id(4), id(1)}
	for i := range want {
		if got[i].ID != want[i] {
			t.Errorf("candidate %d = %s, want %s", i, got[i].ID, want[i])
		}
	}
	if len(Candidates(nil, 40, 2048, 10)) != 0 {
		t.Error("candidates from no hosts")
	}
}

func TestCanGrow(t *testing.T) {
	h := host(func(h *Host) { h.CommittedDiskMB = 450*1024 - 41*1024 })
	if !CanGrow(h, 40, 1024, 10) {
		t.Error("growth that fits exactly was refused")
	}
	if CanGrow(h, 40, 1025, 10) {
		t.Error("growth beyond the free disk was allowed")
	}
}

func TestIsOnline(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	if IsOnline(nil, 45*time.Second, now) {
		t.Error("a host that never sent a heartbeat is online")
	}
	if !IsOnline(ago(44*time.Second), 45*time.Second, now) {
		t.Error("a 44-second-old heartbeat counts as offline")
	}
	if IsOnline(ago(45*time.Second), 45*time.Second, now) {
		t.Error("a 45-second-old heartbeat counts as online")
	}
}

func TestOverLimit(t *testing.T) {
	l := Limits{MaxVMs: 2, MaxRAMMB: 16384, MaxVCPUs: 8, MaxDiskGB: 160}
	cur := Totals{VMs: 1, RAMMB: 8192, VCPUs: 4, DiskGB: 80}
	cases := []struct {
		add  Totals
		want string
	}{
		{Totals{VMs: 1, RAMMB: 8192, VCPUs: 4, DiskGB: 80}, ""},
		{Totals{VMs: 2, RAMMB: 1, VCPUs: 1, DiskGB: 1}, "vms"},
		{Totals{VMs: 1, RAMMB: 8193, VCPUs: 1, DiskGB: 1}, "ram_mb"},
		{Totals{VMs: 1, RAMMB: 1, VCPUs: 5, DiskGB: 1}, "vcpus"},
		{Totals{VMs: 1, RAMMB: 1, VCPUs: 1, DiskGB: 81}, "disk_gb"},
		// A resize adds no VM, only the difference in size.
		{Totals{RAMMB: 8192}, ""},
	}
	for _, tc := range cases {
		if got := OverLimit(l, cur, tc.add); got != tc.want {
			t.Errorf("OverLimit(+%+v) = %q, want %q", tc.add, got, tc.want)
		}
	}
}
