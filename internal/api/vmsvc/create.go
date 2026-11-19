package vmsvc

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"zynthera/internal/api/scheduler"
	"zynthera/internal/api/store/sqlcgen"
	"zynthera/internal/shared/vmtypes"
)

// LimitError says which per-user limit a request would exceed.
type LimitError struct {
	Limit string // vms | ram_mb | vcpus | disk_gb | snapshots
}

func (e *LimitError) Error() string { return "limit exceeded: " + e.Limit }

// LimitsFor returns a user's limits: the override on the user row, or the
// default from the configuration.
func (s *Service) LimitsFor(u sqlcgen.User) scheduler.Limits {
	pick := func(override pgtype.Int4, def int) int {
		if override.Valid {
			return int(override.Int32)
		}
		return def
	}
	return scheduler.Limits{
		MaxVMs:    pick(u.MaxVms, s.Config.DefaultMaxVMs),
		MaxRAMMB:  pick(u.MaxRamMb, s.Config.DefaultMaxRAMMB),
		MaxVCPUs:  pick(u.MaxVcpus, s.Config.DefaultMaxVCPUs),
		MaxDiskGB: pick(u.MaxDiskGb, s.Config.DefaultMaxDiskGB),
	}
}

// TotalsFor returns what a user's VMs currently add up to.
func (s *Service) TotalsFor(ctx context.Context, q *sqlcgen.Queries, userID uuid.UUID) (scheduler.Totals, error) {
	row, err := q.UserVMTotals(ctx, userID)
	if err != nil {
		return scheduler.Totals{}, fmt.Errorf("user totals: %w", err)
	}
	return scheduler.Totals{VMs: int(row.VmCount), RAMMB: int(row.RamMb), VCPUs: int(row.Vcpus), DiskGB: int(row.DiskGb)}, nil
}

// CreateParams is an already validated request for a new VM.
type CreateParams struct {
	User          sqlcgen.User
	Image         sqlcgen.OsImage
	Name          string
	RAMMB         int
	VCPUs         int
	DiskGB        int
	LinuxUsername string
	// PasswordHash is the SHA-512 crypt hash; the plain password never gets here.
	PasswordHash string
}

// Create checks the user's limits, picks a host and inserts the VM in state
// "provisioning". The caller then queues the provision job.
//
// The user row is locked while the limits are checked and the chosen host
// row while its disk is checked, so neither can be oversubscribed by
// simultaneous requests.
func (s *Service) Create(ctx context.Context, p CreateParams) (sqlcgen.Vm, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return sqlcgen.Vm{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.Q.WithTx(tx)

	user, err := q.LockUser(ctx, p.User.ID)
	if err != nil {
		return sqlcgen.Vm{}, fmt.Errorf("lock user: %w", err)
	}
	totals, err := s.TotalsFor(ctx, q, user.ID)
	if err != nil {
		return sqlcgen.Vm{}, err
	}
	add := scheduler.Totals{VMs: 1, RAMMB: p.RAMMB, VCPUs: p.VCPUs, DiskGB: p.DiskGB}
	if which := scheduler.OverLimit(s.LimitsFor(user), totals, add); which != "" {
		return sqlcgen.Vm{}, &LimitError{Limit: which}
	}

	image := vmtypes.ImageRef{Slug: p.Image.Slug, Version: p.Image.CurrentVersion}
	hosts, err := q.ListHosts(ctx)
	if err != nil {
		return sqlcgen.Vm{}, fmt.Errorf("list hosts: %w", err)
	}
	views := make([]scheduler.Host, 0, len(hosts))
	for _, h := range hosts {
		view, err := s.HostView(ctx, q, h, &image)
		if err != nil {
			return sqlcgen.Vm{}, err
		}
		views = append(views, view)
	}

	headroom := s.Config.DiskHeadroomPercent
	var chosen *scheduler.Host
	for _, candidate := range scheduler.Candidates(views, p.DiskGB, p.RAMMB, headroom) {
		// Look again under the lock: another request may have taken the
		// space since the first look.
		locked, err := q.LockHost(ctx, candidate.ID)
		if err != nil {
			return sqlcgen.Vm{}, fmt.Errorf("lock host: %w", err)
		}
		fresh, err := s.HostView(ctx, q, locked, &image)
		if err != nil {
			return sqlcgen.Vm{}, err
		}
		if scheduler.CanPlace(fresh, p.DiskGB, p.RAMMB, headroom) {
			chosen = &fresh
			break
		}
	}
	if chosen == nil {
		return sqlcgen.Vm{}, ErrNoCapacity
	}

	vm, err := q.InsertVM(ctx, sqlcgen.InsertVMParams{
		UserID:              user.ID,
		HostID:              chosen.ID,
		OsImageID:           p.Image.ID,
		ImageVersion:        p.Image.CurrentVersion,
		Name:                p.Name,
		RamMb:               int32(p.RAMMB),  //nolint:gosec // validated against the allowed sizes
		Vcpus:               int32(p.VCPUs),  //nolint:gosec // validated against the allowed sizes
		DiskGb:              int32(p.DiskGB), //nolint:gosec // validated against the allowed sizes
		LinuxUsername:       p.LinuxUsername,
		PendingPasswordHash: text(p.PasswordHash),
	})
	if err != nil {
		return sqlcgen.Vm{}, fmt.Errorf("insert vm: %w", err)
	}
	if err := insertEvent(ctx, q, vm.ID, &Event{Type: "created", Actor: "user", Message: "PC created"}); err != nil {
		return sqlcgen.Vm{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return sqlcgen.Vm{}, fmt.Errorf("commit: %w", err)
	}
	s.Publish(ctx, vm)
	return vm, nil
}

// CheckResize verifies, inside Begin's transaction, that a stopped VM may
// change to the new size: within the user's limits and the host's disk.
func (s *Service) CheckResize(ramMB, vcpus, diskGB int) func(ctx context.Context, q *sqlcgen.Queries, vm sqlcgen.Vm) error {
	return func(ctx context.Context, q *sqlcgen.Queries, vm sqlcgen.Vm) error {
		user, err := q.GetUserByID(ctx, vm.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load user: %w", err)
		}
		totals, err := s.TotalsFor(ctx, q, user.ID)
		if err != nil {
			return err
		}
		add := scheduler.Totals{
			RAMMB:  ramMB - int(vm.RamMb),
			VCPUs:  vcpus - int(vm.Vcpus),
			DiskGB: diskGB - int(vm.DiskGb),
		}
		if which := scheduler.OverLimit(s.LimitsFor(user), totals, add); which != "" {
			return &LimitError{Limit: which}
		}
		host, err := q.GetHost(ctx, vm.HostID)
		if err != nil {
			return fmt.Errorf("load host: %w", err)
		}
		view, err := s.HostView(ctx, q, host, nil)
		if err != nil {
			return err
		}
		if !scheduler.CanGrow(view, add.DiskGB, add.RAMMB, s.Config.DiskHeadroomPercent) {
			return ErrNoCapacity
		}
		return nil
	}
}
