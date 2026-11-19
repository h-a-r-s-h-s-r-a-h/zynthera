// Package server is the HTTP API of the host agent.
package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"zynthera/internal/agent/agentconfig"
	"zynthera/internal/agent/driver"
	"zynthera/internal/shared/httpx"
	"zynthera/internal/shared/vmtypes"
)

// Version is reported in /health and in heartbeats.
const Version = "0.1.0"

// maxBodyBytes caps the JSON bodies of control-plane calls.
const maxBodyBytes = 64 << 10

// Deps is everything the agent's HTTP layer needs.
type Deps struct {
	Config agentconfig.Config
	Logger *slog.Logger
	Driver driver.Driver
}

type server struct {
	Deps
	tokenHash [32]byte
	locks     vmLocks
}

// vmLocks holds one lock per VM, so two operations on the same VM never
// overlap. A second request for a busy VM is refused instead of queued.
type vmLocks struct {
	mu   sync.Mutex
	busy map[string]bool
}

func (l *vmLocks) tryLock(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.busy[id] {
		return false
	}
	if l.busy == nil {
		l.busy = map[string]bool{}
	}
	l.busy[id] = true
	return true
}

func (l *vmLocks) unlock(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.busy, id)
}

// NewRouter builds the agent router. The stream and file routes for browsers
// are added in milestones 4 and 6.
func NewRouter(d Deps) http.Handler {
	s := &server{Deps: d, tokenHash: sha256.Sum256([]byte(d.Config.Token))}

	r := chi.NewRouter()
	r.Use(httpx.WithRequestID)
	r.Use(httpx.Recover(d.Logger))
	r.Use(httpx.AccessLog(d.Logger))

	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "Not found.", nil)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.", nil)
	})

	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"version": Version,
			"driver":  d.Config.VMDriver,
		})
	})

	// Everything below is for the control plane only.
	r.Group(func(r chi.Router) {
		r.Use(s.requireAgentToken)
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
				next.ServeHTTP(w, r)
			})
		})

		r.Get("/capacity", s.handleCapacity)
		r.Get("/vms", s.handleList)
		r.Post("/vms", s.handleCreate)

		r.Route("/vms/{id}", func(r chi.Router) {
			r.Use(s.validVMID)
			r.Get("/", s.handleStatus)
			r.Delete("/", s.locked(s.handleDelete))
			r.Post("/start", s.locked(s.simple(d.Driver.Start)))
			r.Post("/suspend", s.locked(s.simple(d.Driver.Suspend)))
			r.Post("/shutdown", s.locked(s.handleShutdown))
			r.Post("/force-stop", s.locked(s.simple(d.Driver.ForceStop)))
			r.Post("/reboot", s.locked(s.simple(d.Driver.Reboot)))
			r.Post("/discard-saved-state", s.locked(s.simple(d.Driver.DiscardSavedState)))
			r.Post("/resize", s.locked(s.handleResize))
			r.Post("/sync-clock", s.locked(s.simple(d.Driver.SyncClock)))

			r.Get("/snapshots", s.handleSnapshotList)
			r.Post("/snapshots", s.locked(s.handleSnapshotCreate))
			r.Post("/snapshots/{sid}/restore", s.locked(s.snapshotOp(d.Driver.SnapshotRestore)))
			r.Delete("/snapshots/{sid}", s.locked(s.snapshotOp(d.Driver.SnapshotDelete)))
		})
	})

	return r
}

// requireAgentToken checks "Authorization: Bearer <agent token>" in constant time.
func (s *server) requireAgentToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		// Comparing hashes keeps the comparison constant-time even when the
		// lengths differ.
		got := sha256.Sum256([]byte(token))
		if !ok || subtle.ConstantTimeCompare(got[:], s.tokenHash[:]) != 1 {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated", "Missing or wrong agent token.", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// validVMID rejects any {id} that is not a UUID before it reaches a driver.
func (s *server) validVMID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !vmtypes.IsUUID(chi.URLParam(r, "id")) {
			httpx.WriteError(w, http.StatusBadRequest, "validation_failed", "The VM id must be a UUID.", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// locked runs h while holding the VM's lock, or answers 409 busy.
func (s *server) locked(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if !s.locks.tryLock(id) {
			httpx.WriteError(w, http.StatusConflict, "busy", "Another operation is running on this VM.", nil)
			return
		}
		defer s.locks.unlock(id)
		h(w, r)
	}
}

// opContext keeps an operation running to its own deadline even if the
// caller disconnects. A half-finished suspend or delete is worse than one
// whose result nobody reads; the control plane reconciles afterwards.
func opContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Minute)
}

// writeDriverError maps a driver error to a response.
func (s *server) writeDriverError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, driver.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found", "No such VM or snapshot on this host.", nil)
	case errors.Is(err, driver.ErrInvalidState):
		httpx.WriteError(w, http.StatusConflict, "invalid_state", err.Error(), nil)
	case errors.Is(err, driver.ErrShutdownTimeout):
		httpx.WriteError(w, http.StatusConflict, "shutdown_timeout", "The guest did not shut down in time.", nil)
	default:
		s.Logger.ErrorContext(r.Context(), "driver error",
			"request_id", httpx.RequestID(r.Context()), "path", r.URL.Path, "error", err.Error())
		// The detail is for the control plane's log. It never reaches a user.
		httpx.WriteError(w, http.StatusInternalServerError, "driver_error", "The VM operation failed.",
			map[string]any{"detail": err.Error()})
	}
}

func ok(w http.ResponseWriter) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// decode reads an optional JSON body into dst.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		httpx.WriteError(w, http.StatusUnsupportedMediaType, "validation_failed", "Send the request as JSON.", nil)
		return false
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		httpx.WriteError(w, http.StatusBadRequest, "validation_failed", "The request body is not valid JSON.", nil)
		return false
	}
	return true
}

// simple adapts a driver method that only needs the VM id.
func (s *server) simple(op func(ctx context.Context, id string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := opContext(r)
		defer cancel()
		if err := op(ctx, chi.URLParam(r, "id")); err != nil {
			s.writeDriverError(w, r, err)
			return
		}
		ok(w)
	}
}

func (s *server) handleCapacity(w http.ResponseWriter, r *http.Request) {
	c, err := s.Driver.Capacity(r.Context())
	if err != nil {
		s.writeDriverError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, c)
}

func (s *server) handleList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Driver.List(r.Context())
	if err != nil {
		s.writeDriverError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"vms": list})
}

func (s *server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var spec vmtypes.VMSpec
	if !decode(w, r, &spec) {
		return
	}
	if msg := spec.Validate(); msg != "" {
		httpx.WriteError(w, http.StatusUnprocessableEntity, "validation_failed", msg, nil)
		return
	}
	if !s.locks.tryLock(spec.ID) {
		httpx.WriteError(w, http.StatusConflict, "busy", "Another operation is running on this VM.", nil)
		return
	}
	defer s.locks.unlock(spec.ID)

	ctx, cancel := opContext(r)
	defer cancel()
	if err := s.Driver.Create(ctx, spec); err != nil {
		s.writeDriverError(w, r, err)
		return
	}
	ok(w)
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.Driver.Status(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeDriverError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (s *server) handleDelete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := opContext(r)
	defer cancel()
	if err := s.Driver.Delete(ctx, chi.URLParam(r, "id")); err != nil {
		s.writeDriverError(w, r, err)
		return
	}
	ok(w)
}

func (s *server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	req := struct {
		TimeoutSeconds int `json:"timeout_seconds"`
	}{TimeoutSeconds: 120}
	if !decode(w, r, &req) {
		return
	}
	if req.TimeoutSeconds < 1 || req.TimeoutSeconds > 600 {
		httpx.WriteError(w, http.StatusUnprocessableEntity, "validation_failed", "timeout_seconds must be between 1 and 600.", nil)
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	if err := s.Driver.Shutdown(ctx, chi.URLParam(r, "id"), time.Duration(req.TimeoutSeconds)*time.Second); err != nil {
		s.writeDriverError(w, r, err)
		return
	}
	ok(w)
}

func (s *server) handleResize(w http.ResponseWriter, r *http.Request) {
	var req vmtypes.ResizeRequest
	if !decode(w, r, &req) {
		return
	}
	if req.RAMMB < 512 || req.VCPUs < 1 || req.DiskGB < 1 {
		httpx.WriteError(w, http.StatusUnprocessableEntity, "validation_failed", "ram_mb, vcpus and disk_gb are required.", nil)
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	if err := s.Driver.Resize(ctx, chi.URLParam(r, "id"), req.RAMMB, req.VCPUs, req.DiskGB); err != nil {
		s.writeDriverError(w, r, err)
		return
	}
	ok(w)
}

func (s *server) handleSnapshotList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Driver.SnapshotList(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeDriverError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"snapshots": list})
}

func (s *server) handleSnapshotCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SnapshotID string `json:"snapshot_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !vmtypes.IsUUID(req.SnapshotID) {
		httpx.WriteError(w, http.StatusBadRequest, "validation_failed", "snapshot_id must be a UUID.", nil)
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	if err := s.Driver.SnapshotCreate(ctx, chi.URLParam(r, "id"), req.SnapshotID); err != nil {
		s.writeDriverError(w, r, err)
		return
	}
	ok(w)
}

// snapshotOp adapts a driver method that needs the VM id and a snapshot id.
func (s *server) snapshotOp(op func(ctx context.Context, id, snapshotID string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sid := chi.URLParam(r, "sid")
		if !vmtypes.IsUUID(sid) {
			httpx.WriteError(w, http.StatusBadRequest, "validation_failed", "The snapshot id must be a UUID.", nil)
			return
		}
		ctx, cancel := opContext(r)
		defer cancel()
		if err := op(ctx, chi.URLParam(r, "id"), sid); err != nil {
			s.writeDriverError(w, r, err)
			return
		}
		ok(w)
	}
}
