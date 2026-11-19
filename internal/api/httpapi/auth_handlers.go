package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"zynthera/internal/api/auth"
	"zynthera/internal/api/jobs"
	mailer "zynthera/internal/api/mail"
	"zynthera/internal/api/store/sqlcgen"
	"zynthera/internal/api/vms"
	"zynthera/internal/api/vmsvc"
	"zynthera/internal/shared/httpx"
)

const (
	purposeVerifyEmail   = "verify_email"
	purposeResetPassword = "reset_password"

	verifyTokenTTL = 24 * time.Hour
	resetTokenTTL  = time.Hour
)

// userJSON is the account object returned to the browser.
type userJSON struct {
	ID            uuid.UUID `json:"id"`
	Email         string    `json:"email"`
	Role          string    `json:"role"`
	EmailVerified bool      `json:"email_verified"`
	CreatedAt     time.Time `json:"created_at"`
}

func toUserJSON(u sqlcgen.User) userJSON {
	return userJSON{
		ID:            u.ID,
		Email:         u.Email,
		Role:          u.Role,
		EmailVerified: u.EmailVerifiedAt != nil,
		CreatedAt:     u.CreatedAt.UTC(),
	}
}

// normalizeEmail returns a trimmed address, or false when it is not a plain
// single email address.
func normalizeEmail(raw string) (string, bool) {
	email := strings.TrimSpace(raw)
	if email == "" || len(email) > 254 || strings.ContainsAny(email, "\r\n\t <>,;\"") {
		return "", false
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return "", false
	}
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" || !strings.Contains(domain, ".") {
		return "", false
	}
	return email, true
}

func passwordProblem(pw string) string {
	n := utf8.RuneCountInString(pw)
	switch {
	case n < auth.MinPasswordLength:
		return "Use at least 10 characters."
	case n > auth.MaxPasswordLength || len(pw) > 4*auth.MaxPasswordLength:
		return "Use at most 128 characters."
	}
	return ""
}

// issueEmailToken retires older tokens of the same purpose and stores a new one.
func (s *server) issueEmailToken(ctx context.Context, userID uuid.UUID, purpose string, ttl time.Duration) (string, error) {
	plain, hash, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	if err := s.Q.InvalidateEmailTokens(ctx, sqlcgen.InvalidateEmailTokensParams{UserID: userID, Purpose: purpose}); err != nil {
		return "", err
	}
	err = s.Q.CreateEmailToken(ctx, sqlcgen.CreateEmailTokenParams{
		UserID:    userID,
		Purpose:   purpose,
		TokenHash: hash,
		ExpiresAt: time.Now().Add(ttl),
	})
	return plain, err
}

func (s *server) sendInBackground(msg mailer.Message, what string) {
	s.background(func(ctx context.Context) {
		if err := s.Mailer.Send(ctx, msg); err != nil {
			s.Logger.Error("sending email failed", "what", what, "error", err.Error())
		}
	})
}

func (s *server) sendVerification(ctx context.Context, user sqlcgen.User) error {
	token, err := s.issueEmailToken(ctx, user.ID, purposeVerifyEmail, verifyTokenTTL)
	if err != nil {
		return err
	}
	s.sendInBackground(mailer.VerifyEmail(s.Config.WebOrigin, user.Email, token), "verification")
	return nil
}

// POST /api/auth/signup
//
// The response is the same whether or not the email already has an account,
// so accounts cannot be discovered by trying addresses.
func (s *server) handleSignup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	fields := fieldErrors{}
	email, ok := normalizeEmail(req.Email)
	if !ok {
		fields["email"] = "Enter a valid email address."
	}
	if msg := passwordProblem(req.Password); msg != "" {
		fields["password"] = msg
	}
	if len(fields) > 0 {
		writeValidation(w, fields)
		return
	}
	if !s.allow(w, r, auth.RuleSignupPerIP, clientIP(r).String()) {
		return
	}

	// Hash before looking at the database, so the work done (and the time
	// taken) is the same for new and existing addresses.
	hash, err := auth.HashPassword(req.Password, s.Argon2)
	if err != nil {
		s.internal(w, r, "hash password", err)
		return
	}
	user, err := s.Q.CreateUser(r.Context(), sqlcgen.CreateUserParams{Email: email, PasswordHash: hash})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// The address already has an account. Say nothing different.
		s.Logger.InfoContext(r.Context(), "signup for an existing address ignored")
	case err != nil:
		s.internal(w, r, "create user", err)
		return
	default:
		if err := s.sendVerification(r.Context(), user); err != nil {
			s.internal(w, r, "send verification", err)
			return
		}
		s.Logger.InfoContext(r.Context(), "user signed up", "user_id", user.ID.String())
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "ok"})
}

// POST /api/auth/login
func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	email := strings.TrimSpace(req.Email)
	if email == "" || req.Password == "" {
		writeValidation(w, fieldErrors{"email": "Enter your email and password."})
		return
	}
	if !s.allow(w, r, auth.RuleLoginPerIPEmail, clientIP(r).String()+"|"+strings.ToLower(email)) {
		return
	}

	invalid := func() {
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_credentials", "Wrong email or password.", nil)
	}

	user, err := s.Q.GetUserByEmail(r.Context(), email)
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.internal(w, r, "get user", err)
		return
	}
	// Always verify against some hash, so an unknown address takes as long
	// as a wrong password.
	hash := s.dummyHash
	if found {
		hash = user.PasswordHash
	}
	if len(req.Password) > 4*auth.MaxPasswordLength {
		invalid()
		return
	}
	match, err := auth.VerifyPassword(req.Password, hash)
	if err != nil {
		s.internal(w, r, "verify password", err)
		return
	}
	if !found || !match || user.DisabledAt != nil {
		s.Logger.InfoContext(r.Context(), "login failed", "ip", clientIP(r).String())
		invalid()
		return
	}

	// Session rotation: whatever session the browser had is replaced.
	if err := s.endSession(w, r); err != nil {
		s.internal(w, r, "end old session", err)
		return
	}
	if err := s.startSession(w, r, user); err != nil {
		s.internal(w, r, "start session", err)
		return
	}
	s.Logger.InfoContext(r.Context(), "login", "user_id", user.ID.String(), "ip", clientIP(r).String())
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"user": toUserJSON(user)})
}

// POST /api/auth/logout
func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if user, ok := currentUser(r.Context()); ok {
		s.suspendOnLogout(r.Context(), user)
	}
	if err := s.endSession(w, r); err != nil {
		s.internal(w, r, "end session", err)
		return
	}
	if user, ok := currentUser(r.Context()); ok {
		s.Logger.InfoContext(r.Context(), "logout", "user_id", user.ID.String())
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GET /api/auth/me
func (s *server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, _ := currentUser(r.Context())
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"user": toUserJSON(user)})
}

func invalidLink(w http.ResponseWriter) {
	httpx.WriteError(w, http.StatusUnprocessableEntity, "invalid_token",
		"This link is invalid, has expired or was already used.", nil)
}

// POST /api/auth/verify-email
func (s *server) handleVerifyEmail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !auth.LooksLikeToken(req.Token) {
		invalidLink(w)
		return
	}
	userID, err := s.Q.ConsumeEmailToken(r.Context(), sqlcgen.ConsumeEmailTokenParams{
		TokenHash: auth.HashToken(req.Token),
		Purpose:   purposeVerifyEmail,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		invalidLink(w)
		return
	}
	if err != nil {
		s.internal(w, r, "consume verify token", err)
		return
	}
	if err := s.Q.MarkEmailVerified(r.Context(), userID); err != nil {
		s.internal(w, r, "mark verified", err)
		return
	}
	s.Logger.InfoContext(r.Context(), "email verified", "user_id", userID.String())
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /api/auth/resend-verification
func (s *server) handleResendVerification(w http.ResponseWriter, r *http.Request) {
	user, _ := currentUser(r.Context())
	if user.EmailVerifiedAt != nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "already_verified"})
		return
	}
	if !s.allow(w, r, auth.RuleResendVerification, user.ID.String()) {
		return
	}
	if err := s.sendVerification(r.Context(), user); err != nil {
		s.internal(w, r, "send verification", err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "ok"})
}

// POST /api/auth/forgot-password
//
// Like signup, the response never reveals whether the address has an account.
func (s *server) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	email, ok := normalizeEmail(req.Email)
	if !ok {
		writeValidation(w, fieldErrors{"email": "Enter a valid email address."})
		return
	}
	if !s.allow(w, r, auth.RuleForgotPerIP, clientIP(r).String()) {
		return
	}
	if !s.allow(w, r, auth.RuleForgotPerEmail, strings.ToLower(email)) {
		return
	}

	user, err := s.Q.GetUserByEmail(r.Context(), email)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		s.internal(w, r, "get user", err)
		return
	case user.DisabledAt == nil:
		token, err := s.issueEmailToken(r.Context(), user.ID, purposeResetPassword, resetTokenTTL)
		if err != nil {
			s.internal(w, r, "issue reset token", err)
			return
		}
		s.sendInBackground(mailer.ResetPassword(s.Config.WebOrigin, user.Email, token), "password reset")
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "ok"})
}

// POST /api/auth/reset-password
func (s *server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg := passwordProblem(req.Password); msg != "" {
		writeValidation(w, fieldErrors{"password": msg})
		return
	}
	if !auth.LooksLikeToken(req.Token) {
		invalidLink(w)
		return
	}
	hash, err := auth.HashPassword(req.Password, s.Argon2)
	if err != nil {
		s.internal(w, r, "hash password", err)
		return
	}

	// Token use, password change and session removal succeed or fail together.
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		s.internal(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(r.Context())) }()
	q := s.Q.WithTx(tx)

	userID, err := q.ConsumeEmailToken(r.Context(), sqlcgen.ConsumeEmailTokenParams{
		TokenHash: auth.HashToken(req.Token),
		Purpose:   purposeResetPassword,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		invalidLink(w)
		return
	}
	if err != nil {
		s.internal(w, r, "consume reset token", err)
		return
	}
	if err := q.UpdateUserPassword(r.Context(), sqlcgen.UpdateUserPasswordParams{ID: userID, PasswordHash: hash}); err != nil {
		s.internal(w, r, "update password", err)
		return
	}
	// Receiving the reset email proves the address belongs to the user.
	if err := q.MarkEmailVerified(r.Context(), userID); err != nil {
		s.internal(w, r, "mark verified", err)
		return
	}
	if err := q.DeleteUserSessions(r.Context(), userID); err != nil {
		s.internal(w, r, "delete sessions", err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.internal(w, r, "commit", err)
		return
	}
	s.clearSessionCookie(w)
	s.Logger.InfoContext(r.Context(), "password reset", "user_id", userID.String())
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// suspendOnLogout suspends the user's running PCs, except those with an
// active keep-alive. Problems are logged and never block the logout: a PC
// that stays running is caught by the idle rule later.
func (s *server) suspendOnLogout(ctx context.Context, user sqlcgen.User) {
	running, err := s.Q.ListRunningVMsForUser(ctx, user.ID)
	if err != nil {
		s.Logger.ErrorContext(ctx, "logout: list running vms", "error", err.Error())
		return
	}
	now := time.Now()
	for _, vm := range running {
		if vm.KeepAliveUntil != nil && vm.KeepAliveUntil.After(now) {
			continue
		}
		if _, _, err := s.Svc.Begin(ctx, vm.ID, vms.ActionSuspend, vmsvc.BeginOpts{}); err != nil {
			if !errors.Is(err, vmsvc.ErrInvalidState) && !errors.Is(err, vmsvc.ErrNotFound) {
				s.Logger.ErrorContext(ctx, "logout: begin suspend", "vm_id", vm.ID.String(), "error", err.Error())
			}
			continue
		}
		if err := s.Queue.Enqueue(ctx, jobs.TypeSuspend, jobs.Payload{VMID: vm.ID, Actor: "user", Reason: "logout"}); err != nil {
			s.Logger.ErrorContext(ctx, "logout: enqueue suspend", "vm_id", vm.ID.String(), "error", err.Error())
		}
	}
}
