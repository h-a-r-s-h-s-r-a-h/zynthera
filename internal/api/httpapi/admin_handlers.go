package httpapi

import (
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"zynthera/internal/api/jobs"
	"zynthera/internal/api/store/sqlcgen"
	"zynthera/internal/api/vms"
	"zynthera/internal/api/vmsvc"
	"zynthera/internal/shared/httpx"
)

const (
	adminPageSize    = 50
	adminMaxPageSize = 200
)

// ---------------------------------------------------------------- pagination

// encodeCursor packs the sort key of the last row of a page into an opaque
// string. The next page starts strictly after it.
func encodeCursor(createdAt time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeCursor(raw string) (*time.Time, uuid.NullUUID, bool) {
	if raw == "" {
		return nil, uuid.NullUUID{}, true
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, uuid.NullUUID{}, false
	}
	ts, idPart, ok := strings.Cut(string(b), "|")
	if !ok {
		return nil, uuid.NullUUID{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return nil, uuid.NullUUID{}, false
	}
	id, err := uuid.Parse(idPart)
	if err != nil {
		return nil, uuid.NullUUID{}, false
	}
	return &t, uuid.NullUUID{UUID: id, Valid: true}, true
}

func pageSize(r *http.Request) int32 {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n < 1 {
		return adminPageSize
	}
	if n > adminMaxPageSize {
		n = adminMaxPageSize
	}
	return int32(n) //nolint:gosec // bounded above
}

func badQuery(w http.ResponseWriter, message string) {
	httpx.WriteError(w, http.StatusUnprocessableEntity, "validation_failed", message, nil)
}

func optionalText(s string) pgtype.Text {
	s = strings.TrimSpace(s)
	return pgtype.Text{String: s, Valid: s != ""}
}

// ---------------------------------------------------------------- VMs

// adminVMJSON is a VM as the admin area sees it: the normal object plus who
// owns it and where it lives.
type adminVMJSON struct {
	vmsvc.VMJSON
	UserID    uuid.UUID `json:"user_id"`
	UserEmail string    `json:"user_email"`
	HostID    uuid.UUID `json:"host_id"`
	HostName  string    `json:"host_name"`
}

// GET /api/admin/vms?state=&host=&email=&cursor=&limit=
func (s *server) handleAdminListVMs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	params := sqlcgen.AdminListVMsParams{
		State:    optionalText(q.Get("state")),
		Email:    optionalText(q.Get("email")),
		PageSize: pageSize(r) + 1, // one extra row tells whether a next page exists
	}
	if params.State.Valid {
		known := false
		for _, st := range vms.AllStates {
			known = known || string(st) == params.State.String
		}
		if !known {
			badQuery(w, "Unknown state.")
			return
		}
	}
	if raw := q.Get("host"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			badQuery(w, "The host filter must be a host id.")
			return
		}
		params.HostID = uuid.NullUUID{UUID: id, Valid: true}
	}
	var ok bool
	if params.CursorCreatedAt, params.CursorID, ok = decodeCursor(q.Get("cursor")); !ok {
		badQuery(w, "The cursor is not valid.")
		return
	}

	rows, err := s.Q.AdminListVMs(r.Context(), params)
	if err != nil {
		s.internal(w, r, "admin list vms", err)
		return
	}
	next := ""
	if len(rows) == int(params.PageSize) {
		rows = rows[:len(rows)-1]
		last := rows[len(rows)-1].Vm
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	out := make([]adminVMJSON, 0, len(rows))
	for _, row := range rows {
		image := sqlcgen.OsImage{Slug: row.ImageSlug, DisplayName: row.ImageDisplayName}
		out = append(out, adminVMJSON{
			VMJSON: vmsvc.ToJSON(row.Vm, image),
			UserID: row.Vm.UserID, UserEmail: row.UserEmail,
			HostID: row.Vm.HostID, HostName: row.HostName,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"vms": out, "next_cursor": next})
}

// adminVMAction lets an administrator suspend or force stop any PC.
func (s *server) adminVMAction(action vms.Action, taskType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		admin, _ := currentUser(r.Context())
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "No such PC.", nil)
			return
		}
		_, after, err := s.Svc.Begin(r.Context(), id, action, vmsvc.BeginOpts{})
		if err != nil {
			s.writeSvcError(w, r, "admin begin "+string(action), err)
			return
		}
		// Every admin action on someone's PC is recorded with who did it.
		s.Logger.InfoContext(r.Context(), "admin vm action",
			"admin_id", admin.ID.String(), "vm_id", id.String(), "action", string(action))
		if !s.enqueue(w, r, taskType, jobs.Payload{VMID: id, Actor: "admin", Reason: "admin"}) {
			return
		}
		s.writeVM(w, r, http.StatusAccepted, after)
	}
}

// ---------------------------------------------------------------- users

type adminUserJSON struct {
	userJSON
	Disabled   bool   `json:"disabled"`
	MaxVMs     *int32 `json:"max_vms"`
	MaxRAMMB   *int32 `json:"max_ram_mb"`
	MaxVCPUs   *int32 `json:"max_vcpus"`
	MaxDiskGB  *int32 `json:"max_disk_gb"`
	VMCount    int32  `json:"vm_count"`
	UsedRAMMB  int32  `json:"used_ram_mb"`
	UsedDiskGB int32  `json:"used_disk_gb"`
}

func int4Ptr(v pgtype.Int4) *int32 {
	if !v.Valid {
		return nil
	}
	return &v.Int32
}

func toAdminUserJSON(u sqlcgen.User) adminUserJSON {
	return adminUserJSON{
		userJSON: toUserJSON(u), Disabled: u.DisabledAt != nil,
		MaxVMs: int4Ptr(u.MaxVms), MaxRAMMB: int4Ptr(u.MaxRamMb), MaxVCPUs: int4Ptr(u.MaxVcpus), MaxDiskGB: int4Ptr(u.MaxDiskGb),
	}
}

// GET /api/admin/users?email=&cursor=&limit=
func (s *server) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	params := sqlcgen.AdminListUsersParams{Email: optionalText(q.Get("email")), PageSize: pageSize(r) + 1}
	var ok bool
	if params.CursorCreatedAt, params.CursorID, ok = decodeCursor(q.Get("cursor")); !ok {
		badQuery(w, "The cursor is not valid.")
		return
	}
	rows, err := s.Q.AdminListUsers(r.Context(), params)
	if err != nil {
		s.internal(w, r, "admin list users", err)
		return
	}
	next := ""
	if len(rows) == int(params.PageSize) {
		rows = rows[:len(rows)-1]
		last := rows[len(rows)-1].User
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	out := make([]adminUserJSON, 0, len(rows))
	for _, row := range rows {
		u := toAdminUserJSON(row.User)
		u.VMCount, u.UsedRAMMB, u.UsedDiskGB = row.VmCount, row.UsedRamMb, row.UsedDiskGb
		out = append(out, u)
	}
	limits := s.Config
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"users": out, "next_cursor": next,
		"defaults": map[string]int{
			"max_vms": limits.DefaultMaxVMs, "max_ram_mb": limits.DefaultMaxRAMMB,
			"max_vcpus": limits.DefaultMaxVCPUs, "max_disk_gb": limits.DefaultMaxDiskGB,
		},
	})
}

// limitField reads one optional limit from a PATCH body: absent keeps the
// current value, null clears the override, a number sets it.
func limitField(body map[string]json.RawMessage, key string, current pgtype.Int4, maxValue int, fields fieldErrors) pgtype.Int4 {
	raw, present := body[key]
	if !present {
		return current
	}
	if string(raw) == "null" {
		return pgtype.Int4{}
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil || n < 0 || n > maxValue {
		fields[key] = fmt.Sprintf("Must be a number from 0 to %d, or empty for the default.", maxValue)
		return current
	}
	return pgtype.Int4{Int32: int32(n), Valid: true} //nolint:gosec // bounded above
}

// PATCH /api/admin/users/:id  { max_vms?, max_ram_mb?, max_vcpus?, max_disk_gb?, disabled? }
func (s *server) handleAdminPatchUser(w http.ResponseWriter, r *http.Request) {
	admin, _ := currentUser(r.Context())
	notFound := func() { httpx.WriteError(w, http.StatusNotFound, "not_found", "No such user.", nil) }
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		notFound()
		return
	}
	var body map[string]json.RawMessage
	if !decodeJSON(w, r, &body) {
		return
	}
	for key := range body {
		switch key {
		case "max_vms", "max_ram_mb", "max_vcpus", "max_disk_gb", "disabled":
		default:
			httpx.WriteError(w, http.StatusBadRequest, "validation_failed", "Unknown field: "+key, nil)
			return
		}
	}
	user, err := s.Q.GetUserByID(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound()
		return
	}
	if err != nil {
		s.internal(w, r, "load user", err)
		return
	}

	fields := fieldErrors{}
	params := sqlcgen.AdminSetUserLimitsParams{
		ID:         user.ID,
		MaxVms:     limitField(body, "max_vms", user.MaxVms, 1000, fields),
		MaxRamMb:   limitField(body, "max_ram_mb", user.MaxRamMb, 4*1024*1024, fields),
		MaxVcpus:   limitField(body, "max_vcpus", user.MaxVcpus, 4096, fields),
		MaxDiskGb:  limitField(body, "max_disk_gb", user.MaxDiskGb, 1024*1024, fields),
		DisabledAt: user.DisabledAt,
	}
	disabling := false
	if raw, present := body["disabled"]; present {
		var disabled bool
		if err := json.Unmarshal(raw, &disabled); err != nil {
			fields["disabled"] = "Must be true or false."
		} else if disabled && user.ID == admin.ID {
			fields["disabled"] = "You cannot disable your own account."
		} else if disabled && user.DisabledAt == nil {
			now := time.Now()
			params.DisabledAt, disabling = &now, true
		} else if !disabled {
			params.DisabledAt = nil
		}
	}
	if len(fields) > 0 {
		writeValidation(w, fields)
		return
	}

	updated, err := s.Q.AdminSetUserLimits(r.Context(), params)
	if err != nil {
		s.internal(w, r, "update user", err)
		return
	}
	s.Logger.InfoContext(r.Context(), "admin updated user",
		"admin_id", admin.ID.String(), "user_id", updated.ID.String(), "disabled", updated.DisabledAt != nil)

	if disabling {
		// A disabled account is logged out everywhere, and its running PCs
		// are suspended so they stop using the host. Nothing is deleted.
		if err := s.Q.DeleteUserSessions(r.Context(), updated.ID); err != nil {
			s.internal(w, r, "delete sessions", err)
			return
		}
		running, err := s.Q.ListRunningVMsForUser(r.Context(), updated.ID)
		if err != nil {
			s.internal(w, r, "list running vms", err)
			return
		}
		for _, vm := range running {
			if _, _, err := s.Svc.Begin(r.Context(), vm.ID, vms.ActionSuspend, vmsvc.BeginOpts{}); err != nil {
				continue
			}
			if err := s.Queue.Enqueue(r.Context(), jobs.TypeSuspend, jobs.Payload{VMID: vm.ID, Actor: "admin", Reason: "admin"}); err != nil {
				s.Logger.ErrorContext(r.Context(), "disable user: enqueue suspend", "vm_id", vm.ID.String(), "error", err.Error())
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"user": toAdminUserJSON(updated)})
}

// ---------------------------------------------------------------- usage

// parseDay accepts a date (2026-11-19, meaning the start of that day in UTC)
// or a full RFC 3339 timestamp.
func parseDay(raw string) (time.Time, bool) {
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

type usageRowJSON struct {
	UserID    uuid.UUID `json:"user_id"`
	UserEmail string    `json:"user_email"`
	RAMMB     int32     `json:"ram_mb"`
	VCPUs     int32     `json:"vcpus"`
	Hours     float64   `json:"hours"`
}

// csvSafe keeps a spreadsheet from treating a cell as a formula.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// GET /api/admin/usage?from=&to=[&format=csv]
//
// Running hours per user and per size. "to" is exclusive. Without
// parameters it covers the last 30 days.
func (s *server) handleAdminUsage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	until := time.Now().UTC()
	since := until.AddDate(0, 0, -30)
	if raw := q.Get("from"); raw != "" {
		t, ok := parseDay(raw)
		if !ok {
			badQuery(w, "from must be a date like 2026-11-01.")
			return
		}
		since = t
	}
	if raw := q.Get("to"); raw != "" {
		t, ok := parseDay(raw)
		if !ok {
			badQuery(w, "to must be a date like 2026-12-01.")
			return
		}
		until = t
	}
	if !until.After(since) || until.Sub(since) > 366*24*time.Hour {
		badQuery(w, "The range must be between one moment and 366 days long.")
		return
	}

	rows, err := s.Q.AdminUsage(r.Context(), sqlcgen.AdminUsageParams{Since: since, Until: until})
	if err != nil {
		s.internal(w, r, "admin usage", err)
		return
	}
	out := make([]usageRowJSON, 0, len(rows))
	total := 0.0
	for _, row := range rows {
		hours := float64(int64(row.Hours*100+0.5)) / 100 // two decimals
		out = append(out, usageRowJSON{UserID: row.UserID, UserEmail: row.UserEmail, RAMMB: row.RamMb, VCPUs: row.Vcpus, Hours: hours})
		total += hours
	}

	if q.Get("format") == "csv" {
		name := fmt.Sprintf("zynthera-usage-%s-to-%s.csv", since.Format("2006-01-02"), until.Format("2006-01-02"))
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"user_email", "ram_mb", "vcpus", "hours"})
		for _, row := range out {
			_ = cw.Write([]string{
				csvSafe(row.UserEmail), strconv.Itoa(int(row.RAMMB)), strconv.Itoa(int(row.VCPUs)),
				strconv.FormatFloat(row.Hours, 'f', 2, 64),
			})
		}
		cw.Flush()
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"from": since, "to": until, "rows": out, "total_hours": float64(int64(total*100+0.5)) / 100,
	})
}
