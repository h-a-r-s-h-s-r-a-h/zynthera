// Package httpapi is the HTTP layer of the control plane: router, middleware
// and handlers.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"zynthera/internal/api/appconfig"
	"zynthera/internal/shared/httpx"
)

// maxBodyBytes caps every JSON request body. File uploads never pass through
// the API; they go straight to the agent.
const maxBodyBytes = 1 << 20

// Pinger is anything whose health can be checked.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Deps is everything the HTTP layer needs.
type Deps struct {
	Config appconfig.Config
	Logger *slog.Logger
	DB     Pinger
	Redis  Pinger
}

// NewRouter builds the full API router. Middleware order follows spec
// section 10.1: request ID, panic recovery, access log, security headers,
// body size limit, session loading, Origin check, rate limit, handler.
// Session loading and rate limits are added in milestone 1.
func NewRouter(d Deps) http.Handler {
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

	health := healthHandler(d)
	r.Get("/health", health)

	r.Route("/api", func(r chi.Router) {
		r.Use(originCheck(d.Config.WebOrigin))
		// Same check, reachable through the web app's /api rewrite.
		r.Get("/health", health)
	})

	return r
}

func healthHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		checks := map[string]string{"postgres": "ok", "redis": "ok"}
		healthy := true
		if err := d.DB.Ping(ctx); err != nil {
			d.Logger.ErrorContext(ctx, "health: postgres ping failed", "error", err)
			checks["postgres"] = "down"
			healthy = false
		}
		if err := d.Redis.Ping(ctx); err != nil {
			d.Logger.ErrorContext(ctx, "health: redis ping failed", "error", err)
			checks["redis"] = "down"
			healthy = false
		}

		status, state := http.StatusOK, "ok"
		if !healthy {
			status, state = http.StatusServiceUnavailable, "degraded"
		}
		httpx.WriteJSON(w, status, map[string]any{"status": state, "checks": checks})
	}
}
