package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"zynthera/internal/api/secrets"
	"zynthera/internal/api/store/sqlcgen"
	"zynthera/internal/shared/httpx"
	"zynthera/internal/shared/vmtypes"
)

// hostJSON is a VM host as shown in the admin area. It never contains the
// host's secrets.
type hostJSON struct {
	ID              uuid.UUID          `json:"id"`
	Name            string             `json:"name"`
	AgentURL        string             `json:"agent_url"`
	PublicURL       string             `json:"public_url"`
	AdminState      string             `json:"admin_state"`
	Online          bool               `json:"online"`
	LastHeartbeatAt *time.Time         `json:"last_heartbeat_at"`
	AgentVersion    *string            `json:"agent_version"`
	TotalRAMMB      int32              `json:"total_ram_mb"`
	TotalVCPUs      int32              `json:"total_vcpus"`
	TotalDiskGB     int32              `json:"total_disk_gb"`
	FreeDiskGB      int32              `json:"free_disk_gb"`
	ReservedRAMMB   int32              `json:"reserved_ram_mb"`
	CPUOvercommit   float64            `json:"cpu_overcommit"`
	Images          []vmtypes.ImageRef `json:"images"`
	CreatedAt       time.Time          `json:"created_at"`
}

// hostOnline is the computed "online" state: a heartbeat arrived recently.
func hostOnline(h sqlcgen.Host, offlineAfter time.Duration, now time.Time) bool {
	return h.LastHeartbeatAt != nil && now.Sub(*h.LastHeartbeatAt) < offlineAfter
}

func (s *server) toHostJSON(h sqlcgen.Host) hostJSON {
	out := hostJSON{
		ID:            h.ID,
		Name:          h.Name,
		AgentURL:      h.AgentUrl,
		PublicURL:     h.PublicUrl,
		AdminState:    h.AdminState,
		Online:        hostOnline(h, time.Duration(s.Config.HostOfflineAfterSeconds)*time.Second, time.Now()),
		TotalRAMMB:    h.TotalRamMb,
		TotalVCPUs:    h.TotalVcpus,
		TotalDiskGB:   h.TotalDiskGb,
		FreeDiskGB:    h.FreeDiskGb,
		ReservedRAMMB: h.ReservedRamMb,
		CPUOvercommit: h.CpuOvercommit,
		Images:        []vmtypes.ImageRef{},
		CreatedAt:     h.CreatedAt.UTC(),
	}
	if h.LastHeartbeatAt != nil {
		t := h.LastHeartbeatAt.UTC()
		out.LastHeartbeatAt = &t
	}
	if h.AgentVersion.Valid {
		out.AgentVersion = &h.AgentVersion.String
	}
	// A damaged images column must not break the admin page.
	_ = json.Unmarshal(h.Images, &out.Images)
	return out
}

// requireAdmin lets only administrators through. It runs after requireUser.
func requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, ok := currentUser(r.Context()); !ok || user.Role != "admin" {
			httpx.WriteError(w, http.StatusForbidden, "forbidden", "This area is for administrators.", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// GET /api/admin/hosts
func (s *server) handleAdminListHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.Q.ListHosts(r.Context())
	if err != nil {
		s.internal(w, r, "list hosts", err)
		return
	}
	out := make([]hostJSON, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, s.toHostJSON(h))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"hosts": out})
}

var hostNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// cleanBaseURL accepts an http(s) URL with a host and no query, and returns
// it without a trailing slash.
func cleanBaseURL(raw string, schemes ...string) (string, bool) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || len(raw) > 300 {
		return "", false
	}
	for _, scheme := range schemes {
		if u.Scheme == scheme {
			return strings.TrimRight(raw, "/"), true
		}
	}
	return "", false
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// POST /api/admin/hosts
//
// The agent token and the stream secret are generated here and returned
// exactly once. Only their encrypted form is stored.
func (s *server) handleAdminCreateHost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		AgentURL  string `json:"agent_url"`
		PublicURL string `json:"public_url"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	fields := fieldErrors{}
	name := strings.TrimSpace(req.Name)
	if !hostNameRe.MatchString(name) {
		fields["name"] = "Use lower-case letters, digits and dashes, for example host-1."
	}
	agentURL, ok := cleanBaseURL(req.AgentURL, "http", "https")
	if !ok {
		fields["agent_url"] = "Enter the address the control plane uses to reach the agent, for example https://host-1.example.com."
	}
	publicURL, ok := cleanBaseURL(req.PublicURL, "http", "https")
	if !ok {
		fields["public_url"] = "Enter the address browsers use to reach this host, for example https://host-1.example.com."
	}
	if len(fields) > 0 {
		writeValidation(w, fields)
		return
	}

	agentToken, err := secrets.NewSecret()
	if err != nil {
		s.internal(w, r, "generate agent token", err)
		return
	}
	streamSecret, err := secrets.NewSecret()
	if err != nil {
		s.internal(w, r, "generate stream secret", err)
		return
	}
	tokenEnc, err := s.Secrets.Seal([]byte(agentToken))
	if err != nil {
		s.internal(w, r, "seal agent token", err)
		return
	}
	secretEnc, err := s.Secrets.Seal([]byte(streamSecret))
	if err != nil {
		s.internal(w, r, "seal stream secret", err)
		return
	}

	host, err := s.Q.CreateHost(r.Context(), sqlcgen.CreateHostParams{
		Name:            name,
		AgentUrl:        agentURL,
		PublicUrl:       publicURL,
		AgentTokenEnc:   tokenEnc,
		StreamSecretEnc: secretEnc,
	})
	if isUniqueViolation(err) {
		writeValidation(w, fieldErrors{"name": "A host with this name already exists."})
		return
	}
	if err != nil {
		s.internal(w, r, "create host", err)
		return
	}
	admin, _ := currentUser(r.Context())
	s.Logger.InfoContext(r.Context(), "admin created host", "admin_id", admin.ID.String(), "host_id", host.ID.String(), "name", host.Name)

	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"host":          s.toHostJSON(host),
		"agent_token":   agentToken,
		"stream_secret": streamSecret,
	})
}

// PATCH /api/admin/hosts/:id
func (s *server) handleAdminPatchHost(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "No such host.", nil)
		return
	}
	var req struct {
		AdminState    *string  `json:"admin_state"`
		ReservedRAMMB *int     `json:"reserved_ram_mb"`
		CPUOvercommit *float64 `json:"cpu_overcommit"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	fields := fieldErrors{}
	params := sqlcgen.UpdateHostAdminParams{ID: id}
	if req.AdminState != nil {
		switch *req.AdminState {
		case "active", "draining", "disabled":
			params.AdminState = pgtype.Text{String: *req.AdminState, Valid: true}
		default:
			fields["admin_state"] = "Must be active, draining or disabled."
		}
	}
	if req.ReservedRAMMB != nil {
		if *req.ReservedRAMMB < 0 || *req.ReservedRAMMB > 1024*1024 {
			fields["reserved_ram_mb"] = "Must be between 0 and 1048576."
		} else {
			params.ReservedRamMb = pgtype.Int4{Int32: int32(*req.ReservedRAMMB), Valid: true} //nolint:gosec // range checked above
		}
	}
	if req.CPUOvercommit != nil {
		if *req.CPUOvercommit < 1 || *req.CPUOvercommit > 16 {
			fields["cpu_overcommit"] = "Must be between 1 and 16."
		} else {
			params.CpuOvercommit = pgtype.Float8{Float64: *req.CPUOvercommit, Valid: true}
		}
	}
	if len(fields) > 0 {
		writeValidation(w, fields)
		return
	}

	host, err := s.Q.UpdateHostAdmin(r.Context(), params)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "No such host.", nil)
		return
	}
	if err != nil {
		s.internal(w, r, "update host", err)
		return
	}
	admin, _ := currentUser(r.Context())
	s.Logger.InfoContext(r.Context(), "admin updated host", "admin_id", admin.ID.String(), "host_id", host.ID.String(), "admin_state", host.AdminState)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"host": s.toHostJSON(host)})
}

// authenticateAgent checks that the request carries the agent token of the
// host it names. All failures look the same, so host ids cannot be probed.
func (s *server) authenticateAgent(w http.ResponseWriter, r *http.Request, hostID string) (sqlcgen.Host, bool) {
	deny := func() (sqlcgen.Host, bool) {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated", "Unknown host or wrong agent token.", nil)
		return sqlcgen.Host{}, false
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	id, err := uuid.Parse(hostID)
	if !ok || token == "" || err != nil {
		return deny()
	}
	host, err := s.Q.GetHost(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return deny()
	}
	if err != nil {
		s.internal(w, r, "get host", err)
		return sqlcgen.Host{}, false
	}
	want, err := s.Secrets.Open(host.AgentTokenEnc)
	if err != nil {
		s.internal(w, r, "open agent token", err)
		return sqlcgen.Host{}, false
	}
	// Hashing first makes the comparison constant-time for any lengths.
	gotSum, wantSum := sha256.Sum256([]byte(token)), sha256.Sum256(want)
	if subtle.ConstantTimeCompare(gotSum[:], wantSum[:]) != 1 {
		return deny()
	}
	return host, true
}

// POST /internal/agent/heartbeat
func (s *server) handleAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	var hb vmtypes.Heartbeat
	if !decodeJSON(w, r, &hb) {
		return
	}
	host, ok := s.authenticateAgent(w, r, hb.HostID)
	if !ok {
		return
	}
	c := hb.Capacity
	const maxInt = 1 << 30
	if c.TotalRAMMB < 0 || c.TotalVCPUs < 0 || c.TotalDiskGB < 0 || c.FreeDiskGB < 0 ||
		c.TotalRAMMB > maxInt || c.TotalVCPUs > maxInt || c.TotalDiskGB > maxInt || c.FreeDiskGB > maxInt ||
		len(hb.AgentVersion) > 40 || len(hb.Images) > 200 {
		httpx.WriteError(w, http.StatusUnprocessableEntity, "validation_failed", "The heartbeat has values out of range.", nil)
		return
	}
	images := make([]vmtypes.ImageRef, 0, len(hb.Images))
	for _, img := range hb.Images {
		if vmtypes.IsImageSlug(img.Slug) && vmtypes.IsImageVersion(img.Version) {
			images = append(images, img)
		}
	}
	imagesJSON, err := json.Marshal(images)
	if err != nil {
		s.internal(w, r, "encode images", err)
		return
	}

	err = s.Q.RecordHeartbeat(r.Context(), sqlcgen.RecordHeartbeatParams{
		ID:           host.ID,
		TotalRamMb:   int32(c.TotalRAMMB),  //nolint:gosec // range checked above
		TotalVcpus:   int32(c.TotalVCPUs),  //nolint:gosec // range checked above
		TotalDiskGb:  int32(c.TotalDiskGB), //nolint:gosec // range checked above
		FreeDiskGb:   int32(c.FreeDiskGB),  //nolint:gosec // range checked above
		Images:       imagesJSON,
		AgentVersion: pgtype.Text{String: hb.AgentVersion, Valid: hb.AgentVersion != ""},
	})
	if err != nil {
		s.internal(w, r, "record heartbeat", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
