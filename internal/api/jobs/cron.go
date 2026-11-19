package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/hibiken/asynq"

	"zynthera/internal/api/heartbeats"
	"zynthera/internal/api/scheduler"
	"zynthera/internal/api/store/sqlcgen"
	"zynthera/internal/api/vms"
	"zynthera/internal/api/vmsvc"
	"zynthera/internal/shared/vmtypes"
)

// ---------------------------------------------------------------- idle sweep

// cronIdleSweep suspends running PCs nobody is looking at.
func (w *Worker) cronIdleSweep(ctx context.Context, _ *asynq.Task) error {
	cutoff := time.Now().Add(-time.Duration(w.Svc.Config.IdleSuspendMinutes) * time.Minute)
	idle, err := w.Svc.Q.ListIdleVMs(ctx, cutoff)
	if err != nil {
		return err
	}
	for _, vm := range idle {
		_, _, err := w.Svc.Begin(ctx, vm.ID, vms.ActionSuspend, vmsvc.BeginOpts{})
		if errors.Is(err, vmsvc.ErrInvalidState) || errors.Is(err, vmsvc.ErrNotFound) {
			continue // the owner did something in the meantime
		}
		if err != nil {
			w.Logger.ErrorContext(ctx, "idle sweep: begin suspend", "vm_id", vm.ID.String(), "error", err.Error())
			continue
		}
		if err := w.Queue.Enqueue(ctx, TypeSuspend, Payload{VMID: vm.ID, Actor: "system", Reason: "idle"}); err != nil {
			w.Logger.ErrorContext(ctx, "idle sweep: enqueue suspend", "vm_id", vm.ID.String(), "error", err.Error())
		}
	}
	return nil
}

// ---------------------------------------------------------------- reconcile

// reconcileAction is what to do about one difference between the database
// and the host (the table in spec section 10.7). ok is false when the pair
// needs no action.
func reconcileAction(db vms.State, host string) (next vms.State, savedState bool, ev vmsvc.Event, ok bool) {
	system := func(typ, msg string) vmsvc.Event { return vmsvc.Event{Type: typ, Actor: "system", Message: msg} }
	if host == vmtypes.StatusMissing {
		if db == vms.StateError {
			return "", false, vmsvc.Event{}, false
		}
		return vms.StateError, false, system("error", "PC not found on its host"), true
	}
	switch db {
	case vms.StateRunning:
		switch host {
		case vmtypes.StatusStopped:
			return vms.StateStopped, false, system("stopped", "Shut down from inside the PC"), true
		case vmtypes.StatusSuspended:
			// This is what a host reboot looks like.
			return vms.StateSuspended, true, system("suspended", "PC suspended by its server"), true
		}
	case vms.StateSuspended:
		switch host {
		case vmtypes.StatusRunning:
			return vms.StateRunning, false, system("started", "PC found running on its server"), true
		case vmtypes.StatusStopped:
			return vms.StateStopped, false, system("saved_state_lost", "Saved session was lost"), true
		}
	case vms.StateStopped:
		switch host {
		case vmtypes.StatusRunning:
			return vms.StateRunning, false, system("started", "PC found running on its server"), true
		case vmtypes.StatusSuspended:
			return vms.StateSuspended, true, system("suspended", "PC found suspended on its server"), true
		}
	}
	return "", false, vmsvc.Event{}, false
}

// cronReconcile compares what each online host reported with the database
// and corrects drift. A difference must appear in two consecutive
// heartbeats, both newer than the VM's last state change, so a heartbeat
// that raced with a finishing job cannot cause a wrong correction.
func (w *Worker) cronReconcile(ctx context.Context, _ *asynq.Task) error {
	hosts, err := w.Svc.Q.ListHosts(ctx)
	if err != nil {
		return err
	}
	offlineAfter := time.Duration(w.Svc.Config.HostOfflineAfterSeconds) * time.Second
	for _, host := range hosts {
		if !scheduler.IsOnline(host.LastHeartbeatAt, offlineAfter, time.Now()) {
			continue
		}
		recs, err := heartbeats.LastTwo(ctx, w.Redis, host.ID)
		if err != nil {
			w.Logger.ErrorContext(ctx, "reconcile: load heartbeats", "host", host.Name, "error", err.Error())
			continue
		}
		if len(recs) < 2 {
			continue
		}
		newest, older := recs[0], recs[1]

		dbVMs, err := w.Svc.Q.ListVMsByHost(ctx, host.ID)
		if err != nil {
			w.Logger.ErrorContext(ctx, "reconcile: list vms", "host", host.Name, "error", err.Error())
			continue
		}
		known := make(map[string]bool, len(dbVMs))
		for _, vm := range dbVMs {
			known[vm.ID.String()] = true
			w.reconcileVM(ctx, vm, newest, older)
		}
		for _, reported := range newest.VMs {
			if !known[reported.ID] {
				// Never delete automatically: a person has to look at this.
				w.Logger.WarnContext(ctx, "reconcile: host has a vm the database does not know",
					"host", host.Name, "vm_id", reported.ID, "status", reported.Status)
			}
		}
	}
	return nil
}

func (w *Worker) reconcileVM(ctx context.Context, vm sqlcgen.Vm, newest, older heartbeats.Record) {
	state := vms.State(vm.State)
	// VMs in a transitional state are left alone: a job owns them.
	if !vms.IsStable(state) {
		return
	}
	if !vm.StateChangedAt.Before(older.ReceivedAt) {
		return
	}
	id := vm.ID.String()
	reported := newest.StatusOf(id)
	if reported != older.StatusOf(id) {
		return
	}
	next, saved, ev, ok := reconcileAction(state, reported)
	if !ok {
		return
	}
	opts := vmsvc.SettleOpts{HasSavedState: vmsvc.Bool(saved), Event: &ev}
	if next == vms.StateError {
		opts.Detail = ev.Message + "."
	}
	if _, changed, err := w.Svc.Settle(ctx, vm.ID, state, next, opts); err != nil {
		w.Logger.ErrorContext(ctx, "reconcile: settle", "vm_id", id, "error", err.Error())
	} else if changed {
		w.Logger.WarnContext(ctx, "reconcile: corrected vm state", "vm_id", id, "from", string(state), "to", string(next), "host_says", reported)
	}
}

// ---------------------------------------------------------------- stuck VMs

// stuckAfter is how long a VM may sit in each transitional state: every
// attempt of its job at full length, plus a margin.
var stuckAfter = map[vms.State]time.Duration{
	vms.StateProvisioning: 35 * time.Minute,
	vms.StateStarting:     12 * time.Minute,
	vms.StateSuspending:   32 * time.Minute,
	vms.StateStopping:     6 * time.Minute,
	vms.StateRebooting:    8 * time.Minute,
	vms.StateMaintenance:  24 * time.Minute,
	vms.StateDeleting:     32 * time.Minute,
}

// cronStuckVMs rescues VMs whose job was lost (a crashed worker, a Redis
// outage): it asks the host for the real status and settles the VM into the
// matching stable state, or "error".
func (w *Worker) cronStuckVMs(ctx context.Context, _ *asynq.Task) error {
	states := make([]string, 0, len(stuckAfter))
	for s := range stuckAfter {
		states = append(states, string(s))
	}
	list, err := w.Svc.Q.ListTransitionalVMs(ctx, states)
	if err != nil {
		return err
	}
	for _, vm := range list {
		state := vms.State(vm.State)
		if time.Since(vm.StateChangedAt) < stuckAfter[state] {
			continue
		}
		w.Logger.WarnContext(ctx, "stuck vm", "vm_id", vm.ID.String(), "state", vm.State, "since", vm.StateChangedAt)
		if state == vms.StateDeleting {
			// Deletion must really happen: run the job again.
			if err := w.Queue.Enqueue(ctx, TypeDelete, Payload{VMID: vm.ID, Actor: "system"}); err != nil {
				w.Logger.ErrorContext(ctx, "stuck vm: re-queue delete", "vm_id", vm.ID.String(), "error", err.Error())
			}
			continue
		}
		agent, _, err := w.Svc.Agent(ctx, vm.HostID)
		if err != nil {
			w.Logger.ErrorContext(ctx, "stuck vm: agent", "vm_id", vm.ID.String(), "error", err.Error())
			continue
		}
		if err := w.settleFromHost(ctx, agent, vm, "", nil); err != nil {
			w.Logger.ErrorContext(ctx, "stuck vm: settle", "vm_id", vm.ID.String(), "error", err.Error())
		}
	}
	return nil
}

// ---------------------------------------------------------------- cleanup

func (w *Worker) cronSessionCleanup(ctx context.Context, _ *asynq.Task) error {
	sessions, err := w.Svc.Q.DeleteExpiredSessions(ctx)
	if err != nil {
		return err
	}
	tokens, err := w.Svc.Q.DeleteExpiredEmailTokens(ctx)
	if err != nil {
		return err
	}
	if sessions > 0 || tokens > 0 {
		w.Logger.InfoContext(ctx, "cleanup", "expired_sessions", sessions, "expired_email_tokens", tokens)
	}
	return nil
}
