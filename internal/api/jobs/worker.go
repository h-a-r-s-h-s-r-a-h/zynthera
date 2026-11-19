package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"zynthera/internal/api/agentclient"
	"zynthera/internal/api/scheduler"
	"zynthera/internal/api/secrets"
	"zynthera/internal/api/store/sqlcgen"
	"zynthera/internal/api/vms"
	"zynthera/internal/api/vmsvc"
	"zynthera/internal/shared/vmtypes"
)

// Worker handles the queued tasks.
type Worker struct {
	Svc    *vmsvc.Service
	Queue  Enqueuer
	Redis  *redis.Client
	Logger *slog.Logger
}

// Mux returns the task router.
func (w *Worker) Mux() *asynq.ServeMux {
	mux := asynq.NewServeMux()
	mux.Use(w.logging)
	mux.HandleFunc(TypeProvision, w.vmTask(vms.StateProvisioning, w.provision))
	mux.HandleFunc(TypeStart, w.vmTask(vms.StateStarting, w.start))
	mux.HandleFunc(TypeSuspend, w.vmTask(vms.StateSuspending, w.suspend))
	mux.HandleFunc(TypeShutdown, w.vmTask(vms.StateStopping, w.shutdown))
	mux.HandleFunc(TypeForceStop, w.vmTask(vms.StateStopping, w.forceStop))
	mux.HandleFunc(TypeReboot, w.vmTask(vms.StateRebooting, w.reboot))
	mux.HandleFunc(TypeDiscard, w.vmTask(vms.StateMaintenance, w.discard))
	mux.HandleFunc(TypeResize, w.vmTask(vms.StateMaintenance, w.resize))
	mux.HandleFunc(TypeDelete, w.vmTask(vms.StateDeleting, w.deleteVM))
	mux.HandleFunc(TypeSnapshotCreate, w.vmTask(vms.StateMaintenance, w.snapshotCreate))
	mux.HandleFunc(TypeSnapshotRestore, w.vmTask(vms.StateMaintenance, w.snapshotRestore))
	mux.HandleFunc(TypeSnapshotDelete, w.vmTask(vms.StateMaintenance, w.snapshotDelete))

	mux.HandleFunc(TypeCronIdleSweep, w.cronIdleSweep)
	mux.HandleFunc(TypeCronReconcile, w.cronReconcile)
	mux.HandleFunc(TypeCronStuckVMs, w.cronStuckVMs)
	mux.HandleFunc(TypeCronSessionCleanup, w.cronSessionCleanup)
	mux.HandleFunc(TypeCronAccountCleanup, w.cronAccountCleanup)
	return mux
}

// logging logs every task with its attempt number and turns a panic into an
// ordinary task error.
func (w *Worker) logging(next asynq.Handler) asynq.Handler {
	return asynq.HandlerFunc(func(ctx context.Context, t *asynq.Task) (err error) {
		attempt, _ := asynq.GetRetryCount(ctx)
		start := time.Now()
		defer func() {
			if rec := recover(); rec != nil {
				err = fmt.Errorf("panic in task %s: %v", t.Type(), rec)
			}
			attrs := []any{"task", t.Type(), "attempt", attempt + 1, "duration_ms", time.Since(start).Milliseconds()}
			var p Payload
			if json.Unmarshal(t.Payload(), &p) == nil && p.VMID != uuid.Nil {
				attrs = append(attrs, "vm_id", p.VMID.String())
			}
			if err != nil {
				w.Logger.ErrorContext(ctx, "task failed", append(attrs, "error", err.Error())...)
				return
			}
			// The frequent periodic tasks would drown the log.
			if !strings.HasPrefix(t.Type(), "cron:") {
				w.Logger.InfoContext(ctx, "task done", attrs...)
			}
		}()
		return next.ProcessTask(ctx, t)
	})
}

// job is everything one VM task works with.
type job struct {
	p     Payload
	vm    sqlcgen.Vm
	host  sqlcgen.Host
	agent *agentclient.Client
	// lastAttempt is true when asynq will not retry after this run.
	lastAttempt bool
}

func (j *job) actor() string {
	switch j.p.Actor {
	case "user", "admin", "system":
		return j.p.Actor
	}
	return "system"
}

type vmHandler func(ctx context.Context, j *job) error

// vmTask wraps a VM task with the steps every one of them shares: decode the
// payload, load the VM, and stop quietly unless the VM is still in the
// transitional state this task expects. That check makes every task safe to
// run twice.
func (w *Worker) vmTask(expected vms.State, fn vmHandler) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p Payload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return fmt.Errorf("decode payload: %w: %w", err, asynq.SkipRetry)
		}
		vm, err := w.Svc.Q.GetVM(ctx, p.VMID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load vm: %w", err)
		}
		if vms.State(vm.State) != expected {
			return nil
		}
		agent, host, err := w.Svc.Agent(ctx, vm.HostID)
		if err != nil {
			return err
		}
		retried, _ := asynq.GetRetryCount(ctx)
		maxRetry, _ := asynq.GetMaxRetry(ctx)
		j := &job{p: p, vm: vm, host: host, agent: agent, lastAttempt: retried >= maxRetry}
		return fn(ctx, j)
	}
}

// finishCtx gives the bookkeeping after an agent call its own short
// deadline. If the task's deadline has passed, the VM's final state must
// still be written.
func finishCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
}

// giveUp is called when a task fails. While retries remain it just returns
// the error. On the last attempt it moves the VM to "error" with a message a
// person can read, and records the event.
func (w *Worker) giveUp(ctx context.Context, j *job, expected vms.State, cause error, detail string) error {
	if !j.lastAttempt {
		return cause
	}
	fctx, cancel := finishCtx(ctx)
	defer cancel()
	_, _, err := w.Svc.Settle(fctx, j.vm.ID, expected, vms.StateError, vmsvc.SettleOpts{
		Detail: detail,
		Event:  &vmsvc.Event{Type: "error", Actor: "system", Message: detail},
	})
	if err != nil {
		w.Logger.ErrorContext(ctx, "could not mark vm as error", "vm_id", j.vm.ID.String(), "error", err.Error())
	}
	return cause
}

// hostProblem turns an unreachable host into words for the user.
func hostProblem(err error, fallback string) string {
	if errors.Is(err, agentclient.ErrUnavailable) {
		return "The server this PC lives on could not be reached. Please try again in a few minutes."
	}
	return fallback
}

// ---------------------------------------------------------------- provision

func (w *Worker) provision(ctx context.Context, j *job) error {
	const failed = "Setting up this PC failed. You can delete it and create a new one."
	if !j.vm.PendingPasswordHash.Valid {
		j.lastAttempt = true
		return w.giveUp(ctx, j, vms.StateProvisioning, errors.New("no pending password hash"), failed)
	}
	image, err := w.Svc.Q.GetImage(ctx, j.vm.OsImageID)
	if err != nil {
		return fmt.Errorf("load image: %w", err)
	}
	// A fresh file bridge token per attempt: a token from a failed attempt
	// may be on a disk that was thrown away.
	bridgeToken, err := secrets.NewSecret()
	if err != nil {
		return err
	}
	id := j.vm.ID.String()
	spec := vmtypes.VMSpec{
		ID:           id,
		ImageSlug:    image.Slug,
		ImageVersion: j.vm.ImageVersion,
		RAMMB:        int(j.vm.RamMb),
		VCPUs:        int(j.vm.Vcpus),
		DiskGB:       int(j.vm.DiskGb),
		Hostname:     "zynthera-" + strings.ReplaceAll(id, "-", "")[:6],
		Username:     j.vm.LinuxUsername,
		PasswordHash: j.vm.PendingPasswordHash.String,
		BridgeToken:  bridgeToken,
	}
	if err := j.agent.Create(ctx, spec); err != nil {
		return w.giveUp(ctx, j, vms.StateProvisioning, err, hostProblem(err, failed))
	}

	fctx, cancel := finishCtx(ctx)
	defer cancel()
	_, ok, err := w.Svc.Settle(fctx, j.vm.ID, vms.StateProvisioning, vms.StateStarting, vmsvc.SettleOpts{
		Event: &vmsvc.Event{Type: "provisioned", Actor: "system", Message: "PC set up on its host"},
		Extra: func(ctx context.Context, q *sqlcgen.Queries) error {
			// The hash has done its job; it must not stay in the database.
			return q.ClearPendingPassword(ctx, j.vm.ID)
		},
	})
	if err != nil || !ok {
		return err
	}
	return w.Queue.Enqueue(fctx, TypeStart, Payload{VMID: j.vm.ID, Actor: j.actor()})
}

// ---------------------------------------------------------------- start

// stableStateOf is where a VM that is not running belongs.
func stableStateOf(hasSavedState bool) vms.State {
	if hasSavedState {
		return vms.StateSuspended
	}
	return vms.StateStopped
}

func (w *Worker) start(ctx context.Context, j *job) error {
	resuming := j.vm.HasSavedState

	// Re-check capacity. A VM that comes straight from provisioning has not
	// been through the check in Begin, and the host may have filled up.
	view, err := w.Svc.HostView(ctx, w.Svc.Q, j.host, nil)
	if err != nil {
		return err
	}
	// This VM is already counted in the usage, because "starting" holds resources.
	if scheduler.FreeRAMMB(view) < 0 || scheduler.FreeVCPUs(view) < 0 {
		fctx, cancel := finishCtx(ctx)
		defer cancel()
		msg := "This PC could not start because its server is full right now. Please try again later."
		_, _, err := w.Svc.Settle(fctx, j.vm.ID, vms.StateStarting, stableStateOf(resuming), vmsvc.SettleOpts{
			Detail: msg,
			Event:  &vmsvc.Event{Type: "start_refused", Actor: "system", Message: msg},
		})
		return err
	}

	if err := j.agent.Start(ctx, j.vm.ID.String()); err != nil {
		detail := "This PC could not start. Please try again."
		if resuming {
			// The saved session is kept on purpose (spec 12.4): the user decides.
			detail = "This PC could not resume its saved session. Try again, or discard the saved session and start fresh. Your files are not affected."
		}
		if agentclient.IsNotFound(err) {
			j.lastAttempt = true
			detail = "PC not found on its host."
		}
		return w.giveUp(ctx, j, vms.StateStarting, err, hostProblem(err, detail))
	}

	if resuming {
		// The guest clock stood still while the VM slept. Failure here is
		// only a warning: chrony inside the guest corrects it too.
		w.syncClock(ctx, j)
	}

	fctx, cancel := finishCtx(ctx)
	defer cancel()
	ev := &vmsvc.Event{Type: "started", Actor: j.actor(), Message: "PC started"}
	if resuming {
		ev = &vmsvc.Event{Type: "resumed", Actor: j.actor(), Message: "PC resumed where it was left"}
	}
	_, _, err = w.Svc.Settle(fctx, j.vm.ID, vms.StateStarting, vms.StateRunning, vmsvc.SettleOpts{
		HasSavedState: vmsvc.Bool(false),
		Active:        true,
		Event:         ev,
	})
	return err
}

func (w *Worker) syncClock(ctx context.Context, j *job) {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if err = j.agent.SyncClock(ctx, j.vm.ID.String()); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	w.Logger.WarnContext(ctx, "clock sync after resume failed", "vm_id", j.vm.ID.String(), "error", err.Error())
}

// ---------------------------------------------------------------- suspend

func suspendMessage(reason string) string {
	switch reason {
	case "logout":
		return "PC suspended because you logged out"
	case "idle":
		return "PC suspended after being idle"
	case "admin":
		return "PC suspended by an administrator"
	}
	return "PC suspended"
}

func (w *Worker) suspend(ctx context.Context, j *job) error {
	if err := j.agent.Suspend(ctx, j.vm.ID.String()); err != nil {
		return w.giveUp(ctx, j, vms.StateSuspending, err,
			hostProblem(err, "This PC could not be suspended. It may still be running; try Force stop if it does not respond."))
	}
	fctx, cancel := finishCtx(ctx)
	defer cancel()
	_, _, err := w.Svc.Settle(fctx, j.vm.ID, vms.StateSuspending, vms.StateSuspended, vmsvc.SettleOpts{
		HasSavedState: vmsvc.Bool(true),
		Event:         &vmsvc.Event{Type: "suspended", Actor: j.actor(), Message: suspendMessage(j.p.Reason)},
	})
	return err
}

// ---------------------------------------------------------------- shutdown, force stop, reboot

func (w *Worker) shutdown(ctx context.Context, j *job) error {
	// Leave the agent a little less time than the task has, so the answer
	// arrives before the task's own deadline.
	err := j.agent.Shutdown(ctx, j.vm.ID.String(), TaskTimeout(TypeShutdown)-30*time.Second)
	fctx, cancel := finishCtx(ctx)
	defer cancel()
	if err != nil {
		// The guest ignored us, or something else went wrong. Either way
		// the PC is most likely still running: say so and offer force stop.
		msg := "The PC did not shut down in time. If it is stuck, use Force stop."
		_, _, settleErr := w.Svc.Settle(fctx, j.vm.ID, vms.StateStopping, vms.StateRunning, vmsvc.SettleOpts{
			Detail: msg,
			Event:  &vmsvc.Event{Type: "shutdown_failed", Actor: "system", Message: msg},
		})
		if settleErr != nil {
			return settleErr
		}
		if agentclient.IsShutdownTimeout(err) {
			return nil
		}
		return err
	}
	_, _, err = w.Svc.Settle(fctx, j.vm.ID, vms.StateStopping, vms.StateStopped, vmsvc.SettleOpts{
		HasSavedState: vmsvc.Bool(false),
		Event:         &vmsvc.Event{Type: "stopped", Actor: j.actor(), Message: "PC shut down"},
	})
	return err
}

func (w *Worker) forceStop(ctx context.Context, j *job) error {
	// A PC that never made it onto its host is as stopped as it gets.
	if err := j.agent.ForceStop(ctx, j.vm.ID.String()); err != nil && !agentclient.IsNotFound(err) {
		return w.giveUp(ctx, j, vms.StateStopping, err, hostProblem(err, "This PC could not be force stopped."))
	}
	fctx, cancel := finishCtx(ctx)
	defer cancel()
	_, _, err := w.Svc.Settle(fctx, j.vm.ID, vms.StateStopping, vms.StateStopped, vmsvc.SettleOpts{
		HasSavedState: vmsvc.Bool(false),
		Event:         &vmsvc.Event{Type: "stopped", Actor: j.actor(), Message: "PC force stopped"},
	})
	return err
}

func (w *Worker) reboot(ctx context.Context, j *job) error {
	if err := j.agent.Reboot(ctx, j.vm.ID.String()); err != nil {
		return w.giveUp(ctx, j, vms.StateRebooting, err, hostProblem(err, "This PC could not be restarted."))
	}
	fctx, cancel := finishCtx(ctx)
	defer cancel()
	_, _, err := w.Svc.Settle(fctx, j.vm.ID, vms.StateRebooting, vms.StateRunning, vmsvc.SettleOpts{
		Active: true,
		Event:  &vmsvc.Event{Type: "rebooted", Actor: j.actor(), Message: "PC restarted"},
	})
	return err
}

// ---------------------------------------------------------------- discard, resize

func (w *Worker) discard(ctx context.Context, j *job) error {
	if err := j.agent.DiscardSavedState(ctx, j.vm.ID.String()); err != nil && !agentclient.IsNotFound(err) {
		return w.giveUp(ctx, j, vms.StateMaintenance, err, hostProblem(err, "The saved session could not be discarded."))
	}
	fctx, cancel := finishCtx(ctx)
	defer cancel()
	_, _, err := w.Svc.Settle(fctx, j.vm.ID, vms.StateMaintenance, vms.StateStopped, vmsvc.SettleOpts{
		HasSavedState: vmsvc.Bool(false),
		Event:         &vmsvc.Event{Type: "saved_state_discarded", Actor: j.actor(), Message: "Saved session discarded"},
	})
	return err
}

func (w *Worker) resize(ctx context.Context, j *job) error {
	err := j.agent.Resize(ctx, j.vm.ID.String(), j.p.RAMMB, j.p.VCPUs, j.p.DiskGB)
	fctx, cancel := finishCtx(ctx)
	defer cancel()
	if err != nil {
		if !j.lastAttempt {
			return err
		}
		// The PC itself is fine; only the change did not happen.
		msg := hostProblem(err, "The size of this PC could not be changed.")
		if agentclient.IsInvalidState(err) {
			msg = "The size could not be changed. If this PC has snapshots, delete them first and try again."
		}
		_, _, settleErr := w.Svc.Settle(fctx, j.vm.ID, vms.StateMaintenance, vms.StateStopped, vmsvc.SettleOpts{
			Detail: msg,
			Event:  &vmsvc.Event{Type: "resize_failed", Actor: "system", Message: msg},
		})
		if settleErr != nil {
			return settleErr
		}
		return err
	}
	_, _, err = w.Svc.Settle(fctx, j.vm.ID, vms.StateMaintenance, vms.StateStopped, vmsvc.SettleOpts{
		Event: &vmsvc.Event{Type: "resized", Actor: j.actor(),
			Message: fmt.Sprintf("Size changed to %d MB RAM, %d vCPUs, %d GB disk", j.p.RAMMB, j.p.VCPUs, j.p.DiskGB)},
		Extra: func(ctx context.Context, q *sqlcgen.Queries) error {
			return q.UpdateVMSize(ctx, sqlcgen.UpdateVMSizeParams{
				ID:     j.vm.ID,
				RamMb:  int32(j.p.RAMMB),  //nolint:gosec // validated by the handler
				Vcpus:  int32(j.p.VCPUs),  //nolint:gosec // validated by the handler
				DiskGb: int32(j.p.DiskGB), //nolint:gosec // validated by the handler
			})
		},
	})
	return err
}

// ---------------------------------------------------------------- delete

func (w *Worker) deleteVM(ctx context.Context, j *job) error {
	// The agent's delete force stops the VM if needed and tolerates a VM
	// that is already gone.
	if err := j.agent.Delete(ctx, j.vm.ID.String()); err != nil {
		return w.giveUp(ctx, j, vms.StateDeleting, err,
			hostProblem(err, "This PC could not be deleted from its server. Please try deleting it again."))
	}
	fctx, cancel := finishCtx(ctx)
	defer cancel()

	tx, err := w.Svc.Pool.Begin(fctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(fctx)) }()
	q := w.Svc.Q.WithTx(tx)
	vm, err := q.MarkVMDeleted(fctx, j.vm.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("mark deleted: %w", err)
	}
	if err := q.CloseUsageInterval(fctx, vm.ID); err != nil {
		return fmt.Errorf("close usage interval: %w", err)
	}
	err = q.InsertVMEvent(fctx, sqlcgen.InsertVMEventParams{VmID: vm.ID, Type: "deleted", Actor: j.actor(), Message: "PC deleted"})
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	if err := tx.Commit(fctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	w.Svc.Publish(fctx, vm)
	return nil
}

// ---------------------------------------------------------------- snapshots

// settleFromHost asks the host what a VM is really doing and puts the record
// into the matching stable state. It is the way out of a transitional state
// after a job failed halfway, and the core of the stuck-VM check.
func (w *Worker) settleFromHost(ctx context.Context, agent *agentclient.Client, vm sqlcgen.Vm, detail string, ev *vmsvc.Event) error {
	expected := vms.State(vm.State)
	st, err := agent.Status(ctx, vm.ID.String())
	opts := vmsvc.SettleOpts{Detail: detail, Event: ev}
	next := vms.StateError
	switch {
	case err != nil:
		opts.Detail = hostProblem(err, "The state of this PC could not be checked.")
	case st.Status == vmtypes.StatusRunning:
		next, opts.HasSavedState, opts.Active = vms.StateRunning, vmsvc.Bool(false), true
	case st.Status == vmtypes.StatusSuspended:
		next, opts.HasSavedState = vms.StateSuspended, vmsvc.Bool(true)
	case st.Status == vmtypes.StatusStopped:
		next, opts.HasSavedState = vms.StateStopped, vmsvc.Bool(false)
	case st.Status == vmtypes.StatusMissing:
		opts.Detail = "PC not found on its host."
	default:
		opts.Detail = "This PC is in an unexpected condition (" + st.Status + "). Try Force stop, then start it again."
	}
	if next == vms.StateError && opts.Event == nil {
		opts.Event = &vmsvc.Event{Type: "error", Actor: "system", Message: opts.Detail}
	}
	_, _, settleErr := w.Svc.Settle(ctx, vm.ID, expected, next, opts)
	return settleErr
}

func (w *Worker) snapshotCreate(ctx context.Context, j *job) error {
	id, snapID := j.vm.ID.String(), j.p.SnapshotID.String()
	wasRunning := j.p.ReturnTo == string(vms.StateRunning)

	run := func() error {
		// V1 snapshots are disk-only and need the disk closed: a running
		// PC is suspended, snapshotted and started again.
		if wasRunning {
			if err := j.agent.Suspend(ctx, id); err != nil {
				return fmt.Errorf("suspend before snapshot: %w", err)
			}
		}
		if err := j.agent.SnapshotCreate(ctx, id, snapID); err != nil {
			return fmt.Errorf("snapshot: %w", err)
		}
		if wasRunning {
			if err := j.agent.Start(ctx, id); err != nil {
				return fmt.Errorf("start after snapshot: %w", err)
			}
		}
		return nil
	}
	runErr := run()
	fctx, cancel := finishCtx(ctx)
	defer cancel()

	if runErr != nil {
		if !j.lastAttempt {
			return runErr
		}
		if err := w.Svc.Q.SetSnapshotStatus(fctx, sqlcgen.SetSnapshotStatusParams{ID: j.p.SnapshotID, Status: "error"}); err != nil {
			w.Logger.ErrorContext(ctx, "mark snapshot failed", "error", err.Error())
		}
		msg := hostProblem(runErr, "The snapshot could not be taken.")
		if err := w.settleFromHost(fctx, j.agent, j.vm, msg, &vmsvc.Event{Type: "snapshot_failed", Actor: "system", Message: msg}); err != nil {
			return err
		}
		return runErr
	}

	var size int64
	if list, err := j.agent.SnapshotList(fctx, id); err == nil {
		for _, s := range list {
			if s.ID == snapID {
				size = s.SizeBytes
			}
		}
	}
	next := vms.State(j.p.ReturnTo)
	opts := vmsvc.SettleOpts{
		Event: &vmsvc.Event{Type: "snapshot_created", Actor: j.actor(), Message: "Snapshot taken"},
		Extra: func(ctx context.Context, q *sqlcgen.Queries) error {
			return q.SetSnapshotStatus(ctx, sqlcgen.SetSnapshotStatusParams{ID: j.p.SnapshotID, Status: "ready", SizeBytes: size})
		},
	}
	switch next {
	case vms.StateRunning:
		opts.HasSavedState, opts.Active = vmsvc.Bool(false), true
	case vms.StateSuspended, vms.StateStopped:
	default:
		next = stableStateOf(j.vm.HasSavedState)
	}
	_, _, err := w.Svc.Settle(fctx, j.vm.ID, vms.StateMaintenance, next, opts)
	return err
}

func (w *Worker) snapshotRestore(ctx context.Context, j *job) error {
	id := j.vm.ID.String()
	// Memory from now must never be combined with a disk from earlier:
	// the saved session goes first.
	err := j.agent.DiscardSavedState(ctx, id)
	if err == nil {
		err = j.agent.SnapshotRestore(ctx, id, j.p.SnapshotID.String())
	}
	fctx, cancel := finishCtx(ctx)
	defer cancel()
	if err != nil {
		if !j.lastAttempt {
			return err
		}
		msg := hostProblem(err, "The snapshot could not be restored.")
		if settleErr := w.settleFromHost(fctx, j.agent, j.vm, msg, &vmsvc.Event{Type: "snapshot_restore_failed", Actor: "system", Message: msg}); settleErr != nil {
			return settleErr
		}
		return err
	}
	_, _, err = w.Svc.Settle(fctx, j.vm.ID, vms.StateMaintenance, vms.StateStopped, vmsvc.SettleOpts{
		HasSavedState: vmsvc.Bool(false),
		Event:         &vmsvc.Event{Type: "snapshot_restored", Actor: j.actor(), Message: "Snapshot restored"},
	})
	return err
}

func (w *Worker) snapshotDelete(ctx context.Context, j *job) error {
	err := j.agent.SnapshotDelete(ctx, j.vm.ID.String(), j.p.SnapshotID.String())
	fctx, cancel := finishCtx(ctx)
	defer cancel()
	next := vms.State(j.p.ReturnTo)
	if next != vms.StateSuspended && next != vms.StateStopped {
		next = stableStateOf(j.vm.HasSavedState)
	}
	if err != nil && !agentclient.IsNotFound(err) {
		if !j.lastAttempt {
			return err
		}
		msg := hostProblem(err, "The snapshot could not be deleted.")
		_, _, settleErr := w.Svc.Settle(fctx, j.vm.ID, vms.StateMaintenance, next, vmsvc.SettleOpts{
			Detail: msg,
			Event:  &vmsvc.Event{Type: "snapshot_delete_failed", Actor: "system", Message: msg},
		})
		if settleErr != nil {
			return settleErr
		}
		return err
	}
	_, _, err = w.Svc.Settle(fctx, j.vm.ID, vms.StateMaintenance, next, vmsvc.SettleOpts{
		Event: &vmsvc.Event{Type: "snapshot_deleted", Actor: j.actor(), Message: "Snapshot deleted"},
		Extra: func(ctx context.Context, q *sqlcgen.Queries) error {
			return q.DeleteSnapshotRow(ctx, j.p.SnapshotID)
		},
	})
	return err
}
