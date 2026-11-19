// Package httpx holds the HTTP plumbing shared by the API and the agent:
// the JSON error envelope, basic middleware and graceful shutdown.
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"
)

// ErrorBody is the single error shape returned by every endpoint.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the content of the error envelope.
type ErrorDetail struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	// The header is already sent, so an encoding error can only be logged by
	// the access log as a short response.
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes the error envelope.
func WriteError(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	WriteJSON(w, status, ErrorBody{Error: ErrorDetail{Code: code, Message: message, Details: details}})
}

// Internal writes the generic 500 response. The real cause must be logged by
// the caller and is never sent to the client.
func Internal(w http.ResponseWriter) {
	WriteError(w, http.StatusInternalServerError, "internal", "Something went wrong on our side.", nil)
}

type ctxKey int

const requestIDKey ctxKey = iota

// RequestID returns the id assigned to this request, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// WithRequestID gives each request a random id, echoed in X-Request-Id.
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b [8]byte
		_, _ = rand.Read(b[:])
		id := hex.EncodeToString(b[:])
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// Recover turns a panic in a handler into a logged 500.
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec)
				}
				log.ErrorContext(r.Context(), "panic in handler",
					"request_id", RequestID(r.Context()),
					"path", r.URL.Path,
					"panic", rec,
					"stack", string(debug.Stack()))
				Internal(w)
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// statusWriter records the response status for the access log. It forwards
// Flush (needed by Server-Sent Events) and Unwrap (needed by WebSocket
// upgrades through http.ResponseController).
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// AccessLog logs one line per request. It logs the path only and never the
// query string, because some URLs carry tokens there.
func AccessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r)
			log.InfoContext(r.Context(), "http request",
				"request_id", RequestID(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration_ms", time.Since(start).Milliseconds())
		})
	}
}

// Serve runs the handler on addr until ctx is cancelled, then shuts down
// gracefully, giving open requests up to 15 seconds to finish.
func Serve(ctx context.Context, log *slog.Logger, addr string, h http.Handler) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
