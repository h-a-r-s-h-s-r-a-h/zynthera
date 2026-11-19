package vms

import (
	"reflect"
	"testing"
)

// The table from spec section 10.4, written out independently of the rules
// in statemachine.go: for each action, the states it is allowed from.
var specTable = map[Action][]State{
	ActionConnect:           {StateRunning},
	ActionStart:             {StateStopped, StateSuspended, StateError},
	ActionSuspend:           {StateRunning},
	ActionShutdown:          {StateRunning},
	ActionForceStop:         {StateRunning, StateError},
	ActionReboot:            {StateRunning},
	ActionDiscardSavedState: {StateSuspended, StateError},
	ActionResize:            {StateStopped},
	ActionSnapshot:          {StateRunning, StateSuspended, StateStopped},
	ActionRestoreSnapshot:   {StateSuspended, StateStopped},
	ActionDeleteSnapshot:    {StateSuspended, StateStopped},
	ActionDelete:            {StateStopped, StateSuspended, StateRunning, StateError},
}

var specTransitional = map[Action]State{
	ActionConnect:           "",
	ActionStart:             StateStarting,
	ActionSuspend:           StateSuspending,
	ActionShutdown:          StateStopping,
	ActionForceStop:         StateStopping,
	ActionReboot:            StateRebooting,
	ActionDiscardSavedState: StateMaintenance,
	ActionResize:            StateMaintenance,
	ActionSnapshot:          StateMaintenance,
	ActionRestoreSnapshot:   StateMaintenance,
	ActionDeleteSnapshot:    StateMaintenance,
	ActionDelete:            StateDeleting,
}

func inList(list []State, s State) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Every action in every state, with and without a saved state.
func TestCanMatchesTheSpecTable(t *testing.T) {
	if len(specTable) != len(AllActions) {
		t.Fatalf("spec table has %d actions, AllActions has %d", len(specTable), len(AllActions))
	}
	for _, action := range AllActions {
		for _, state := range AllStates {
			for _, saved := range []bool{false, true} {
				want := inList(specTable[action], state)
				switch action {
				case ActionDiscardSavedState:
					want = want && saved // only when there is something to discard
				case ActionResize:
					want = want && !saved // never while a saved state exists
				}
				if got := Can(action, state, saved); got != want {
					t.Errorf("Can(%s, %s, saved=%v) = %v, want %v", action, state, saved, got, want)
				}
			}
		}
	}
}

func TestTransitionalStates(t *testing.T) {
	for _, action := range AllActions {
		got := Transitional(action)
		if got != specTransitional[action] {
			t.Errorf("Transitional(%s) = %q, want %q", action, got, specTransitional[action])
		}
		if got != "" && !IsTransitional(got) {
			t.Errorf("Transitional(%s) = %q, which is not a transitional state", action, got)
		}
	}
}

func TestNothingIsAllowedWhileAJobOwnsTheVM(t *testing.T) {
	for _, state := range AllStates {
		if !IsTransitional(state) && state != StateDeleted {
			continue
		}
		for _, saved := range []bool{false, true} {
			if got := Allowed(state, saved); len(got) != 0 {
				t.Errorf("Allowed(%s, saved=%v) = %v, want nothing", state, saved, got)
			}
		}
	}
}

func TestAllowedLists(t *testing.T) {
	cases := []struct {
		state State
		saved bool
		want  []Action
	}{
		{StateRunning, false, []Action{ActionConnect, ActionSuspend, ActionShutdown, ActionForceStop, ActionReboot, ActionSnapshot, ActionDelete}},
		{StateSuspended, true, []Action{ActionStart, ActionDiscardSavedState, ActionSnapshot, ActionRestoreSnapshot, ActionDeleteSnapshot, ActionDelete}},
		{StateStopped, false, []Action{ActionStart, ActionResize, ActionSnapshot, ActionRestoreSnapshot, ActionDeleteSnapshot, ActionDelete}},
		{StateError, false, []Action{ActionStart, ActionForceStop, ActionDelete}},
		{StateError, true, []Action{ActionStart, ActionForceStop, ActionDiscardSavedState, ActionDelete}},
	}
	for _, tc := range cases {
		if got := Allowed(tc.state, tc.saved); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Allowed(%s, saved=%v)\n got  %v\n want %v", tc.state, tc.saved, got, tc.want)
		}
	}
}

func TestEveryStateIsClassifiedExactlyOnce(t *testing.T) {
	for _, s := range AllStates {
		n := 0
		if IsStable(s) {
			n++
		}
		if IsTransitional(s) {
			n++
		}
		if s == StateDeleted {
			n++
		}
		if n != 1 {
			t.Errorf("state %s falls into %d classes, want exactly 1", s, n)
		}
	}
}

func TestHoldsResources(t *testing.T) {
	want := map[State]bool{
		StateStarting: true, StateRunning: true, StateSuspending: true, StateStopping: true, StateRebooting: true,
	}
	for _, s := range AllStates {
		if HoldsResources(s) != want[s] {
			t.Errorf("HoldsResources(%s) = %v", s, HoldsResources(s))
		}
	}
	if len(ResourceStates) != len(want) {
		t.Errorf("ResourceStates has %d entries, want %d", len(ResourceStates), len(want))
	}
	for _, s := range ResourceStates {
		if !HoldsResources(State(s)) {
			t.Errorf("ResourceStates contains %s", s)
		}
	}
}

func TestSourceStatesAndSavedRequirement(t *testing.T) {
	if got := SourceStates(ActionStart); !reflect.DeepEqual(got, []string{"stopped", "suspended", "error"}) {
		t.Errorf("SourceStates(start) = %v", got)
	}
	if got := SourceStates(Action("nonsense")); len(got) != 0 {
		t.Errorf("SourceStates(unknown) = %v", got)
	}
	if v := SavedStateRequirement(ActionDiscardSavedState); v == nil || !*v {
		t.Error("discard must require a saved state")
	}
	if v := SavedStateRequirement(ActionResize); v == nil || *v {
		t.Error("resize must forbid a saved state")
	}
	if v := SavedStateRequirement(ActionStart); v != nil {
		t.Error("start must not care about a saved state")
	}
	if Can(Action("nonsense"), StateRunning, false) {
		t.Error("unknown action allowed")
	}
}
