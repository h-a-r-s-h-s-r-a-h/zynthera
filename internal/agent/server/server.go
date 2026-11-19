// Package server is the HTTP API of the host agent.
package server

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"zynthera/internal/agent/agentconfig"
	"zynthera/internal/shared/httpx"
)

// Version is reported in /health and in heartbeats.
const Version = "0.1.0"

// Deps is everything the agent's HTTP layer needs.
type Deps struct {
	Config agentconfig.Config
	Logger *slog.Logger
}

// NewRouter builds the agent router. The VM, stream and file routes are
// added in later milestones.
func NewRouter(d Deps) http.Handler {
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

	return r
}
