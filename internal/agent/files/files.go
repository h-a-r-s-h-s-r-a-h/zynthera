// Package files forwards file requests from the browser to the file bridge
// inside a VM (spec section 11.8). The browser never talks to a VM directly:
// it presents a signed "files" token to the agent, and the agent adds the
// VM's own bridge token when it forwards the request.
package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"zynthera/internal/agent/driver"
	"zynthera/internal/shared/httpx"
	"zynthera/internal/shared/token"
	"zynthera/internal/shared/vmtypes"
)

// Resolver finds the file bridge of a VM.
type Resolver interface {
	// FileBridge returns the base URL of the VM's bridge (for example
	// http://10.210.0.12:7070) and the token it expects.
	FileBridge(ctx context.Context, vmID string) (baseURL, bridgeToken string, err error)
}

// Proxy serves the /files/ routes.
type Proxy struct {
	Secret    []byte // verifies the browser's token
	WebOrigin string // the only origin allowed to call
	Driver    driver.Driver
	Resolver  Resolver
	Logger    *slog.Logger
	// MaxUploadBytes aborts an upload that grows beyond it.
	MaxUploadBytes int64
	Now            func() time.Time

	client *http.Client
}

// New returns a proxy.
func New(secret []byte, webOrigin string, drv driver.Driver, resolver Resolver, log *slog.Logger, maxUploadBytes int64) *Proxy {
	return &Proxy{
		Secret: secret, WebOrigin: webOrigin, Driver: drv, Resolver: resolver, Logger: log, MaxUploadBytes: maxUploadBytes,
		client: &http.Client{
			// No overall timeout: a large file takes as long as it takes.
			// The connection and the first response byte are bounded.
			Transport: &http.Transport{
				Proxy:                 nil,
				DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
				ResponseHeaderTimeout: 30 * time.Second,
				MaxIdleConnsPerHost:   4,
				IdleConnTimeout:       60 * time.Second,
			},
			// The bridge never redirects; following one could send the
			// bridge token somewhere else.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (p *Proxy) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Handler returns the handler for everything under /files/.
func (p *Proxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /files/list", p.cors(p.handleList))
	mux.HandleFunc("PUT /files/upload", p.cors(p.handleUpload))
	mux.HandleFunc("GET /files/download", p.handleDownload)
	mux.HandleFunc("OPTIONS /files/", p.preflight)
	return mux
}

func (p *Proxy) setCORS(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", p.WebOrigin)
	h.Set("Vary", "Origin")
}

// preflight answers the browser's CORS check before a cross-origin request.
func (p *Proxy) preflight(w http.ResponseWriter, _ *http.Request) {
	p.setCORS(w)
	h := w.Header()
	h.Set("Access-Control-Allow-Methods", "GET, PUT, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	h.Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

// cors allows the web app, and only the web app, to call from a browser.
func (p *Proxy) cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p.setCORS(w)
		// Browsers always send Origin on these cross-origin calls. A
		// different origin is refused even with a valid token.
		if origin := r.Header.Get("Origin"); origin != p.WebOrigin {
			httpx.WriteError(w, http.StatusForbidden, "forbidden", "This request did not come from the Zynthera web app.", nil)
			return
		}
		next(w, r)
	}
}

// authorize verifies the files token and that it names a running VM on this
// host. It writes the error response itself.
func (p *Proxy) authorize(w http.ResponseWriter, r *http.Request, tok string) (baseURL, bridgeToken string, ok bool) {
	claims, err := token.Verify(p.Secret, tok, token.PurposeFiles, p.now())
	if err != nil || !vmtypes.IsUUID(claims.VMID) {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated", "The file access token is invalid or has expired.", nil)
		return "", "", false
	}
	st, err := p.Driver.Status(r.Context(), claims.VMID)
	if err != nil {
		p.Logger.Error("files: status lookup failed", "vm_id", claims.VMID, "error", err.Error())
		httpx.Internal(w)
		return "", "", false
	}
	if st.Status != vmtypes.StatusRunning {
		httpx.WriteError(w, http.StatusConflict, "vm_not_running", "The PC is not running.", nil)
		return "", "", false
	}
	baseURL, bridgeToken, err = p.Resolver.FileBridge(r.Context(), claims.VMID)
	if err != nil {
		p.Logger.Error("files: no bridge for vm", "vm_id", claims.VMID, "error", err.Error())
		httpx.WriteError(w, http.StatusBadGateway, "bridge_unavailable", "File transfer is not available for this PC right now.", nil)
		return "", "", false
	}
	return baseURL, bridgeToken, true
}

func bearer(r *http.Request) string {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return tok
}

// forward sends one request to the bridge and streams the answer back. The
// body is never held in memory.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, method, target, bridgeToken string, body io.Reader, contentLength int64, passHeaders ...string) {
	req, err := http.NewRequestWithContext(r.Context(), method, target, body)
	if err != nil {
		httpx.Internal(w)
		return
	}
	req.Header.Set("Authorization", "Bearer "+bridgeToken)
	if contentLength >= 0 && body != nil {
		req.ContentLength = contentLength
	}
	// Range lets a browser resume an interrupted download.
	if rng := r.Header.Get("Range"); rng != "" && method == http.MethodGet {
		req.Header.Set("Range", rng)
	}

	res, err := p.client.Do(req)
	if err != nil {
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			httpx.WriteError(w, http.StatusRequestEntityTooLarge, "too_large", "The file is larger than the upload limit.", nil)
		case r.Context().Err() != nil:
			// The browser went away; nobody is left to answer.
		default:
			p.Logger.Warn("files: bridge request failed", "error", err.Error())
			httpx.WriteError(w, http.StatusBadGateway, "bridge_unavailable",
				"File transfer is not available for this PC right now. It may still be starting.", nil)
		}
		return
	}
	defer func() { _ = res.Body.Close() }()

	h := w.Header()
	for _, name := range append([]string{"Content-Type", "Content-Length"}, passHeaders...) {
		if v := res.Header.Get(name); v != "" {
			h.Set(name, v)
		}
	}
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, res.Body)
}

func bridgeURL(base, path string, query url.Values) string {
	return strings.TrimRight(base, "/") + path + "?" + query.Encode()
}

// GET /files/list?path=...[&hidden=1]
func (p *Proxy) handleList(w http.ResponseWriter, r *http.Request) {
	base, bridgeToken, ok := p.authorize(w, r, bearer(r))
	if !ok {
		return
	}
	q := url.Values{"path": {r.URL.Query().Get("path")}}
	if r.URL.Query().Get("hidden") == "1" {
		q.Set("hidden", "1")
	}
	p.forward(w, r, http.MethodGet, bridgeURL(base, "/v1/list", q), bridgeToken, nil, -1)
}

// PUT /files/upload?name=...   (the body is the file)
func (p *Proxy) handleUpload(w http.ResponseWriter, r *http.Request) {
	base, bridgeToken, ok := p.authorize(w, r, bearer(r))
	if !ok {
		return
	}
	if p.MaxUploadBytes > 0 && r.ContentLength > p.MaxUploadBytes {
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "too_large", "The file is larger than the upload limit.", nil)
		return
	}
	body := r.Body
	if p.MaxUploadBytes > 0 {
		// Enforced while streaming too: a client can lie about the length
		// or send none.
		body = http.MaxBytesReader(w, r.Body, p.MaxUploadBytes)
	}
	q := url.Values{"name": {r.URL.Query().Get("name")}}
	p.forward(w, r, http.MethodPut, bridgeURL(base, "/v1/upload", q), bridgeToken, body, r.ContentLength)
}

// GET /files/download?path=...&token=...
//
// Opened by the browser as a normal download, so the token travels in the
// query string. That is why the access log never records query strings.
func (p *Proxy) handleDownload(w http.ResponseWriter, r *http.Request) {
	base, bridgeToken, ok := p.authorize(w, r, r.URL.Query().Get("token"))
	if !ok {
		return
	}
	// Keeps the token out of the Referer of anything the download triggers.
	w.Header().Set("Referrer-Policy", "no-referrer")
	q := url.Values{"path": {r.URL.Query().Get("path")}}
	p.forward(w, r, http.MethodGet, bridgeURL(base, "/v1/download", q), bridgeToken, nil, -1,
		"Content-Disposition", "Content-Range", "Accept-Ranges", "Last-Modified")
}

// ErrNoBridge is returned by resolvers for a VM without a reachable bridge.
var ErrNoBridge = errors.New("files: no file bridge for this vm")

// Static is a Resolver built from a function, for drivers and tests.
type Static func(ctx context.Context, vmID string) (baseURL, bridgeToken string, err error)

// FileBridge implements Resolver.
func (f Static) FileBridge(ctx context.Context, vmID string) (string, string, error) {
	return f(ctx, vmID)
}

// GuestResolver builds the Resolver for real VMs: the bridge listens on the
// VM's private address.
func GuestResolver(port int, address func(ctx context.Context, vmID string) (string, error), bridgeToken func(vmID string) (string, error)) Resolver {
	return Static(func(ctx context.Context, vmID string) (string, string, error) {
		ip, err := address(ctx, vmID)
		if err != nil {
			return "", "", fmt.Errorf("%w: %w", ErrNoBridge, err)
		}
		tok, err := bridgeToken(vmID)
		if err != nil {
			return "", "", fmt.Errorf("%w: %w", ErrNoBridge, err)
		}
		return "http://" + net.JoinHostPort(ip, fmt.Sprint(port)), tok, nil
	})
}
