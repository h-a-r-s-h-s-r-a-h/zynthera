// Package vmsvc is the one place that changes a VM's state. HTTP handlers
// call Begin to move a VM into a transitional state; jobs call Settle to move
// it out again. Both write the audit event, keep the usage intervals right
// and publish the change to the owner's browsers.
package vmsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"zynthera/internal/api/agentclient"
	"zynthera/internal/api/appconfig"
	"zynthera/internal/api/scheduler"
	"zynthera/internal/api/secrets"
	"zynthera/internal/api/store/sqlcgen"
	"zynthera/internal/api/vms"
	"zynthera/internal/shared/vmtypes"
)

// Errors returned by Begin and Create. Handlers map them to API error codes.
var (
	ErrNotFound        = errors.New("vm not found")
	ErrInvalidState    = errors.New("action not allowed in the vm's current state")
	ErrNoCapacity      = errors.New("no capacity")
	ErrHostUnavailable = errors.New("host unavailable")
)

// Publisher sends a VM object to its owner's live listeners.
type Publisher interface {
	Publish(ctx context.Context, userID uuid.UUID, vmJSON []byte) error
}

// Service bundles what state changes need.
type Service struct {
	Pool    *pgxpool.Pool
	Q       *sqlcgen.Queries
	Config  appconfig.Config
	Secrets *secrets.Box
	Logger  *slog.Logger
	Events  Publisher
	// Now is the clock; tests may replace it.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ---------------------------------------------------------------- JSON

// ImageJSON is the short image description inside a VM object.
type ImageJSON struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

// VMJSON is the VM object returned by every endpoint and sent on the event
// stream (spec section 10.3).
type VMJSON struct {
	ID              uuid.UUID  `json:"id"`
	Name            string     `json:"name"`
	Image           ImageJSON  `json:"image"`
	RAMMB           int32      `json:"ram_mb"`
	VCPUs           int32      `json:"vcpus"`
	DiskGB          int32      `json:"disk_gb"`
	DiskUsedBytes   int64      `json:"disk_used_bytes"`
	LinuxUsername   string     `json:"linux_username"`
	State           string     `json:"state"`
	StateDetail     *string    `json:"state_detail"`
	HasSavedState   bool       `json:"has_saved_state"`
	ViewerConnected bool       `json:"viewer_connected"`
	KeepAliveUntil  *time.Time `json:"keep_alive_until"`
	LastActiveAt    *time.Time `json:"last_active_at"`
	CreatedAt       time.Time  `json:"created_at"`
	AllowedActions  []string   `json:"allowed_actions"`
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// ToJSON builds the API object for a VM.
func ToJSON(vm sqlcgen.Vm, image sqlcgen.OsImage) VMJSON {
	out := VMJSON{
		ID:              vm.ID,
		Name:            vm.Name,
		Image:           ImageJSON{Slug: image.Slug, DisplayName: image.DisplayName},
		RAMMB:           vm.RamMb,
		VCPUs:           vm.Vcpus,
		DiskGB:          vm.DiskGb,
		DiskUsedBytes:   vm.DiskUsedBytes,
		LinuxUsername:   vm.LinuxUsername,
		State:           vm.State,
		HasSavedState:   vm.HasSavedState,
		ViewerConnected: vm.ViewerConnected,
		KeepAliveUntil:  utcPtr(vm.KeepAliveUntil),
		LastActiveAt:    utcPtr(vm.LastActiveAt),
		CreatedAt:       vm.CreatedAt.UTC(),
		AllowedActions:  []string{},
	}
	if vm.StateDetail.Valid {
		out.StateDetail = &vm.StateDetail.String
	}
	for _, a := range vms.Allowed(vms.State(vm.State), vm.HasSavedState) {
		out.AllowedActions = append(out.AllowedActions, string(a))
	}
	return out
}

// JSON loads the VM's image and builds the API object.
func (s *Service) JSON(ctx context.Context, vm sqlcgen.Vm) (VMJSON, error) {
	image, err := s.Q.GetImage(ctx, vm.OsImageID)
	if err != nil {
		return VMJSON{}, fmt.Errorf("load image of vm %s: %w", vm.ID, err)
	}
	return ToJSON(vm, image), nil
}

// Publish sends the VM's current state to its owner's browsers. A failure is
// logged, never returned: the change itself already happened, and the UI
// catches up on its next refresh.
func (s *Service) Publish(ctx context.Context, vm sqlcgen.Vm) {
	if s.Events == nil {
		return
	}
	obj, err := s.JSON(ctx, vm)
	if err != nil {
		s.Logger.WarnContext(ctx, "publish: build vm json", "vm_id", vm.ID.String(), "error", err.Error())
		return
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		s.Logger.WarnContext(ctx, "publish: encode vm json", "vm_id", vm.ID.String(), "error", err.Error())
		return
	}
	if err := s.Events.Publish(ctx, vm.UserID, raw); err != nil {
		s.Logger.WarnContext(ctx, "publish vm event", "vm_id", vm.ID.String(), "error", err.Error())
	}
}

// ---------------------------------------------------------------- hosts

// HostView builds the scheduler's view of a host from its row and usage.
func (s *Service) HostView(ctx context.Context, q *sqlcgen.Queries, h sqlcgen.Host, image *vmtypes.ImageRef) (scheduler.Host, error) {
	usage, err := q.HostResourceUsage(ctx, sqlcgen.HostResourceUsageParams{HostID: h.ID, ResourceStates: vms.ResourceStates})
	if err != nil {
		return scheduler.Host{}, fmt.Errorf("host usage: %w", err)
	}
	committed, err := q.HostDiskCommittedMB(ctx, h.ID)
	if err != nil {
		return scheduler.Host{}, fmt.Errorf("host disk usage: %w", err)
	}
	view := scheduler.Host{
		ID:              h.ID,
		AdminState:      h.AdminState,
		Online:          scheduler.IsOnline(h.LastHeartbeatAt, time.Duration(s.Config.HostOfflineAfterSeconds)*time.Second, s.now()),
		TotalRAMMB:      int(h.TotalRamMb),
		TotalVCPUs:      int(h.TotalVcpus),
		TotalDiskGB:     int(h.TotalDiskGb),
		ReservedRAMMB:   int(h.ReservedRamMb),
		CPUOvercommit:   h.CpuOvercommit,
		UsedRAMMB:       int(usage.RamMb),
		UsedVCPUs:       int(usage.Vcpus),
		CommittedDiskMB: committed,
	}
	if image != nil {
		var present []vmtypes.ImageRef
		_ = json.Unmarshal(h.Images, &present)
		for _, img := range present {
			if img == *image {
				view.HasImage = true
			}
		}
	}
	return view, nil
}

// Agent returns a client for the host a VM lives on.
func (s *Service) Agent(ctx context.Context, hostID uuid.UUID) (*agentclient.Client, sqlcgen.Host, error) {
	host, err := s.Q.GetHost(ctx, hostID)
	if err != nil {
		return nil, sqlcgen.Host{}, fmt.Errorf("load host %s: %w", hostID, err)
	}
	token, err := s.Secrets.Open(host.AgentTokenEnc)
	if err != nil {
		return nil, sqlcgen.Host{}, fmt.Errorf("host %s: %w", host.Name, err)
	}
	return agentclient.New(host.AgentUrl, string(token)), host, nil
}

// ---------------------------------------------------------------- usage

// applyUsage keeps usage_intervals in step with a state change: an interval
// opens when a VM enters running, and closes when it leaves running for
// anything other than a reboot.
func applyUsage(ctx context.Context, q *sqlcgen.Queries, vm sqlcgen.Vm, from, to vms.State) error {
	if from == vms.StateRunning && to != vms.StateRunning && to != vms.StateRebooting {
		if err := q.CloseUsageInterval(ctx, vm.ID); err != nil {
			return fmt.Errorf("close usage interval: %w", err)
		}
	}
	if to == vms.StateRunning && from != vms.StateRebooting {
		err := q.OpenUsageInterval(ctx, sqlcgen.OpenUsageIntervalParams{
			VmID: vm.ID, UserID: vm.UserID, HostID: vm.HostID, RamMb: vm.RamMb, Vcpus: vm.Vcpus,
		})
		if err != nil {
			return fmt.Errorf("open usage interval: %w", err)
		}
	}
	// A reboot that fails leaves "rebooting" without passing through
	// "running": close the interval that was kept open for it.
	if from == vms.StateRebooting && to != vms.StateRunning {
		if err := q.CloseUsageInterval(ctx, vm.ID); err != nil {
			return fmt.Errorf("close usage interval: %w", err)
		}
	}
	return nil
}

// Event is one audit row.
type Event struct {
	Type    string // created, started, resumed, suspended, stopped, error, ...
	Actor   string // user | admin | system
	Message string
}

func insertEvent(ctx context.Context, q *sqlcgen.Queries, vmID uuid.UUID, ev *Event) error {
	if ev == nil {
		return nil
	}
	err := q.InsertVMEvent(ctx, sqlcgen.InsertVMEventParams{VmID: vmID, Type: ev.Type, Actor: ev.Actor, Message: ev.Message})
	if err != nil {
		return fmt.Errorf("insert vm event: %w", err)
	}
	return nil
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

// ---------------------------------------------------------------- Begin

// BeginOpts adjusts Begin.
type BeginOpts struct {
	// Detail is shown while the job runs, for example "Taking a snapshot".
	Detail string
	// Event is written together with the state change, if set.
	Event *Event
	// Check runs inside the transaction after the VM row is locked and the
	// action was found allowed. Returning an error aborts the change.
	Check func(ctx context.Context, q *sqlcgen.Queries, vm sqlcgen.Vm) error
}

// Begin moves a VM into the transitional state of the given action.
//
// The whole decision happens in one transaction: the host row is locked when
// capacity matters, then the VM row is locked, the state machine is asked,
// and only then is the state changed. Two simultaneous requests therefore
// can never both win, and two starts can never both take the last free RAM.
// It returns the VM as it was before the change along with the changed VM.
func (s *Service) Begin(ctx context.Context, vmID uuid.UUID, action vms.Action, opts BeginOpts) (before, after sqlcgen.Vm, err error) {
	target := vms.Transitional(action)
	if target == "" {
		return before, after, fmt.Errorf("action %q does not start a job", action)
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return before, after, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.Q.WithTx(tx)

	// Starting needs RAM and CPU on the VM's host. Lock order is always
	// host first, then VM.
	if action == vms.ActionStart {
		peek, err := q.GetVM(ctx, vmID)
		if errors.Is(err, pgx.ErrNoRows) {
			return before, after, ErrNotFound
		}
		if err != nil {
			return before, after, fmt.Errorf("load vm: %w", err)
		}
		host, err := q.LockHost(ctx, peek.HostID)
		if err != nil {
			return before, after, fmt.Errorf("lock host: %w", err)
		}
		view, err := s.HostView(ctx, q, host, nil)
		if err != nil {
			return before, after, err
		}
		if !view.Online || host.AdminState == "disabled" {
			return before, after, ErrHostUnavailable
		}
		if !scheduler.CanStart(view, int(peek.RamMb), int(peek.Vcpus)) {
			return before, after, ErrNoCapacity
		}
	}

	before, err = q.GetVMForUpdate(ctx, vmID)
	if errors.Is(err, pgx.ErrNoRows) {
		return before, after, ErrNotFound
	}
	if err != nil {
		return before, after, fmt.Errorf("lock vm: %w", err)
	}
	if !vms.Can(action, vms.State(before.State), before.HasSavedState) {
		return before, after, ErrInvalidState
	}
	if opts.Check != nil {
		if err := opts.Check(ctx, q, before); err != nil {
			return before, after, err
		}
	}

	params := sqlcgen.TransitionVMParams{
		ID:          vmID,
		NewState:    string(target),
		FromStates:  vms.SourceStates(action),
		StateDetail: text(opts.Detail),
	}
	if want := vms.SavedStateRequirement(action); want != nil {
		params.HasSavedState = pgtype.Bool{Bool: *want, Valid: true}
	}
	after, err = q.TransitionVM(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return before, after, ErrInvalidState
	}
	if err != nil {
		return before, after, fmt.Errorf("transition vm: %w", err)
	}
	if err := applyUsage(ctx, q, after, vms.State(before.State), target); err != nil {
		return before, after, err
	}
	if err := insertEvent(ctx, q, vmID, opts.Event); err != nil {
		return before, after, err
	}
	if err := tx.Commit(ctx); err != nil {
		return before, after, fmt.Errorf("commit: %w", err)
	}
	s.Publish(ctx, after)
	return before, after, nil
}

// ---------------------------------------------------------------- Settle

// SettleOpts describes how a job leaves a transitional state.
type SettleOpts struct {
	Detail        string
	HasSavedState *bool
	// Active marks the VM as active now (sets last_active_at).
	Active bool
	Event  *Event
	// Extra runs in the same transaction, for changes that must succeed or
	// fail together with the state change.
	Extra func(ctx context.Context, q *sqlcgen.Queries) error
}

// Settle moves a VM from the transitional state a job expects to its next
// state, together with the event row and the usage interval change. It
// reports false (and changes nothing) when the VM is no longer in the
// expected state, which makes every job safe to run twice.
func (s *Service) Settle(ctx context.Context, vmID uuid.UUID, expected, next vms.State, opts SettleOpts) (sqlcgen.Vm, bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return sqlcgen.Vm{}, false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.Q.WithTx(tx)

	params := sqlcgen.SettleVMParams{
		ID:            vmID,
		ExpectedState: string(expected),
		NewState:      string(next),
		StateDetail:   text(opts.Detail),
	}
	if opts.HasSavedState != nil {
		params.HasSavedState = pgtype.Bool{Bool: *opts.HasSavedState, Valid: true}
	}
	if opts.Active {
		now := s.now()
		params.LastActiveAt = &now
	}
	vm, err := q.SettleVM(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.Vm{}, false, nil
	}
	if err != nil {
		return sqlcgen.Vm{}, false, fmt.Errorf("settle vm: %w", err)
	}
	if err := applyUsage(ctx, q, vm, expected, next); err != nil {
		return sqlcgen.Vm{}, false, err
	}
	if err := insertEvent(ctx, q, vmID, opts.Event); err != nil {
		return sqlcgen.Vm{}, false, err
	}
	if opts.Extra != nil {
		if err := opts.Extra(ctx, q); err != nil {
			return sqlcgen.Vm{}, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return sqlcgen.Vm{}, false, fmt.Errorf("commit: %w", err)
	}
	s.Publish(ctx, vm)
	return vm, true, nil
}

// Bool is a small helper for SettleOpts.HasSavedState.
func Bool(v bool) *bool { return &v }
