// Package vms holds the rules of the VM lifecycle as pure functions: which
// states exist, which action is allowed in which state, and what state each
// action leads to (spec section 10.4). Handlers, jobs and the
// "allowed_actions" list shown to the UI all use these functions, so the
// rules exist in exactly one place.
package vms

// State is a value of vms.state.
type State string

// Every VM state.
const (
	StateProvisioning State = "provisioning"
	StateStopped      State = "stopped"
	StateStarting     State = "starting"
	StateRunning      State = "running"
	StateSuspending   State = "suspending"
	StateSuspended    State = "suspended"
	StateStopping     State = "stopping"
	StateRebooting    State = "rebooting"
	StateMaintenance  State = "maintenance"
	StateDeleting     State = "deleting"
	StateDeleted      State = "deleted"
	StateError        State = "error"
)

// AllStates lists every state, for tests and validation.
var AllStates = []State{
	StateProvisioning, StateStopped, StateStarting, StateRunning, StateSuspending,
	StateSuspended, StateStopping, StateRebooting, StateMaintenance, StateDeleting,
	StateDeleted, StateError,
}

// IsStable reports whether no job is working on a VM in this state.
func IsStable(s State) bool {
	switch s {
	case StateStopped, StateRunning, StateSuspended, StateError:
		return true
	}
	return false
}

// IsTransitional reports whether a job owns a VM in this state. The
// transitional state is the lock: nothing else may act on the VM.
func IsTransitional(s State) bool {
	switch s {
	case StateProvisioning, StateStarting, StateSuspending, StateStopping,
		StateRebooting, StateMaintenance, StateDeleting:
		return true
	}
	return false
}

// HoldsResources reports whether a VM in this state occupies RAM and CPU on
// its host (spec section 10.6).
func HoldsResources(s State) bool {
	switch s {
	case StateStarting, StateRunning, StateSuspending, StateStopping, StateRebooting:
		return true
	}
	return false
}

// ResourceStates is HoldsResources as a list, for SQL queries.
var ResourceStates = []string{
	string(StateStarting), string(StateRunning), string(StateSuspending),
	string(StateStopping), string(StateRebooting),
}

// Action is something a user or admin can do with a VM.
type Action string

// Every action. The string values are what the API returns in
// "allowed_actions".
const (
	ActionConnect           Action = "connect"
	ActionStart             Action = "start"
	ActionSuspend           Action = "suspend"
	ActionShutdown          Action = "shutdown"
	ActionForceStop         Action = "force_stop"
	ActionReboot            Action = "reboot"
	ActionDiscardSavedState Action = "discard_saved_state"
	ActionResize            Action = "resize"
	ActionSnapshot          Action = "snapshot"
	ActionRestoreSnapshot   Action = "restore_snapshot"
	ActionDeleteSnapshot    Action = "delete_snapshot"
	ActionDelete            Action = "delete"
)

// AllActions lists every action in the order the UI shows them.
var AllActions = []Action{
	ActionConnect, ActionStart, ActionSuspend, ActionShutdown, ActionForceStop,
	ActionReboot, ActionDiscardSavedState, ActionResize, ActionSnapshot,
	ActionRestoreSnapshot, ActionDeleteSnapshot, ActionDelete,
}

// savedState says what an action needs from vms.has_saved_state.
type savedState int

const (
	savedAny savedState = iota
	savedRequired
	savedForbidden
)

type rule struct {
	from         []State
	transitional State // "" for actions that do not change the state
	saved        savedState
}

var rules = map[Action]rule{
	ActionConnect:           {from: []State{StateRunning}},
	ActionStart:             {from: []State{StateStopped, StateSuspended, StateError}, transitional: StateStarting},
	ActionSuspend:           {from: []State{StateRunning}, transitional: StateSuspending},
	ActionShutdown:          {from: []State{StateRunning}, transitional: StateStopping},
	ActionForceStop:         {from: []State{StateRunning, StateError}, transitional: StateStopping},
	ActionReboot:            {from: []State{StateRunning}, transitional: StateRebooting},
	ActionDiscardSavedState: {from: []State{StateSuspended, StateError}, transitional: StateMaintenance, saved: savedRequired},
	ActionResize:            {from: []State{StateStopped}, transitional: StateMaintenance, saved: savedForbidden},
	ActionSnapshot:          {from: []State{StateRunning, StateSuspended, StateStopped}, transitional: StateMaintenance},
	ActionRestoreSnapshot:   {from: []State{StateSuspended, StateStopped}, transitional: StateMaintenance},
	ActionDeleteSnapshot:    {from: []State{StateSuspended, StateStopped}, transitional: StateMaintenance},
	ActionDelete:            {from: []State{StateStopped, StateSuspended, StateRunning, StateError}, transitional: StateDeleting},
}

// SourceStates returns the states an action may start from, as strings for
// the compare-and-set UPDATE.
func SourceStates(a Action) []string {
	r := rules[a]
	out := make([]string, len(r.from))
	for i, s := range r.from {
		out[i] = string(s)
	}
	return out
}

// Transitional returns the state a VM is put into while the action's job
// runs. It is "" for an unknown action and for ActionConnect.
func Transitional(a Action) State { return rules[a].transitional }

// SavedStateRequirement returns what the action needs from has_saved_state:
// a pointer to the required value, or nil when it does not matter.
func SavedStateRequirement(a Action) *bool {
	switch rules[a].saved {
	case savedRequired:
		v := true
		return &v
	case savedForbidden:
		v := false
		return &v
	}
	return nil
}

// Can reports whether the action is allowed for a VM in the given condition.
func Can(a Action, s State, hasSavedState bool) bool {
	r, ok := rules[a]
	if !ok {
		return false
	}
	switch r.saved {
	case savedRequired:
		if !hasSavedState {
			return false
		}
	case savedForbidden:
		if hasSavedState {
			return false
		}
	}
	for _, from := range r.from {
		if from == s {
			return true
		}
	}
	return false
}

// Allowed returns every action currently possible, in display order. The UI
// enables its buttons from this list and never re-implements the rules.
func Allowed(s State, hasSavedState bool) []Action {
	out := []Action{}
	for _, a := range AllActions {
		if Can(a, s, hasSavedState) {
			out = append(out, a)
		}
	}
	return out
}
