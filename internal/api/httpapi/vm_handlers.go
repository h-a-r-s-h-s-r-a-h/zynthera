package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"zynthera/internal/api/auth"
	"zynthera/internal/api/jobs"
	"zynthera/internal/api/linuxpw"
	"zynthera/internal/api/scheduler"
	"zynthera/internal/api/store/sqlcgen"
	"zynthera/internal/api/vms"
	"zynthera/internal/api/vmsvc"
	"zynthera/internal/shared/httpx"
	"zynthera/internal/shared/vmtypes"
)

// reservedUsernames may not be used as the Linux account of a PC: system
// accounts present in the images, and names that would confuse people.
var reservedUsernames = map[string]bool{
	"root": true, "admin": true, "daemon": true, "sys": true, "ubuntu": true, "debian": true,
	"fedora": true, "lightdm": true, "nobody": true, "zynthera": true, "bin": true, "sync": true,
	"games": true, "man": true, "mail": true, "news": true, "uucp": true, "proxy": true,
	"backup": true, "list": true, "irc": true, "systemd": true, "messagebus": true, "syslog": true,
	"sshd": true, "polkitd": true, "rtkit": true, "colord": true, "avahi": true, "saned": true,
	"pulse": true, "geoclue": true, "tss": true, "uuidd": true, "tcpdump": true, "landscape": true,
	"usbmux": true, "dnsmasq": true, "kernoops": true, "whoopsie": true, "sudo": true, "adm": true,
	"www-data": true, "chrony": true, "_chrony": true, "_apt": true, "cloud-init": true, "user": true,
}

// cleanLabel trims a user-supplied label and checks its length. Labels are
// only ever shown as text, never used in a path or a command.
func cleanLabel(raw string, maxRunes int) (string, bool) {
	label := strings.TrimSpace(raw)
	n := utf8.RuneCountInString(label)
	if n < 1 || n > maxRunes {
		return "", false
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return label, true
}

func limitMessage(which string) string {
	switch which {
	case "vms":
		return "You have reached the number of PCs your account allows."
	case "ram_mb":
		return "This is more memory than your account allows in total."
	case "vcpus":
		return "This is more CPU than your account allows in total."
	case "disk_gb":
		return "This is more disk space than your account allows in total."
	case "snapshots":
		return "This PC has reached its snapshot limit. Delete a snapshot first."
	}
	return "This is more than your account allows."
}

// writeSvcError maps a vmsvc error to the API's error codes.
func (s *server) writeSvcError(w http.ResponseWriter, r *http.Request, what string, err error) {
	var limit *vmsvc.LimitError
	switch {
	case errors.Is(err, vmsvc.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found", "No such PC.", nil)
	case errors.Is(err, vmsvc.ErrInvalidState):
		httpx.WriteError(w, http.StatusConflict, "vm_invalid_state", "This is not possible in the PC's current state.", nil)
	case errors.Is(err, vmsvc.ErrNoCapacity):
		httpx.WriteError(w, http.StatusServiceUnavailable, "no_capacity",
			"Our servers are full right now, so this PC cannot be given the resources it needs. Please try again a little later.", nil)
	case errors.Is(err, vmsvc.ErrHostUnavailable):
		httpx.WriteError(w, http.StatusServiceUnavailable, "host_unavailable",
			"The server this PC lives on is not reachable right now. Please try again in a few minutes.", nil)
	case errors.As(err, &limit):
		httpx.WriteError(w, http.StatusUnprocessableEntity, "limit_exceeded", limitMessage(limit.Limit), map[string]any{"limit": limit.Limit})
	default:
		s.internal(w, r, what, err)
	}
}

// ownVM loads a VM that belongs to the logged-in user. Anyone else's VM, a
// deleted VM and a malformed id all give the same 404, so ids cannot be probed.
func (s *server) ownVM(w http.ResponseWriter, r *http.Request) (sqlcgen.Vm, bool) {
	user, _ := currentUser(r.Context())
	notFound := func() (sqlcgen.Vm, bool) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "No such PC.", nil)
		return sqlcgen.Vm{}, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return notFound()
	}
	vm, err := s.Q.GetVMForUser(r.Context(), sqlcgen.GetVMForUserParams{ID: id, UserID: user.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound()
	}
	if err != nil {
		s.internal(w, r, "load vm", err)
		return sqlcgen.Vm{}, false
	}
	return vm, true
}

func (s *server) writeVM(w http.ResponseWriter, r *http.Request, status int, vm sqlcgen.Vm) {
	obj, err := s.Svc.JSON(r.Context(), vm)
	if err != nil {
		s.internal(w, r, "vm json", err)
		return
	}
	httpx.WriteJSON(w, status, map[string]any{"vm": obj})
}

// enqueue queues the job of an action that has already begun. If queuing
// fails the VM stays in its transitional state until the stuck-VM check
// settles it; the caller is told something went wrong.
func (s *server) enqueue(w http.ResponseWriter, r *http.Request, taskType string, p jobs.Payload) bool {
	if err := s.Queue.Enqueue(r.Context(), taskType, p); err != nil {
		s.internal(w, r, "enqueue "+taskType, err)
		return false
	}
	return true
}

// GET /api/vms
func (s *server) handleListVMs(w http.ResponseWriter, r *http.Request) {
	list, err := s.userVMs(r.Context())
	if err != nil {
		s.internal(w, r, "list vms", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"vms": list})
}

func (s *server) userVMs(ctx context.Context) ([]vmsvc.VMJSON, error) {
	user, _ := currentUser(ctx)
	rows, err := s.Q.ListVMsForUser(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	out := make([]vmsvc.VMJSON, 0, len(rows))
	for _, vm := range rows {
		obj, err := s.Svc.JSON(ctx, vm)
		if err != nil {
			return nil, err
		}
		out = append(out, obj)
	}
	return out, nil
}

// GET /api/vms/:id
func (s *server) handleGetVM(w http.ResponseWriter, r *http.Request) {
	if vm, ok := s.ownVM(w, r); ok {
		s.writeVM(w, r, http.StatusOK, vm)
	}
}

// POST /api/vms
func (s *server) handleCreateVM(w http.ResponseWriter, r *http.Request) {
	user, _ := currentUser(r.Context())
	var req struct {
		Name          string `json:"name"`
		Image         string `json:"image"`
		RAMMB         int    `json:"ram_mb"`
		VCPUs         int    `json:"vcpus"`
		DiskGB        int    `json:"disk_gb"`
		LinuxUsername string `json:"linux_username"`
		LinuxPassword string `json:"linux_password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if user.EmailVerifiedAt == nil {
		httpx.WriteError(w, http.StatusForbidden, "email_not_verified", "Confirm your email address before creating a PC.", nil)
		return
	}
	if !s.allow(w, r, auth.RuleVMActionsPerUser, user.ID.String()) {
		return
	}

	fields := fieldErrors{}
	name, ok := cleanLabel(req.Name, 40)
	if !ok {
		fields["name"] = "Give your PC a name of 1 to 40 characters."
	}
	var image sqlcgen.OsImage
	if !vmtypes.IsImageSlug(req.Image) {
		fields["image"] = "Choose an operating system."
	} else {
		img, err := s.Q.GetImageBySlug(r.Context(), req.Image)
		switch {
		case errors.Is(err, pgx.ErrNoRows) || (err == nil && !img.Enabled):
			fields["image"] = "This operating system is not available."
		case err != nil:
			s.internal(w, r, "load image", err)
			return
		default:
			image = img
		}
	}
	cfg := s.Config
	if !slices.Contains(cfg.AllowedRAMMB, req.RAMMB) {
		fields["ram_mb"] = "Choose one of the offered memory sizes."
	} else if image.Slug != "" && req.RAMMB < int(image.MinRamMb) {
		fields["ram_mb"] = fmt.Sprintf("%s needs at least %d MB of memory.", image.DisplayName, image.MinRamMb)
	}
	if !slices.Contains(cfg.AllowedVCPUs, req.VCPUs) {
		fields["vcpus"] = "Choose one of the offered CPU counts."
	}
	if !slices.Contains(cfg.AllowedDiskGB, req.DiskGB) {
		fields["disk_gb"] = "Choose one of the offered disk sizes."
	} else if image.Slug != "" && req.DiskGB < int(image.MinDiskGb) {
		fields["disk_gb"] = fmt.Sprintf("%s needs at least %d GB of disk.", image.DisplayName, image.MinDiskGb)
	}
	switch {
	case !vmtypes.IsLinuxUsername(req.LinuxUsername):
		fields["linux_username"] = "Use 3 to 31 lower-case letters, digits, dashes or underscores, starting with a letter."
	case reservedUsernames[req.LinuxUsername]:
		fields["linux_username"] = "This username is reserved by the system. Please choose another."
	}
	if n := utf8.RuneCountInString(req.LinuxPassword); n < 8 || n > 128 || len(req.LinuxPassword) > 512 {
		fields["linux_password"] = "Use 8 to 128 characters."
	}
	if len(fields) > 0 {
		writeValidation(w, fields)
		return
	}

	// The plain password ends here. Only the hash goes any further.
	hash, err := linuxpw.Hash(req.LinuxPassword)
	req.LinuxPassword = ""
	if err != nil {
		s.internal(w, r, "hash linux password", err)
		return
	}

	vm, err := s.Svc.Create(r.Context(), vmsvc.CreateParams{
		User: user, Image: image, Name: name,
		RAMMB: req.RAMMB, VCPUs: req.VCPUs, DiskGB: req.DiskGB,
		LinuxUsername: req.LinuxUsername, PasswordHash: hash,
	})
	if err != nil {
		s.writeSvcError(w, r, "create vm", err)
		return
	}
	s.Logger.InfoContext(r.Context(), "vm created", "vm_id", vm.ID.String(), "user_id", user.ID.String(), "host_id", vm.HostID.String())
	if !s.enqueue(w, r, jobs.TypeProvision, jobs.Payload{VMID: vm.ID, Actor: "user"}) {
		return
	}
	s.writeVM(w, r, http.StatusAccepted, vm)
}

// vmAction builds the handler of an action endpoint: check ownership, apply
// the rate limit, move the VM into the action's transitional state and queue
// its job. The state machine decides whether the action is allowed.
func (s *server) vmAction(action vms.Action, taskType, reason string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, _ := currentUser(r.Context())
		vm, ok := s.ownVM(w, r)
		if !ok {
			return
		}
		if !s.allow(w, r, auth.RuleVMActionsPerUser, user.ID.String()) {
			return
		}
		_, after, err := s.Svc.Begin(r.Context(), vm.ID, action, vmsvc.BeginOpts{})
		if err != nil {
			s.writeSvcError(w, r, "begin "+string(action), err)
			return
		}
		if !s.enqueue(w, r, taskType, jobs.Payload{VMID: vm.ID, Actor: "user", Reason: reason}) {
			return
		}
		s.writeVM(w, r, http.StatusAccepted, after)
	}
}

// PATCH /api/vms/:id  { name?, keep_alive_hours?, ram_mb?, vcpus?, disk_gb? }
func (s *server) handlePatchVM(w http.ResponseWriter, r *http.Request) {
	user, _ := currentUser(r.Context())
	vm, ok := s.ownVM(w, r)
	if !ok {
		return
	}
	var req struct {
		Name           *string `json:"name"`
		KeepAliveHours *int    `json:"keep_alive_hours"`
		RAMMB          *int    `json:"ram_mb"`
		VCPUs          *int    `json:"vcpus"`
		DiskGB         *int    `json:"disk_gb"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	cfg := s.Config
	fields := fieldErrors{}
	var name string
	if req.Name != nil {
		var ok bool
		if name, ok = cleanLabel(*req.Name, 40); !ok {
			fields["name"] = "Give your PC a name of 1 to 40 characters."
		}
	}
	if req.KeepAliveHours != nil && (*req.KeepAliveHours < 0 || *req.KeepAliveHours > cfg.MaxKeepAliveHours) {
		fields["keep_alive_hours"] = fmt.Sprintf("Choose between 0 and %d hours.", cfg.MaxKeepAliveHours)
	}
	resizing := req.RAMMB != nil || req.VCPUs != nil || req.DiskGB != nil
	ram, vcpus, disk := int(vm.RamMb), int(vm.Vcpus), int(vm.DiskGb)
	if resizing {
		if req.RAMMB != nil {
			ram = *req.RAMMB
		}
		if req.VCPUs != nil {
			vcpus = *req.VCPUs
		}
		if req.DiskGB != nil {
			disk = *req.DiskGB
		}
		image, err := s.Q.GetImage(r.Context(), vm.OsImageID)
		if err != nil {
			s.internal(w, r, "load image", err)
			return
		}
		if !slices.Contains(cfg.AllowedRAMMB, ram) || ram < int(image.MinRamMb) {
			fields["ram_mb"] = "Choose one of the offered memory sizes."
		}
		if !slices.Contains(cfg.AllowedVCPUs, vcpus) {
			fields["vcpus"] = "Choose one of the offered CPU counts."
		}
		switch {
		case disk < int(vm.DiskGb):
			fields["disk_gb"] = "A disk can only grow, not shrink."
		case !slices.Contains(cfg.AllowedDiskGB, disk):
			fields["disk_gb"] = "Choose one of the offered disk sizes."
		}
	}
	if len(fields) > 0 {
		writeValidation(w, fields)
		return
	}

	var err error
	if req.Name != nil {
		if vm, err = s.Q.UpdateVMName(r.Context(), sqlcgen.UpdateVMNameParams{ID: vm.ID, Name: name}); err != nil {
			s.internal(w, r, "rename vm", err)
			return
		}
	}
	if req.KeepAliveHours != nil {
		var until *time.Time
		if *req.KeepAliveHours > 0 {
			t := time.Now().Add(time.Duration(*req.KeepAliveHours) * time.Hour)
			until = &t
		}
		if vm, err = s.Q.SetVMKeepAlive(r.Context(), sqlcgen.SetVMKeepAliveParams{ID: vm.ID, KeepAliveUntil: until}); err != nil {
			s.internal(w, r, "set keep-alive", err)
			return
		}
	}
	status := http.StatusOK
	if resizing && (ram != int(vm.RamMb) || vcpus != int(vm.Vcpus) || disk != int(vm.DiskGb)) {
		if !s.allow(w, r, auth.RuleVMActionsPerUser, user.ID.String()) {
			return
		}
		_, after, err := s.Svc.Begin(r.Context(), vm.ID, vms.ActionResize, vmsvc.BeginOpts{
			Detail: "Changing the size",
			Check:  s.Svc.CheckResize(ram, vcpus, disk),
		})
		if err != nil {
			s.writeSvcError(w, r, "begin resize", err)
			return
		}
		if !s.enqueue(w, r, jobs.TypeResize, jobs.Payload{VMID: vm.ID, Actor: "user", RAMMB: ram, VCPUs: vcpus, DiskGB: disk}) {
			return
		}
		vm, status = after, http.StatusAccepted
	} else if req.Name != nil || req.KeepAliveHours != nil {
		s.Svc.Publish(r.Context(), vm)
	}
	s.writeVM(w, r, status, vm)
}

type eventJSON struct {
	Type      string    `json:"type"`
	Actor     string    `json:"actor"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// GET /api/vms/:id/events
func (s *server) handleVMEvents(w http.ResponseWriter, r *http.Request) {
	vm, ok := s.ownVM(w, r)
	if !ok {
		return
	}
	rows, err := s.Q.ListVMEvents(r.Context(), sqlcgen.ListVMEventsParams{VmID: vm.ID, Limit: 200})
	if err != nil {
		s.internal(w, r, "list events", err)
		return
	}
	out := make([]eventJSON, 0, len(rows))
	for _, e := range rows {
		out = append(out, eventJSON{Type: e.Type, Actor: e.Actor, Message: e.Message, CreatedAt: e.CreatedAt.UTC()})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"events": out})
}

// ---------------------------------------------------------------- snapshots

type snapshotJSON struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
}

func toSnapshotJSON(sn sqlcgen.Snapshot) snapshotJSON {
	return snapshotJSON{ID: sn.ID, Name: sn.Name, Status: sn.Status, SizeBytes: sn.SizeBytes, CreatedAt: sn.CreatedAt.UTC()}
}

// GET /api/vms/:id/snapshots
func (s *server) handleListSnapshots(w http.ResponseWriter, r *http.Request) {
	vm, ok := s.ownVM(w, r)
	if !ok {
		return
	}
	rows, err := s.Q.ListSnapshots(r.Context(), vm.ID)
	if err != nil {
		s.internal(w, r, "list snapshots", err)
		return
	}
	out := make([]snapshotJSON, 0, len(rows))
	for _, sn := range rows {
		out = append(out, toSnapshotJSON(sn))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"snapshots": out})
}

// POST /api/vms/:id/snapshots  { name }
func (s *server) handleCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	user, _ := currentUser(r.Context())
	vm, ok := s.ownVM(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	name, ok := cleanLabel(req.Name, 60)
	if !ok {
		writeValidation(w, fieldErrors{"name": "Give the snapshot a name of 1 to 60 characters."})
		return
	}
	if !s.allow(w, r, auth.RuleVMActionsPerUser, user.ID.String()) {
		return
	}

	var snap sqlcgen.Snapshot
	before, _, err := s.Svc.Begin(r.Context(), vm.ID, vms.ActionSnapshot, vmsvc.BeginOpts{
		Detail: "Taking a snapshot",
		// The limit check and the new row are part of the same transaction
		// as the state change, so the limit cannot be passed by two
		// requests at once.
		Check: func(ctx context.Context, q *sqlcgen.Queries, vm sqlcgen.Vm) error {
			n, err := q.CountSnapshots(ctx, vm.ID)
			if err != nil {
				return fmt.Errorf("count snapshots: %w", err)
			}
			if int(n) >= s.Config.MaxSnapshotsPerVM {
				return &vmsvc.LimitError{Limit: "snapshots"}
			}
			snap, err = q.InsertSnapshot(ctx, sqlcgen.InsertSnapshotParams{VmID: vm.ID, Name: name})
			if err != nil {
				return fmt.Errorf("insert snapshot: %w", err)
			}
			return nil
		},
	})
	if err != nil {
		s.writeSvcError(w, r, "begin snapshot", err)
		return
	}
	payload := jobs.Payload{VMID: vm.ID, Actor: "user", SnapshotID: snap.ID, ReturnTo: before.State}
	if !s.enqueue(w, r, jobs.TypeSnapshotCreate, payload) {
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"snapshot": toSnapshotJSON(snap)})
}

// snapshotAction handles restore and delete of one snapshot.
func (s *server) snapshotAction(action vms.Action, taskType, detail string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, _ := currentUser(r.Context())
		vm, ok := s.ownVM(w, r)
		if !ok {
			return
		}
		notFound := func() { httpx.WriteError(w, http.StatusNotFound, "not_found", "No such snapshot.", nil) }
		sid, err := uuid.Parse(chi.URLParam(r, "sid"))
		if err != nil {
			notFound()
			return
		}
		// The snapshot must belong to this PC.
		snap, err := s.Q.GetSnapshot(r.Context(), sqlcgen.GetSnapshotParams{ID: sid, VmID: vm.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			notFound()
			return
		}
		if err != nil {
			s.internal(w, r, "load snapshot", err)
			return
		}
		if action == vms.ActionRestoreSnapshot && snap.Status != "ready" {
			httpx.WriteError(w, http.StatusConflict, "vm_invalid_state", "This snapshot is not ready to be restored.", nil)
			return
		}
		if !s.allow(w, r, auth.RuleVMActionsPerUser, user.ID.String()) {
			return
		}
		before, after, err := s.Svc.Begin(r.Context(), vm.ID, action, vmsvc.BeginOpts{Detail: detail})
		if err != nil {
			s.writeSvcError(w, r, "begin "+string(action), err)
			return
		}
		payload := jobs.Payload{VMID: vm.ID, Actor: "user", SnapshotID: snap.ID, ReturnTo: before.State}
		if !s.enqueue(w, r, taskType, payload) {
			return
		}
		s.writeVM(w, r, http.StatusAccepted, after)
	}
}

// ---------------------------------------------------------------- images, options

type imageJSON struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	MinRAMMB    int32  `json:"min_ram_mb"`
	MinDiskGB   int32  `json:"min_disk_gb"`
}

// GET /api/images: enabled images that at least one online host has.
func (s *server) handleListImages(w http.ResponseWriter, r *http.Request) {
	images, err := s.Q.ListEnabledImages(r.Context())
	if err != nil {
		s.internal(w, r, "list images", err)
		return
	}
	hosts, err := s.Q.ListHosts(r.Context())
	if err != nil {
		s.internal(w, r, "list hosts", err)
		return
	}
	available := map[vmtypes.ImageRef]bool{}
	offlineAfter := time.Duration(s.Config.HostOfflineAfterSeconds) * time.Second
	for _, h := range hosts {
		if h.AdminState != "active" || !scheduler.IsOnline(h.LastHeartbeatAt, offlineAfter, time.Now()) {
			continue
		}
		var present []vmtypes.ImageRef
		_ = json.Unmarshal(h.Images, &present)
		for _, img := range present {
			available[img] = true
		}
	}
	out := []imageJSON{}
	for _, img := range images {
		if available[vmtypes.ImageRef{Slug: img.Slug, Version: img.CurrentVersion}] {
			out = append(out, imageJSON{
				Slug: img.Slug, DisplayName: img.DisplayName, Description: img.Description,
				MinRAMMB: img.MinRamMb, MinDiskGB: img.MinDiskGb,
			})
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"images": out})
}

// GET /api/options: what the wizard offers, the user's limits and usage.
func (s *server) handleOptions(w http.ResponseWriter, r *http.Request) {
	user, _ := currentUser(r.Context())
	totals, err := s.Svc.TotalsFor(r.Context(), s.Q, user.ID)
	if err != nil {
		s.internal(w, r, "user totals", err)
		return
	}
	limits := s.Svc.LimitsFor(user)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"allowed_ram_mb":       s.Config.AllowedRAMMB,
		"allowed_vcpus":        s.Config.AllowedVCPUs,
		"allowed_disk_gb":      s.Config.AllowedDiskGB,
		"max_keep_alive_hours": s.Config.MaxKeepAliveHours,
		"max_snapshots_per_vm": s.Config.MaxSnapshotsPerVM,
		"idle_input_minutes":   s.Config.IdleInputMinutes,
		"limits": map[string]int{
			"max_vms": limits.MaxVMs, "max_ram_mb": limits.MaxRAMMB, "max_vcpus": limits.MaxVCPUs, "max_disk_gb": limits.MaxDiskGB,
		},
		"usage": map[string]int{
			"vms": totals.VMs, "ram_mb": totals.RAMMB, "vcpus": totals.VCPUs, "disk_gb": totals.DiskGB,
		},
	})
}

// ---------------------------------------------------------------- live updates

// GET /api/events: Server-Sent Events with the live state of the user's VMs.
func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	user, _ := currentUser(r.Context())
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.internal(w, r, "events", errors.New("response writer cannot flush"))
		return
	}
	ctx := r.Context()

	// Subscribe before reading the current list, so a change that happens
	// between the two is not lost.
	updates, stop, err := s.Bus.Subscribe(ctx, user.ID)
	if err != nil {
		s.internal(w, r, "subscribe", err)
		return
	}
	defer stop()
	current, err := s.userVMs(ctx)
	if err != nil {
		s.internal(w, r, "list vms", err)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // tells proxies not to buffer the stream
	w.WriteHeader(http.StatusOK)

	send := func(event string, data []byte) bool {
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	for _, vm := range current {
		raw, err := json.Marshal(vm)
		if err != nil || !send("vm", raw) {
			return
		}
	}
	// Tells the client the initial list is complete.
	if !send("ready", []byte("{}")) {
		return
	}

	keepAlive := time.NewTicker(20 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case raw, open := <-updates:
			if !open || !send("vm", raw) {
				return
			}
		case <-keepAlive.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
