package jobs

import (
	"testing"

	"zynthera/internal/api/vms"
	"zynthera/internal/shared/vmtypes"
)

// The table from spec section 10.7.
func TestReconcileAction(t *testing.T) {
	cases := []struct {
		db        vms.State
		host      string
		wantOK    bool
		wantNext  vms.State
		wantSaved bool
		wantMsg   string
	}{
		{vms.StateRunning, vmtypes.StatusStopped, true, vms.StateStopped, false, "Shut down from inside the PC"},
		{vms.StateRunning, vmtypes.StatusSuspended, true, vms.StateSuspended, true, ""},
		{vms.StateSuspended, vmtypes.StatusRunning, true, vms.StateRunning, false, ""},
		{vms.StateStopped, vmtypes.StatusRunning, true, vms.StateRunning, false, ""},
		{vms.StateSuspended, vmtypes.StatusStopped, true, vms.StateStopped, false, "Saved session was lost"},
		{vms.StateStopped, vmtypes.StatusSuspended, true, vms.StateSuspended, true, ""},
		{vms.StateRunning, vmtypes.StatusMissing, true, vms.StateError, false, "PC not found on its host"},
		{vms.StateSuspended, vmtypes.StatusMissing, true, vms.StateError, false, "PC not found on its host"},
		{vms.StateStopped, vmtypes.StatusMissing, true, vms.StateError, false, "PC not found on its host"},
		// Agreement, and conditions that need a person, lead to no action.
		{vms.StateRunning, vmtypes.StatusRunning, false, "", false, ""},
		{vms.StateSuspended, vmtypes.StatusSuspended, false, "", false, ""},
		{vms.StateStopped, vmtypes.StatusStopped, false, "", false, ""},
		{vms.StateRunning, vmtypes.StatusPaused, false, "", false, ""},
		{vms.StateRunning, vmtypes.StatusCrashed, false, "", false, ""},
		{vms.StateError, vmtypes.StatusMissing, false, "", false, ""},
		{vms.StateError, vmtypes.StatusRunning, false, "", false, ""},
	}
	for _, tc := range cases {
		next, saved, ev, ok := reconcileAction(tc.db, tc.host)
		if ok != tc.wantOK || next != tc.wantNext || saved != tc.wantSaved {
			t.Errorf("reconcileAction(%s, %s) = (%s, %v, ok=%v), want (%s, %v, ok=%v)",
				tc.db, tc.host, next, saved, ok, tc.wantNext, tc.wantSaved, tc.wantOK)
		}
		if tc.wantMsg != "" && ev.Message != tc.wantMsg {
			t.Errorf("reconcileAction(%s, %s) message = %q, want %q", tc.db, tc.host, ev.Message, tc.wantMsg)
		}
		if ok && (ev.Actor != "system" || ev.Type == "") {
			t.Errorf("reconcileAction(%s, %s) event = %+v", tc.db, tc.host, ev)
		}
	}
}

func TestEveryTaskTypeHasASpec(t *testing.T) {
	for _, typ := range []string{TypeProvision, TypeStart, TypeSuspend, TypeShutdown, TypeForceStop, TypeReboot,
		TypeDiscard, TypeResize, TypeDelete, TypeSnapshotCreate, TypeSnapshotRestore, TypeSnapshotDelete} {
		spec, ok := taskSpecs[typ]
		if !ok || spec.timeout <= 0 || spec.queue == "" {
			t.Errorf("task %s has no usable spec: %+v", typ, spec)
		}
	}
	// Every transitional state the stuck-VM check knows must outlast the
	// longest job that can hold it.
	if stuckAfter[vms.StateStarting] <= TaskTimeout(TypeStart) || stuckAfter[vms.StateDeleting] <= TaskTimeout(TypeDelete) {
		t.Error("a stuck-VM threshold is shorter than its job's timeout")
	}
	for _, s := range vms.AllStates {
		if _, known := stuckAfter[s]; vms.IsTransitional(s) != known {
			t.Errorf("state %s: transitional=%v but in stuckAfter=%v", s, vms.IsTransitional(s), known)
		}
	}
}
