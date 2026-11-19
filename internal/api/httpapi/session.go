package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"zynthera/internal/api/auth"
	"zynthera/internal/api/store/sqlcgen"
	"zynthera/internal/shared/httpx"
)

const sessionCookie = "zyn_session"

// How often last_seen_at is refreshed, so a busy session does not write to
// the database on every request.
const sessionTouchInterval = 5 * time.Minute

type userCtxKey struct{}

// currentUser returns the logged-in user, if any.
func currentUser(ctx context.Context) (sqlcgen.User, bool) {
	u, ok := ctx.Value(userCtxKey{}).(sqlcgen.User)
	return u, ok
}

// loadSession attaches the logged-in user to the request when the session
// cookie is valid. It never rejects a request; requireUser does that.
func (s *server) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || !auth.LooksLikeToken(c.Value) {
			next.ServeHTTP(w, r)
			return
		}
		row, err := s.Q.GetSessionUser(r.Context(), auth.HashToken(c.Value))
		if errors.Is(err, pgx.ErrNoRows) {
			s.clearSessionCookie(w)
			next.ServeHTTP(w, r)
			return
		}
		if err != nil {
			s.internal(w, r, "load session", err)
			return
		}
		if time.Since(row.SessionLastSeenAt) > sessionTouchInterval {
			if err := s.Q.TouchSession(r.Context(), row.SessionID); err != nil {
				s.Logger.WarnContext(r.Context(), "touch session failed", "error", err.Error())
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userCtxKey{}, row.User)))
	})
}

// requireUser rejects requests without a valid session.
func requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := currentUser(r.Context()); !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated", "Please log in.", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// startSession creates a session row and sets the cookie.
func (s *server) startSession(w http.ResponseWriter, r *http.Request, user sqlcgen.User) error {
	plain, hash, err := auth.NewToken()
	if err != nil {
		return err
	}
	ttl := time.Duration(s.Config.SessionTTLHours) * time.Hour
	params := sqlcgen.CreateSessionParams{
		UserID:    user.ID,
		TokenHash: hash,
		ExpiresAt: time.Now().Add(ttl),
	}
	if ip := clientIP(r); ip.IsValid() {
		params.Ip = &ip
	}
	if ua := r.UserAgent(); ua != "" {
		if len(ua) > 400 {
			ua = ua[:400]
		}
		params.UserAgent = pgtype.Text{String: ua, Valid: true}
	}
	if _, err := s.Q.CreateSession(r.Context(), params); err != nil {
		return err
	}
	// Secure is off only in development, where the site runs on plain http.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: see comment above
		Name:     sessionCookie,
		Value:    plain,
		Path:     "/",
		MaxAge:   int(ttl / time.Second),
		HttpOnly: true,
		Secure:   s.Config.IsProduction(),
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// endSession deletes the session behind the request's cookie, if any.
func (s *server) endSession(w http.ResponseWriter, r *http.Request) error {
	s.clearSessionCookie(w)
	c, cookieErr := r.Cookie(sessionCookie)
	if cookieErr == nil && auth.LooksLikeToken(c.Value) {
		return s.Q.DeleteSessionByTokenHash(r.Context(), auth.HashToken(c.Value))
	}
	return nil
}

func (s *server) clearSessionCookie(w http.ResponseWriter) {
	// Secure is off only in development, where the site runs on plain http.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: see comment above
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.Config.IsProduction(),
		SameSite: http.SameSiteLaxMode,
	})
}
