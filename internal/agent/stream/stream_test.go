package stream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"zynthera/internal/agent/driver/mock"
	"zynthera/internal/shared/logging"
	"zynthera/internal/shared/token"
	"zynthera/internal/shared/vmtypes"
)

const (
	origin   = "http://localhost:3000"
	vmA      = "11111111-1111-4111-8111-111111111111"
	vmB      = "22222222-2222-4222-8222-222222222222"
	testHash = "$6$zyntheratestsalt$" +
		"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789./abcdefghijklmnopqrstuv"
)

var secret = []byte("stream-secret")

// fakeVNC is a TCP server that greets like a VNC server and then echoes
// everything it receives, upper-cased, so both directions can be checked.
type fakeVNC struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func newFakeVNC(t *testing.T) *fakeVNC {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeVNC{ln: ln}
	t.Cleanup(func() { _ = ln.Close(); f.closeAll() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conns = append(f.conns, c)
			f.mu.Unlock()
			go func() {
				defer c.Close()
				_, _ = c.Write([]byte("RFB 003.008\n"))
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						_, _ = c.Write(bytes.ToUpper(buf[:n]))
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return f
}

func (f *fakeVNC) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conns {
		_ = c.Close()
	}
}

type harness struct {
	t      *testing.T
	proxy  *Proxy
	srv    *httptest.Server
	drv    *mock.Driver
	vnc    *fakeVNC
	mu     sync.Mutex
	events []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	vnc := newFakeVNC(t)
	drv, err := mock.New(mock.Options{DataDir: t.TempDir(), VNCAddr: vnc.ln.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, drv: drv, vnc: vnc}
	h.proxy = New(secret, origin, drv, logging.NewTo(io.Discard, "test"), func(vmID, event string) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.events = append(h.events, vmID[:1]+":"+event)
	})
	h.srv = httptest.NewServer(h.proxy)
	t.Cleanup(h.srv.Close)

	ctx := context.Background()
	for _, id := range []string{vmA, vmB} {
		spec := vmtypes.VMSpec{
			ID: id, ImageSlug: "ubuntu-24.04-xfce", ImageVersion: "2026.11.1", RAMMB: 2048, VCPUs: 1, DiskGB: 40,
			Hostname: "zynthera-test", Username: "tester", PasswordHash: testHash, BridgeToken: strings.Repeat("a", 40),
		}
		if err := drv.Create(ctx, spec); err != nil {
			t.Fatal(err)
		}
	}
	if err := drv.Start(ctx, vmA); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) token(vmID, purpose string, ttl time.Duration) string {
	h.t.Helper()
	tok, _, err := token.Sign(secret, vmID, "user-1", purpose, ttl, time.Now())
	if err != nil {
		h.t.Fatal(err)
	}
	return tok
}

func (h *harness) dial(tok, fromOrigin string) *websocket.Conn {
	h.t.Helper()
	header := http.Header{}
	if fromOrigin != "" {
		header.Set("Origin", fromOrigin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(h.srv.URL, "http") + "/stream?token=" + tok
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header, Subprotocols: []string{"binary"}})
	if err != nil {
		h.t.Fatalf("dial: %v", err)
	}
	h.t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

// closeCode reads until the connection closes and returns the close code.
func closeCode(t *testing.T, conn *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, _, err := conn.Read(ctx)
		if err == nil {
			continue
		}
		var ce websocket.CloseError
		if errors.As(err, &ce) {
			return ce.Code
		}
		t.Fatalf("connection ended without a close frame: %v", err)
	}
}

func readExactly(t *testing.T, conn *websocket.Conn, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got []byte
	for len(got) < len(want) {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v (got %q so far)", err, got)
		}
		if typ != websocket.MessageBinary {
			t.Fatalf("message type = %v, want binary", typ)
		}
		got = append(got, data...)
	}
	if string(got) != want {
		t.Fatalf("read %q, want %q", got, want)
	}
}

func (h *harness) waitEvents(want ...string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		got := strings.Join(h.events, ",")
		h.mu.Unlock()
		if got == strings.Join(want, ",") {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("events = [%s], want [%s]", got, strings.Join(want, ","))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBytesFlowBothWays(t *testing.T) {
	h := newHarness(t)
	conn := h.dial(h.token(vmA, token.PurposeStream, time.Minute), origin)
	if conn.Subprotocol() != "binary" {
		t.Errorf("subprotocol = %q", conn.Subprotocol())
	}
	readExactly(t, conn, "RFB 003.008\n")
	if err := conn.Write(context.Background(), websocket.MessageBinary, []byte("hello vm")); err != nil {
		t.Fatal(err)
	}
	readExactly(t, conn, "HELLO VM")

	h.waitEvents("1:connected")
	if !h.proxy.ViewerConnected(vmA) || h.proxy.ViewerConnected(vmB) {
		t.Error("ViewerConnected is wrong while connected")
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
	h.waitEvents("1:connected", "1:disconnected")
	if h.proxy.ViewerConnected(vmA) || h.proxy.Count() != 0 {
		t.Error("viewer still registered after disconnect")
	}
}

func TestTokenChecks(t *testing.T) {
	h := newHarness(t)
	other, _, _ := token.Sign([]byte("another-secret"), vmA, "user-1", token.PurposeStream, time.Minute, time.Now())
	cases := map[string]string{
		"no token":         "",
		"garbage":          "not-a-token",
		"expired":          h.token(vmA, token.PurposeStream, -time.Second),
		"files purpose":    h.token(vmA, token.PurposeFiles, time.Minute),
		"wrong secret":     other,
		"vm id not a uuid": h.token("../../etc/passwd", token.PurposeStream, time.Minute),
	}
	for name, tok := range cases {
		if code := closeCode(t, h.dial(tok, origin)); code != CloseBadToken {
			t.Errorf("%s: close code = %d, want %d", name, code, CloseBadToken)
		}
	}
	h.waitEvents() // nobody ever counted as connected
}

func TestTokenIsSingleUse(t *testing.T) {
	h := newHarness(t)
	tok := h.token(vmA, token.PurposeStream, time.Minute)
	first := h.dial(tok, origin)
	readExactly(t, first, "RFB 003.008\n")
	if code := closeCode(t, h.dial(tok, origin)); code != CloseBadToken {
		t.Fatalf("second use: close code = %d, want %d", code, CloseBadToken)
	}
	// The first connection is not disturbed by the replay attempt.
	if err := first.Write(context.Background(), websocket.MessageBinary, []byte("still here")); err != nil {
		t.Fatal(err)
	}
	readExactly(t, first, "STILL HERE")
}

func TestOriginCheck(t *testing.T) {
	h := newHarness(t)
	for name, from := range map[string]string{"another site": "https://evil.example", "no origin": "", "same host, other port": "http://localhost:3001"} {
		tok := h.token(vmA, token.PurposeStream, time.Minute)
		if code := closeCode(t, h.dial(tok, from)); code != CloseBadOrigin {
			t.Errorf("%s: close code = %d, want %d", name, code, CloseBadOrigin)
		}
		// A refused origin must not use the token up or reach the VM.
		conn := h.dial(tok, origin)
		readExactly(t, conn, "RFB 003.008\n")
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
}

func TestVMNotRunning(t *testing.T) {
	h := newHarness(t)
	// vmB exists but was never started.
	if code := closeCode(t, h.dial(h.token(vmB, token.PurposeStream, time.Minute), origin)); code != CloseNotRunning {
		t.Errorf("stopped vm: close code = %d, want %d", code, CloseNotRunning)
	}
	unknown := "99999999-9999-4999-8999-999999999999"
	if code := closeCode(t, h.dial(h.token(unknown, token.PurposeStream, time.Minute), origin)); code != CloseNotRunning {
		t.Errorf("unknown vm: close code = %d, want %d", code, CloseNotRunning)
	}
}

func TestSecondViewerTakesOver(t *testing.T) {
	h := newHarness(t)
	first := h.dial(h.token(vmA, token.PurposeStream, time.Minute), origin)
	readExactly(t, first, "RFB 003.008\n")
	h.waitEvents("1:connected")

	second := h.dial(h.token(vmA, token.PurposeStream, time.Minute), origin)
	readExactly(t, second, "RFB 003.008\n")
	if code := closeCode(t, first); code != CloseReplaced {
		t.Fatalf("first viewer: close code = %d, want %d", code, CloseReplaced)
	}
	// The new viewer works, and the VM never counted as without a viewer.
	if err := second.Write(context.Background(), websocket.MessageBinary, []byte("mine now")); err != nil {
		t.Fatal(err)
	}
	readExactly(t, second, "MINE NOW")
	if !h.proxy.ViewerConnected(vmA) || h.proxy.Count() != 1 {
		t.Error("viewer bookkeeping is wrong after a takeover")
	}
	h.waitEvents("1:connected", "1:connected")
	_ = second.Close(websocket.StatusNormalClosure, "")
	h.waitEvents("1:connected", "1:connected", "1:disconnected")
}

func TestStreamClosesWhenTheVMStops(t *testing.T) {
	h := newHarness(t)
	conn := h.dial(h.token(vmA, token.PurposeStream, time.Minute), origin)
	readExactly(t, conn, "RFB 003.008\n")

	h.proxy.CloseVM(vmA)
	if code := closeCode(t, conn); code != CloseNotRunning {
		t.Fatalf("close code = %d, want %d", code, CloseNotRunning)
	}
	h.waitEvents("1:connected", "1:disconnected")
	h.proxy.CloseVM(vmA) // no viewer: must be harmless
}

func TestStreamClosesWhenTheDisplayGoesAway(t *testing.T) {
	h := newHarness(t)
	conn := h.dial(h.token(vmA, token.PurposeStream, time.Minute), origin)
	readExactly(t, conn, "RFB 003.008\n")
	h.vnc.closeAll() // QEMU exited
	if code := closeCode(t, conn); code != CloseNotRunning {
		t.Fatalf("close code = %d, want %d", code, CloseNotRunning)
	}
	h.waitEvents("1:connected", "1:disconnected")
}

func TestPlainHTTPRequestIsRefused(t *testing.T) {
	h := newHarness(t)
	res, err := http.Get(h.srv.URL + "/stream?token=x")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode < 400 {
		t.Errorf("plain GET: status = %d", res.StatusCode)
	}
}
