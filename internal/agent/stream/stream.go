// Package stream bridges a browser's WebSocket to a VM's VNC port. noVNC in
// the browser speaks the VNC protocol over the WebSocket; this proxy only
// copies bytes and never looks inside them (spec section 11.7).
package stream

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"zynthera/internal/agent/driver"
	"zynthera/internal/shared/token"
	"zynthera/internal/shared/vmtypes"
)

// Close codes the viewer understands.
const (
	CloseBadToken   websocket.StatusCode = 4401 // invalid, expired or already used token
	CloseBadOrigin  websocket.StatusCode = 4403 // request did not come from the web app
	CloseNotRunning websocket.StatusCode = 4404 // the VM is not running
	CloseReplaced   websocket.StatusCode = 4409 // a newer connection took over
)

const (
	pingInterval = 20 * time.Second
	dialTimeout  = 5 * time.Second
	// Messages from the browser are key and pointer events and clipboard
	// text. One megabyte is far more than any of them needs.
	maxClientMessage = 1 << 20
)

// Proxy serves GET /stream.
type Proxy struct {
	// Secret verifies the browser's token.
	Secret []byte
	// WebOrigin is the only Origin allowed to connect.
	WebOrigin string
	Driver    driver.Driver
	Logger    *slog.Logger
	// Notify tells the control plane that a viewer connected or left. It
	// must not block for long. Nil disables notifications.
	Notify func(vmID, event string)
	// Now is the clock; tests may replace it.
	Now func() time.Time

	used *token.UsedSet

	mu      sync.Mutex
	viewers map[string]*viewer
}

// viewer is one open connection.
type viewer struct {
	conn   *websocket.Conn
	cancel context.CancelFunc
}

// end closes the connection on purpose, telling the browser why. The close
// frame has to be sent before the context is cancelled: cancelling a read
// tears the connection down without a close code.
func (v *viewer) end(code websocket.StatusCode, reason string) {
	_ = v.conn.Close(code, reason)
	v.cancel()
}

// New returns a proxy.
func New(secret []byte, webOrigin string, drv driver.Driver, log *slog.Logger, notify func(vmID, event string)) *Proxy {
	return &Proxy{
		Secret: secret, WebOrigin: webOrigin, Driver: drv, Logger: log, Notify: notify,
		used: token.NewUsedSet(), viewers: map[string]*viewer{},
	}
}

func (p *Proxy) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// ViewerConnected reports whether a viewer is attached to the VM. The
// heartbeat includes it, so a lost notification corrects itself.
func (p *Proxy) ViewerConnected(vmID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.viewers[vmID] != nil
}

// Count returns the number of open streams.
func (p *Proxy) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.viewers)
}

// CloseVM ends the stream of a VM that stopped running. A real VM's VNC
// socket closes by itself when QEMU exits; calling this as well makes the
// behaviour the same for every driver.
func (p *Proxy) CloseVM(vmID string) {
	p.mu.Lock()
	v := p.viewers[vmID]
	p.mu.Unlock()
	if v != nil {
		go v.end(CloseNotRunning, "the PC is not running")
	}
}

func (p *Proxy) notify(vmID, event string) {
	if p.Notify != nil {
		p.Notify(vmID, event)
	}
}

// ServeHTTP handles one viewer connection from start to end.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The handshake is always accepted and problems are reported as
	// WebSocket close codes: a browser cannot read the HTTP status of a
	// refused upgrade, so the viewer could not tell the cases apart.
	// InsecureSkipVerify only turns off the library's own origin check;
	// ours follows immediately and is stricter.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{"binary"},
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled, // VNC data is already compressed
	})
	if err != nil {
		// Not a WebSocket request; Accept has already answered.
		return
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(maxClientMessage)

	if r.Header.Get("Origin") != p.WebOrigin {
		_ = conn.Close(CloseBadOrigin, "wrong origin")
		return
	}
	claims, err := token.Verify(p.Secret, r.URL.Query().Get("token"), token.PurposeStream, p.now())
	if err == nil {
		err = p.used.Use(claims, p.now())
	}
	if err != nil || !vmtypes.IsUUID(claims.VMID) {
		_ = conn.Close(CloseBadToken, "invalid or expired token")
		return
	}
	vmID := claims.VMID

	endpoint, err := p.Driver.Display(r.Context(), vmID)
	if err != nil {
		if !errors.Is(err, driver.ErrInvalidState) && !errors.Is(err, driver.ErrNotFound) {
			p.Logger.Error("stream: display lookup failed", "vm_id", vmID, "error", err.Error())
		}
		_ = conn.Close(CloseNotRunning, "the PC is not running")
		return
	}
	dialer := net.Dialer{Timeout: dialTimeout}
	vnc, err := dialer.DialContext(r.Context(), endpoint.Network, endpoint.Address)
	if err != nil {
		p.Logger.Error("stream: cannot reach the display", "vm_id", vmID, "error", err.Error())
		_ = conn.Close(CloseNotRunning, "the PC's display is not available")
		return
	}
	defer func() { _ = vnc.Close() }()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	me := &viewer{conn: conn, cancel: cancel}

	// One viewer per VM: a new connection takes over from the old one.
	p.mu.Lock()
	old := p.viewers[vmID]
	p.viewers[vmID] = me
	p.mu.Unlock()
	if old != nil {
		go old.end(CloseReplaced, "opened in another tab")
	}
	p.notify(vmID, "connected")
	p.Logger.Info("stream: viewer connected", "vm_id", vmID, "user_id", claims.UserID)

	p.pump(ctx, cancel, conn, vnc)

	p.mu.Lock()
	replaced := p.viewers[vmID] != me
	if !replaced {
		delete(p.viewers, vmID)
	}
	p.mu.Unlock()

	// Does nothing if the connection was already closed with a code.
	_ = conn.Close(websocket.StatusNormalClosure, "")
	// When this viewer was replaced, the VM still has a viewer.
	if !replaced {
		p.notify(vmID, "disconnected")
	}
	p.Logger.Info("stream: viewer disconnected", "vm_id", vmID)
}

// pump copies bytes in both directions until either side closes or ctx ends.
func (p *Proxy) pump(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, vnc net.Conn) {
	var wg sync.WaitGroup
	wg.Add(3)

	// Browser to VM.
	go func() {
		defer wg.Done()
		defer cancel()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if _, err := vnc.Write(data); err != nil {
				return
			}
		}
	}()

	// VM to browser.
	go func() {
		defer wg.Done()
		defer cancel()
		buf := make([]byte, 64*1024)
		for {
			n, err := vnc.Read(buf)
			if n > 0 {
				if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				if ctx.Err() == nil {
					if !errors.Is(err, io.EOF) {
						p.Logger.Warn("stream: display read failed", "error", err.Error())
					}
					// The display closed first: QEMU exited because the PC
					// was suspended or shut down. Say so before the context
					// is cancelled, or the browser gets no close code.
					_ = conn.Close(CloseNotRunning, "the PC is not running")
				}
				return
			}
		}
	}()

	// Keep-alive pings, which also detect a browser that vanished.
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pingCtx, stop := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Ping(pingCtx)
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	<-ctx.Done()
	// Unblock the goroutine that is waiting in vnc.Read.
	_ = vnc.SetDeadline(time.Now())
	wg.Wait()
}
