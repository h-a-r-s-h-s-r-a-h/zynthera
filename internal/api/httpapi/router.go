// Package httpapi is the HTTP layer of the control plane: router, middleware
// and handlers.
package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"zynthera/internal/api/appconfig"
	"zynthera/internal/api/auth"
	"zynthera/internal/api/events"
	"zynthera/internal/api/jobs"
	"zynthera/internal/api/mail"
	"zynthera/internal/api/secrets"
	"zynthera/internal/api/store/sqlcgen"
	"zynthera/internal/api/vms"
	"zynthera/internal/api/vmsvc"
	"zynthera/internal/shared/httpx"
)

// maxBodyBytes caps every JSON request body. File uploads never pass through
// the API; they go straight to the agent.
const maxBodyBytes = 1 << 20

// Pinger is anything whose health can be checked.
type Pinger interface {
	Ping(ctx context.Context) error
}

// DB is the part of the connection pool the HTTP layer uses directly.
type DB interface {
	Pinger
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Deps is everything the HTTP layer needs.
type Deps struct {
	Config  appconfig.Config
	Logger  *slog.Logger
	DB      DB
	Q       *sqlcgen.Queries
	Redis   Pinger
	Limiter auth.Limiter
	Mailer  mail.Sender
	// Secrets encrypts host credentials stored in the database.
	Secrets *secrets.Box
	// Svc changes VM state; Queue runs the jobs; Bus carries live updates.
	Svc   *vmsvc.Service
	Queue jobs.Enqueuer
	Bus   *events.Bus
	// RDB stores the hosts' recent heartbeats for the reconciler.
	RDB *redis.Client
	// Argon2 is the cost of hashing account passwords.
	Argon2 auth.Argon2Params
	// Go runs background work. Nil means "start a goroutine".
	Go func(func())
}

type server struct {
	Deps
	// dummyHash is verified when a login names an unknown email, so that
	// case takes as long as a wrong password.
	dummyHash string
}

// NewRouter builds the full API router. Middleware order follows spec
// section 10.1: request ID, panic recovery, access log, security headers,
// body size limit, session loading, Origin check, rate limit, handler.
// Rate limits are applied inside the handlers that need them, because their
// keys depend on the request body or the logged-in user.
func NewRouter(d Deps) (http.Handler, error) {
	if d.Go == nil {
		d.Go = func(fn func()) { go fn() }
	}
	if d.Argon2 == (auth.Argon2Params{}) {
		d.Argon2 = auth.DefaultArgon2Params
	}
	dummy, err := auth.HashPassword("zynthera-dummy-password", d.Argon2)
	if err != nil {
		return nil, fmt.Errorf("prepare dummy hash: %w", err)
	}
	s := &server{Deps: d, dummyHash: dummy}

	r := chi.NewRouter()
	r.Use(httpx.WithRequestID)
	r.Use(httpx.Recover(d.Logger))
	r.Use(httpx.AccessLog(d.Logger))
	r.Use(securityHeaders)
	r.Use(limitBody(maxBodyBytes))

	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "Not found.", nil)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.", nil)
	})

	r.Get("/health", s.handleHealth)

	// Called by host agents, authenticated with the host's agent token. In
	// production the proxy also limits these routes to the hosts' addresses.
	r.Route("/internal/agent", func(r chi.Router) {
		r.Post("/heartbeat", s.handleAgentHeartbeat)
	})

	r.Route("/api", func(r chi.Router) {
		r.Use(s.loadSession)
		r.Use(originCheck(d.Config.WebOrigin))

		// Same check as /health, reachable through the web app's /api rewrite.
		r.Get("/health", s.handleHealth)

		r.Route("/auth", func(r chi.Router) {
			r.Post("/signup", s.handleSignup)
			r.Post("/login", s.handleLogin)
			r.Post("/logout", s.handleLogout)
			r.Post("/verify-email", s.handleVerifyEmail)
			r.Post("/forgot-password", s.handleForgotPassword)
			r.Post("/reset-password", s.handleResetPassword)

			r.Group(func(r chi.Router) {
				r.Use(requireUser)
				r.Get("/me", s.handleMe)
				r.Post("/resend-verification", s.handleResendVerification)
			})
		})

		r.Group(func(r chi.Router) {
			r.Use(requireUser)
			r.Get("/images", s.handleListImages)
			r.Get("/options", s.handleOptions)
			r.Get("/events", s.handleEvents)

			r.Get("/vms", s.handleListVMs)
			r.Post("/vms", s.handleCreateVM)
			r.Route("/vms/{id}", func(r chi.Router) {
				r.Get("/", s.handleGetVM)
				r.Patch("/", s.handlePatchVM)
				r.Delete("/", s.vmAction(vms.ActionDelete, jobs.TypeDelete, ""))
				r.Post("/start", s.vmAction(vms.ActionStart, jobs.TypeStart, ""))
				r.Post("/suspend", s.vmAction(vms.ActionSuspend, jobs.TypeSuspend, "user"))
				r.Post("/shutdown", s.vmAction(vms.ActionShutdown, jobs.TypeShutdown, ""))
				r.Post("/force-stop", s.vmAction(vms.ActionForceStop, jobs.TypeForceStop, ""))
				r.Post("/reboot", s.vmAction(vms.ActionReboot, jobs.TypeReboot, ""))
				r.Post("/discard-saved-state", s.vmAction(vms.ActionDiscardSavedState, jobs.TypeDiscard, ""))
				r.Get("/events", s.handleVMEvents)

				r.Get("/snapshots", s.handleListSnapshots)
				r.Post("/snapshots", s.handleCreateSnapshot)
				r.Post("/snapshots/{sid}/restore", s.snapshotAction(vms.ActionRestoreSnapshot, jobs.TypeSnapshotRestore, "Restoring a snapshot"))
				r.Delete("/snapshots/{sid}", s.snapshotAction(vms.ActionDeleteSnapshot, jobs.TypeSnapshotDelete, "Deleting a snapshot"))
			})
		})

		r.Route("/admin", func(r chi.Router) {
			r.Use(requireUser)
			r.Use(requireAdmin)
			r.Get("/hosts", s.handleAdminListHosts)
			r.Post("/hosts", s.handleAdminCreateHost)
			r.Patch("/hosts/{id}", s.handleAdminPatchHost)
		})
	})

	return r, nil
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	checks := map[string]string{"postgres": "ok", "redis": "ok"}
	healthy := true
	if err := s.DB.Ping(ctx); err != nil {
		s.Logger.ErrorContext(ctx, "health: postgres ping failed", "error", err.Error())
		checks["postgres"] = "down"
		healthy = false
	}
	if err := s.Redis.Ping(ctx); err != nil {
		s.Logger.ErrorContext(ctx, "health: redis ping failed", "error", err.Error())
		checks["redis"] = "down"
		healthy = false
	}

	status, state := http.StatusOK, "ok"
	if !healthy {
		status, state = http.StatusServiceUnavailable, "degraded"
	}
	httpx.WriteJSON(w, status, map[string]any{"status": state, "checks": checks})
}
