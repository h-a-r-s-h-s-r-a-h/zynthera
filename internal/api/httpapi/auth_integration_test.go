package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"zynthera/internal/api/appconfig"
	"zynthera/internal/api/auth"
	"zynthera/internal/api/mail"
	"zynthera/internal/api/secrets"
	"zynthera/internal/api/store/sqlcgen"
	"zynthera/internal/api/testdb"
	"zynthera/internal/shared/httpx"
	"zynthera/internal/shared/logging"
)

const testOrigin = "http://localhost:3000"

type redisPing struct{ ping func(context.Context) error }

func (r redisPing) Ping(ctx context.Context) error { return r.ping(ctx) }

// testApp is a real API on a real database, with a fake mailer.
type testApp struct {
	t      *testing.T
	srv    *httptest.Server
	mailer *mail.Fake
	pool   *pgxpool.Pool
}

func newTestApp(t *testing.T) *testApp {
	t.Helper()
	pool, rdb := testdb.Open(t)
	mailer := &mail.Fake{}
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewRouter(Deps{
		Secrets: box,
		Config:  appconfig.Config{WebOrigin: testOrigin, SessionTTLHours: 720, HostOfflineAfterSeconds: 45},
		Logger:  logging.NewTo(io.Discard, "test"),
		DB:      pool,
		Q:       sqlcgen.New(pool),
		Redis:   redisPing{func(ctx context.Context) error { return rdb.Ping(ctx).Err() }},
		Limiter: auth.NewRedisLimiter(rdb),
		Mailer:  mailer,
		Argon2:  auth.Argon2Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32},
		Go:      func(fn func()) { fn() }, // send email inline so tests are deterministic
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &testApp{t: t, srv: srv, mailer: mailer, pool: pool}
}

// browser is an HTTP client with its own cookie jar, like one browser.
type browser struct {
	app    *testApp
	client *http.Client
	origin string
}

func (a *testApp) newBrowser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{app: a, client: &http.Client{Jar: jar}, origin: testOrigin}
}

type response struct {
	Status int
	Header http.Header
	Raw    string
	Error  httpx.ErrorDetail
}

func (b *browser) do(method, path string, body any) response {
	b.app.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			b.app.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, b.app.srv.URL+path, rd)
	if err != nil {
		b.app.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if b.origin != "" {
		req.Header.Set("Origin", b.origin)
	}
	res, err := b.client.Do(req)
	if err != nil {
		b.app.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := response{Status: res.StatusCode, Header: res.Header, Raw: string(raw)}
	var env httpx.ErrorBody
	if json.Unmarshal(raw, &env) == nil {
		out.Error = env.Error
	}
	return out
}

func (b *browser) post(path string, body any) response { return b.do("POST", path, body) }
func (b *browser) get(path string) response            { return b.do("GET", path, nil) }

func (b *browser) mustPost(want int, path string, body any) response {
	b.app.t.Helper()
	res := b.post(path, body)
	if res.Status != want {
		b.app.t.Fatalf("POST %s: status = %d, want %d, body = %s", path, res.Status, want, res.Raw)
	}
	return res
}

// lastToken pulls the token out of the newest email sent to an address.
func (a *testApp) lastToken(to, pathPrefix string) string {
	a.t.Helper()
	sent := a.mailer.Sent()
	for i := len(sent) - 1; i >= 0; i-- {
		if sent[i].To != to {
			continue
		}
		idx := strings.Index(sent[i].Text, testOrigin+pathPrefix+"?token=")
		if idx < 0 {
			continue
		}
		link := strings.Fields(sent[i].Text[idx:])[0]
		u, err := url.Parse(link)
		if err != nil {
			a.t.Fatal(err)
		}
		return u.Query().Get("token")
	}
	a.t.Fatalf("no %s email found for %s", pathPrefix, to)
	return ""
}

func (a *testApp) mailCount(to string) int {
	n := 0
	for _, m := range a.mailer.Sent() {
		if m.To == to {
			n++
		}
	}
	return n
}

type creds struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func TestSignupVerifyLoginLogout(t *testing.T) {
	app := newTestApp(t)
	b := app.newBrowser()
	c := creds{"alex@example.com", "a-good-password"}

	b.mustPost(202, "/api/auth/signup", c)
	if res := b.get("/api/auth/me"); res.Status != 401 || res.Error.Code != "unauthenticated" {
		t.Fatalf("me before login: %d %s", res.Status, res.Raw)
	}

	// An unverified user can log in and sees email_verified = false.
	res := b.mustPost(200, "/api/auth/login", c)
	if !strings.Contains(res.Raw, `"email_verified":false`) {
		t.Errorf("login body: %s", res.Raw)
	}
	if strings.Contains(res.Raw, "password") || strings.Contains(res.Raw, "argon2") {
		t.Errorf("login response leaks password data: %s", res.Raw)
	}
	cookie := res.Header.Get("Set-Cookie")
	if !strings.Contains(cookie, "zyn_session=") || !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Lax") {
		t.Errorf("session cookie flags: %q", cookie)
	}

	token := app.lastToken(c.Email, "/verify-email")
	b.mustPost(200, "/api/auth/verify-email", map[string]string{"token": token})
	if res := b.get("/api/auth/me"); res.Status != 200 || !strings.Contains(res.Raw, `"email_verified":true`) {
		t.Fatalf("me after verify: %d %s", res.Status, res.Raw)
	}

	// The link works only once.
	if res := b.post("/api/auth/verify-email", map[string]string{"token": token}); res.Status != 422 || res.Error.Code != "invalid_token" {
		t.Errorf("token reuse: %d %s", res.Status, res.Raw)
	}

	b.mustPost(200, "/api/auth/logout", nil)
	if res := b.get("/api/auth/me"); res.Status != 401 {
		t.Errorf("me after logout: %d", res.Status)
	}
}

func TestOnlyTheTokenHashIsStored(t *testing.T) {
	app := newTestApp(t)
	b := app.newBrowser()
	c := creds{"alex@example.com", "a-good-password"}
	b.mustPost(202, "/api/auth/signup", c)
	b.mustPost(200, "/api/auth/login", c)

	u, _ := url.Parse(app.srv.URL)
	var cookieValue string
	for _, ck := range b.client.Jar.Cookies(u) {
		if ck.Name == "zyn_session" {
			cookieValue = ck.Value
		}
	}
	if cookieValue == "" {
		t.Fatal("no session cookie in the jar")
	}
	var stored []byte
	if err := app.pool.QueryRow(context.Background(), `SELECT token_hash FROM sessions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, auth.HashToken(cookieValue)) {
		t.Error("stored value is not the SHA-256 of the cookie")
	}
	if bytes.Contains(stored, []byte(cookieValue)) {
		t.Error("the cookie value itself is stored")
	}
}

func TestLoginFailuresLookTheSame(t *testing.T) {
	app := newTestApp(t)
	b := app.newBrowser()
	b.mustPost(202, "/api/auth/signup", creds{"alex@example.com", "a-good-password"})

	wrongPassword := b.post("/api/auth/login", creds{"alex@example.com", "not-the-password"})
	unknownEmail := b.post("/api/auth/login", creds{"nobody@example.com", "not-the-password"})
	if wrongPassword.Status != 401 || unknownEmail.Status != 401 {
		t.Fatalf("statuses: %d, %d", wrongPassword.Status, unknownEmail.Status)
	}
	if wrongPassword.Raw != unknownEmail.Raw {
		t.Errorf("responses differ:\n%s\n%s", wrongPassword.Raw, unknownEmail.Raw)
	}
}

func TestSignupDoesNotRevealExistingAccounts(t *testing.T) {
	app := newTestApp(t)
	b := app.newBrowser()

	first := b.mustPost(202, "/api/auth/signup", creds{"alex@example.com", "a-good-password"})
	// Same address again, different capitalisation and another password.
	second := b.mustPost(202, "/api/auth/signup", creds{"Alex@Example.com", "another-password"})
	if first.Raw != second.Raw {
		t.Errorf("responses differ:\n%s\n%s", first.Raw, second.Raw)
	}
	if n := app.mailCount("alex@example.com") + app.mailCount("Alex@Example.com"); n != 1 {
		t.Errorf("emails sent = %d, want 1", n)
	}
	// The first password still works; the second signup changed nothing.
	b.mustPost(200, "/api/auth/login", creds{"alex@example.com", "a-good-password"})
	if res := app.newBrowser().post("/api/auth/login", creds{"alex@example.com", "another-password"}); res.Status != 401 {
		t.Errorf("second signup's password works: %d", res.Status)
	}
}

func TestForgotPasswordDoesNotRevealAccounts(t *testing.T) {
	app := newTestApp(t)
	b := app.newBrowser()
	b.mustPost(202, "/api/auth/signup", creds{"alex@example.com", "a-good-password"})
	app.mailer.Reset()

	known := b.mustPost(202, "/api/auth/forgot-password", map[string]string{"email": "alex@example.com"})
	unknown := b.mustPost(202, "/api/auth/forgot-password", map[string]string{"email": "nobody@example.com"})
	if known.Raw != unknown.Raw {
		t.Errorf("responses differ:\n%s\n%s", known.Raw, unknown.Raw)
	}
	if app.mailCount("alex@example.com") != 1 || app.mailCount("nobody@example.com") != 0 {
		t.Errorf("unexpected emails: %+v", app.mailer.Sent())
	}
}

func TestPasswordResetFlow(t *testing.T) {
	app := newTestApp(t)
	oldPhone, laptop := app.newBrowser(), app.newBrowser()
	c := creds{"alex@example.com", "a-good-password"}
	laptop.mustPost(202, "/api/auth/signup", c)
	laptop.mustPost(200, "/api/auth/login", c)
	oldPhone.mustPost(200, "/api/auth/login", c)

	laptop.mustPost(202, "/api/auth/forgot-password", map[string]string{"email": c.Email})
	token := app.lastToken(c.Email, "/reset-password")

	// A too-short new password is refused and does not use up the link.
	if res := laptop.post("/api/auth/reset-password", map[string]string{"token": token, "password": "short"}); res.Status != 422 || res.Error.Code != "validation_failed" {
		t.Fatalf("short password: %d %s", res.Status, res.Raw)
	}
	laptop.mustPost(200, "/api/auth/reset-password", map[string]string{"token": token, "password": "a-brand-new-password"})

	// Every session is gone, on every device.
	for name, br := range map[string]*browser{"laptop": laptop, "old phone": oldPhone} {
		if res := br.get("/api/auth/me"); res.Status != 401 {
			t.Errorf("%s still logged in after reset: %d", name, res.Status)
		}
	}
	if res := laptop.post("/api/auth/login", c); res.Status != 401 {
		t.Errorf("old password still works: %d", res.Status)
	}
	laptop.mustPost(200, "/api/auth/login", creds{c.Email, "a-brand-new-password"})

	// The link works only once.
	if res := laptop.post("/api/auth/reset-password", map[string]string{"token": token, "password": "yet-another-password"}); res.Status != 422 || res.Error.Code != "invalid_token" {
		t.Errorf("reset link reuse: %d %s", res.Status, res.Raw)
	}
}

func TestVerifyTokenCannotResetPassword(t *testing.T) {
	app := newTestApp(t)
	b := app.newBrowser()
	c := creds{"alex@example.com", "a-good-password"}
	b.mustPost(202, "/api/auth/signup", c)
	verifyToken := app.lastToken(c.Email, "/verify-email")

	res := b.post("/api/auth/reset-password", map[string]string{"token": verifyToken, "password": "a-brand-new-password"})
	if res.Status != 422 || res.Error.Code != "invalid_token" {
		t.Fatalf("verify token accepted for reset: %d %s", res.Status, res.Raw)
	}
	// And it was not used up by the failed attempt.
	b.mustPost(200, "/api/auth/verify-email", map[string]string{"token": verifyToken})
}

func TestExpiredTokenIsRefused(t *testing.T) {
	app := newTestApp(t)
	b := app.newBrowser()
	c := creds{"alex@example.com", "a-good-password"}
	b.mustPost(202, "/api/auth/signup", c)
	token := app.lastToken(c.Email, "/verify-email")
	if _, err := app.pool.Exec(context.Background(), `UPDATE email_tokens SET expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	if res := b.post("/api/auth/verify-email", map[string]string{"token": token}); res.Status != 422 {
		t.Errorf("expired token: %d %s", res.Status, res.Raw)
	}
}

func TestExpiredAndDisabledSessions(t *testing.T) {
	app := newTestApp(t)
	b := app.newBrowser()
	c := creds{"alex@example.com", "a-good-password"}
	b.mustPost(202, "/api/auth/signup", c)
	b.mustPost(200, "/api/auth/login", c)
	ctx := context.Background()

	if _, err := app.pool.Exec(ctx, `UPDATE users SET disabled_at = now()`); err != nil {
		t.Fatal(err)
	}
	if res := b.get("/api/auth/me"); res.Status != 401 {
		t.Errorf("disabled user still has a session: %d", res.Status)
	}
	if res := app.newBrowser().post("/api/auth/login", c); res.Status != 401 {
		t.Errorf("disabled user can log in: %d", res.Status)
	}

	if _, err := app.pool.Exec(ctx, `UPDATE users SET disabled_at = NULL`); err != nil {
		t.Fatal(err)
	}
	b2 := app.newBrowser()
	b2.mustPost(200, "/api/auth/login", c)
	if _, err := app.pool.Exec(ctx, `UPDATE sessions SET expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	if res := b2.get("/api/auth/me"); res.Status != 401 {
		t.Errorf("expired session accepted: %d", res.Status)
	}
}

func TestLoginRotatesTheSession(t *testing.T) {
	app := newTestApp(t)
	b := app.newBrowser()
	c := creds{"alex@example.com", "a-good-password"}
	b.mustPost(202, "/api/auth/signup", c)
	b.mustPost(200, "/api/auth/login", c)
	b.mustPost(200, "/api/auth/login", c)

	var n int
	if err := app.pool.QueryRow(context.Background(), `SELECT count(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("sessions after logging in twice from one browser = %d, want 1", n)
	}
}

func TestWrongOriginIsRejected(t *testing.T) {
	app := newTestApp(t)
	b := app.newBrowser()
	c := creds{"alex@example.com", "a-good-password"}
	b.mustPost(202, "/api/auth/signup", c)
	b.mustPost(200, "/api/auth/login", c)

	// A logged-in browser is tricked into posting from another site.
	b.origin = "https://evil.example"
	if res := b.post("/api/auth/logout", nil); res.Status != 403 || res.Error.Code != "forbidden" {
		t.Fatalf("cross-site POST: %d %s", res.Status, res.Raw)
	}
	b.origin = ""
	if res := b.post("/api/auth/logout", nil); res.Status != 403 {
		t.Fatalf("POST without Origin: %d", res.Status)
	}
	b.origin = testOrigin
	if res := b.get("/api/auth/me"); res.Status != 200 {
		t.Errorf("session did not survive the rejected requests: %d", res.Status)
	}
}

func TestValidation(t *testing.T) {
	app := newTestApp(t)
	b := app.newBrowser()

	res := b.post("/api/auth/signup", creds{"not-an-email", "short"})
	if res.Status != 422 || res.Error.Code != "validation_failed" {
		t.Fatalf("status = %d, body = %s", res.Status, res.Raw)
	}
	fields, _ := res.Error.Details["fields"].(map[string]any)
	if fields["email"] == nil || fields["password"] == nil {
		t.Errorf("field errors = %v", fields)
	}

	for _, bad := range []string{"a@b", "Alex <alex@example.com>", "alex@example.com\r\nBcc: x@y.z", "a b@example.com", strings.Repeat("a", 250) + "@example.com"} {
		if res := b.post("/api/auth/signup", creds{bad, "a-good-password"}); res.Status != 422 {
			t.Errorf("email %q accepted: %d", bad, res.Status)
		}
	}

	// Not JSON.
	req, _ := http.NewRequest("POST", app.srv.URL+"/api/auth/login", strings.NewReader("email=a&password=b"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testOrigin)
	raw, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw.Body.Close()
	if raw.StatusCode != 415 {
		t.Errorf("form post: status = %d", raw.StatusCode)
	}

	// Unknown fields are refused rather than silently ignored.
	if res := b.post("/api/auth/login", map[string]string{"email": "a@example.com", "password": "x", "role": "admin"}); res.Status != 400 {
		t.Errorf("unknown field: status = %d", res.Status)
	}
}

func TestLoginRateLimit(t *testing.T) {
	app := newTestApp(t)
	b := app.newBrowser()
	c := creds{"alex@example.com", "a-good-password"}
	b.mustPost(202, "/api/auth/signup", c)

	for i := 1; i <= auth.RuleLoginPerIPEmail.Max; i++ {
		if res := b.post("/api/auth/login", creds{c.Email, "wrong-password"}); res.Status != 401 {
			t.Fatalf("attempt %d: status = %d", i, res.Status)
		}
	}
	// The next attempt is blocked, even with the right password.
	res := b.post("/api/auth/login", c)
	if res.Status != 429 || res.Error.Code != "rate_limited" {
		t.Fatalf("status = %d, body = %s", res.Status, res.Raw)
	}
	if res.Header.Get("Retry-After") == "" {
		t.Error("no Retry-After header")
	}
	// The limit is per address: another account from the same IP still works.
	other := creds{"sam@example.com", "a-good-password"}
	b.mustPost(202, "/api/auth/signup", other)
	b.mustPost(200, "/api/auth/login", other)
}

func TestSignupAndForgotRateLimits(t *testing.T) {
	app := newTestApp(t)
	b := app.newBrowser()

	for i := 1; i <= auth.RuleSignupPerIP.Max; i++ {
		b.mustPost(202, "/api/auth/signup", creds{"user" + string(rune('a'+i)) + "@example.com", "a-good-password"})
	}
	if res := b.post("/api/auth/signup", creds{"onemore@example.com", "a-good-password"}); res.Status != 429 {
		t.Errorf("signup over the limit: %d", res.Status)
	}

	for i := 1; i <= auth.RuleForgotPerEmail.Max; i++ {
		b.mustPost(202, "/api/auth/forgot-password", map[string]string{"email": "userb@example.com"})
	}
	if res := b.post("/api/auth/forgot-password", map[string]string{"email": "userb@example.com"}); res.Status != 429 {
		t.Errorf("forgot-password over the limit: %d", res.Status)
	}
}

func TestResendVerification(t *testing.T) {
	app := newTestApp(t)
	b := app.newBrowser()
	c := creds{"alex@example.com", "a-good-password"}

	if res := b.post("/api/auth/resend-verification", nil); res.Status != 401 {
		t.Fatalf("resend without login: %d", res.Status)
	}
	b.mustPost(202, "/api/auth/signup", c)
	b.mustPost(200, "/api/auth/login", c)
	first := app.lastToken(c.Email, "/verify-email")

	b.mustPost(202, "/api/auth/resend-verification", nil)
	second := app.lastToken(c.Email, "/verify-email")
	if first == second {
		t.Fatal("resend did not issue a new token")
	}
	// The older link stops working once a newer one exists.
	if res := b.post("/api/auth/verify-email", map[string]string{"token": first}); res.Status != 422 {
		t.Errorf("superseded token accepted: %d", res.Status)
	}
	b.mustPost(200, "/api/auth/verify-email", map[string]string{"token": second})

	res := b.mustPost(200, "/api/auth/resend-verification", nil)
	if !strings.Contains(res.Raw, "already_verified") {
		t.Errorf("resend for a verified user: %s", res.Raw)
	}
}
