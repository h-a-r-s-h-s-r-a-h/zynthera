package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"zynthera/internal/api/auth"
	"zynthera/internal/shared/httpx"
)

// fieldErrors maps a request field to a message the UI can show next to it.
type fieldErrors map[string]string

func writeValidation(w http.ResponseWriter, fields fieldErrors) {
	httpx.WriteError(w, http.StatusUnprocessableEntity, "validation_failed",
		"Please check the highlighted fields.", map[string]any{"fields": fields})
}

// decodeJSON reads a JSON request body into dst. It writes the error
// response itself and returns false when the body is unusable.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		httpx.WriteError(w, http.StatusUnsupportedMediaType, "validation_failed",
			"Send the request as JSON (Content-Type: application/json).", nil)
		return false
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.WriteError(w, http.StatusRequestEntityTooLarge, "validation_failed", "Request body is too large.", nil)
			return false
		}
		httpx.WriteError(w, http.StatusBadRequest, "validation_failed", "The request body is not valid JSON.", nil)
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		httpx.WriteError(w, http.StatusBadRequest, "validation_failed", "The request body is not valid JSON.", nil)
		return false
	}
	return true
}

// clientIP returns the address of the browser. The API always sits behind
// our own proxy (Caddy in production, the Next.js dev server in development),
// which appends the real client address as the last X-Forwarded-For entry.
// That last entry is only trusted when the direct peer is a local or private
// address, which is the case for our proxy and never for an internet client.
func clientIP(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	peer = peer.Unmap()
	if !peer.IsLoopback() && !peer.IsPrivate() {
		return peer
	}
	xff := r.Header.Values("X-Forwarded-For")
	if len(xff) == 0 {
		return peer
	}
	parts := strings.Split(xff[len(xff)-1], ",")
	last := strings.TrimSpace(parts[len(parts)-1])
	if addr, err := netip.ParseAddr(last); err == nil {
		return addr.Unmap()
	}
	return peer
}

// allow applies one rate limit. When the limit is reached, or the limiter
// itself fails, it writes the response and returns false.
func (s *server) allow(w http.ResponseWriter, r *http.Request, rule auth.Rule, key string) bool {
	ok, retryAfter, err := s.Limiter.Allow(r.Context(), rule, key)
	if err != nil {
		// Failing closed: without the limiter, login would be open to
		// unlimited password guessing.
		s.internal(w, r, "rate limiter", err)
		return false
	}
	if !ok {
		seconds := int(retryAfter/time.Second) + 1
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		httpx.WriteError(w, http.StatusTooManyRequests, "rate_limited",
			"Too many attempts. Please wait a little and try again.",
			map[string]any{"retry_after_seconds": seconds})
		return false
	}
	return true
}

// internal logs the real cause and sends the generic 500 response.
func (s *server) internal(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.Logger.ErrorContext(r.Context(), "internal error",
		"request_id", httpx.RequestID(r.Context()), "what", what, "error", err.Error())
	httpx.Internal(w)
}

// background runs fn outside the request, for work the response must not
// wait for (sending email). Tests replace Deps.Go to run it inline.
func (s *server) background(fn func(ctx context.Context)) {
	s.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		fn(ctx)
	})
}
