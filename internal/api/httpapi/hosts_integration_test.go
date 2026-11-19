package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"zynthera/internal/agent/agentconfig"
	"zynthera/internal/agent/driver/mock"
	"zynthera/internal/agent/heartbeat"
	agentserver "zynthera/internal/agent/server"
	"zynthera/internal/api/agentclient"
	"zynthera/internal/shared/logging"
	"zynthera/internal/shared/vmtypes"
)

// loginAs signs up, optionally promotes to admin, and logs in.
func (a *testApp) loginAs(email string, admin bool) *browser {
	a.t.Helper()
	b := a.newBrowser()
	c := creds{email, "a-good-password"}
	b.mustPost(202, "/api/auth/signup", c)
	if admin {
		if _, err := a.pool.Exec(context.Background(), `UPDATE users SET role = 'admin' WHERE email = $1`, email); err != nil {
			a.t.Fatal(err)
		}
	}
	b.mustPost(200, "/api/auth/login", c)
	return b
}

type createdHost struct {
	Host         hostJSON `json:"host"`
	AgentToken   string   `json:"agent_token"`
	StreamSecret string   `json:"stream_secret"`
}

func (a *testApp) createHost(admin *browser, name, agentURL string) createdHost {
	a.t.Helper()
	res := admin.mustPost(201, "/api/admin/hosts", map[string]string{
		"name": name, "agent_url": agentURL, "public_url": agentURL,
	})
	var out createdHost
	if err := json.Unmarshal([]byte(res.Raw), &out); err != nil {
		a.t.Fatal(err)
	}
	return out
}

func (a *testApp) listHosts(admin *browser) []hostJSON {
	a.t.Helper()
	res := admin.get("/api/admin/hosts")
	if res.Status != 200 {
		a.t.Fatalf("list hosts: %d %s", res.Status, res.Raw)
	}
	var out struct {
		Hosts []hostJSON `json:"hosts"`
	}
	if err := json.Unmarshal([]byte(res.Raw), &out); err != nil {
		a.t.Fatal(err)
	}
	return out.Hosts
}

// postHeartbeat sends a raw heartbeat like an agent would (no cookies, no Origin).
func (a *testApp) postHeartbeat(token string, hb vmtypes.Heartbeat) int {
	a.t.Helper()
	raw, _ := json.Marshal(hb)
	req, _ := http.NewRequest("POST", a.srv.URL+"/internal/agent/heartbeat", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestAdminRoutesAreForAdminsOnly(t *testing.T) {
	app := newTestApp(t)
	if res := app.newBrowser().get("/api/admin/hosts"); res.Status != 401 {
		t.Errorf("logged out: %d", res.Status)
	}
	user := app.loginAs("user@example.com", false)
	if res := user.get("/api/admin/hosts"); res.Status != 403 || res.Error.Code != "forbidden" {
		t.Errorf("normal user list: %d %s", res.Status, res.Raw)
	}
	body := map[string]string{"name": "h1", "agent_url": "http://127.0.0.1:9090", "public_url": "http://127.0.0.1:9090"}
	if res := user.post("/api/admin/hosts", body); res.Status != 403 {
		t.Errorf("normal user create: %d", res.Status)
	}
}

func TestCreateHostShowsSecretsOnce(t *testing.T) {
	app := newTestApp(t)
	admin := app.loginAs("admin@example.com", true)

	created := app.createHost(admin, "host-1", "https://host-1.example.com/")
	if len(created.AgentToken) != 43 || len(created.StreamSecret) != 43 || created.AgentToken == created.StreamSecret {
		t.Fatalf("secrets: %q %q", created.AgentToken, created.StreamSecret)
	}
	if created.Host.AgentURL != "https://host-1.example.com" {
		t.Errorf("trailing slash kept: %q", created.Host.AgentURL)
	}
	if created.Host.Online || created.Host.AdminState != "active" {
		t.Errorf("new host: %+v", created.Host)
	}

	// Never again: not in the list, and not in the database in clear text.
	listRaw := admin.get("/api/admin/hosts").Raw
	if strings.Contains(listRaw, created.AgentToken) || strings.Contains(listRaw, created.StreamSecret) || strings.Contains(listRaw, "token") {
		t.Errorf("list leaks secrets: %s", listRaw)
	}
	var tokenEnc, secretEnc []byte
	if err := app.pool.QueryRow(context.Background(), `SELECT agent_token_enc, stream_secret_enc FROM hosts`).Scan(&tokenEnc, &secretEnc); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(tokenEnc, []byte(created.AgentToken)) || bytes.Contains(secretEnc, []byte(created.StreamSecret)) {
		t.Error("secrets are stored unencrypted")
	}

	// Duplicate names and bad input are refused.
	dup := admin.post("/api/admin/hosts", map[string]string{"name": "host-1", "agent_url": "http://a.example", "public_url": "http://a.example"})
	if dup.Status != 422 {
		t.Errorf("duplicate name: %d %s", dup.Status, dup.Raw)
	}
	bad := admin.post("/api/admin/hosts", map[string]string{"name": "Bad Name!", "agent_url": "ftp://x", "public_url": "javascript:alert(1)"})
	fields, _ := bad.Error.Details["fields"].(map[string]any)
	if bad.Status != 422 || fields["name"] == nil || fields["agent_url"] == nil || fields["public_url"] == nil {
		t.Errorf("bad input: %d %s", bad.Status, bad.Raw)
	}
}

func TestHeartbeatMakesHostOnline(t *testing.T) {
	app := newTestApp(t)
	admin := app.loginAs("admin@example.com", true)
	created := app.createHost(admin, "host-1", "http://127.0.0.1:9090")
	other := app.createHost(admin, "host-2", "http://127.0.0.1:9091")

	hb := vmtypes.Heartbeat{
		HostID:       created.Host.ID.String(),
		AgentVersion: "0.1.0",
		Capacity:     vmtypes.Capacity{TotalRAMMB: 128000, TotalVCPUs: 32, TotalDiskGB: 1800, FreeDiskGB: 1400},
		Images:       []vmtypes.ImageRef{{Slug: "ubuntu-24.04-xfce", Version: "2026.11.1"}, {Slug: "../evil", Version: "1"}},
		VMs:          []vmtypes.VMStatus{},
	}

	for name, token := range map[string]string{"no token": "", "wrong token": "nope", "another host's token": other.AgentToken} {
		if got := app.postHeartbeat(token, hb); got != 401 {
			t.Errorf("heartbeat with %s: %d", name, got)
		}
	}
	unknown := hb
	unknown.HostID = "99999999-9999-4999-8999-999999999999"
	if got := app.postHeartbeat(created.AgentToken, unknown); got != 401 {
		t.Errorf("heartbeat for an unknown host: %d", got)
	}
	for _, h := range app.listHosts(admin) {
		if h.Online {
			t.Fatalf("host %s online after only rejected heartbeats", h.Name)
		}
	}

	if got := app.postHeartbeat(created.AgentToken, hb); got != 200 {
		t.Fatalf("valid heartbeat: %d", got)
	}
	var h1, h2 hostJSON
	for _, h := range app.listHosts(admin) {
		if h.Name == "host-1" {
			h1 = h
		} else {
			h2 = h
		}
	}
	if !h1.Online || h1.TotalRAMMB != 128000 || h1.TotalVCPUs != 32 || h1.FreeDiskGB != 1400 || h1.AgentVersion == nil {
		t.Errorf("host-1 after heartbeat: %+v", h1)
	}
	if len(h1.Images) != 1 || h1.Images[0].Slug != "ubuntu-24.04-xfce" {
		t.Errorf("images (the invalid one must be dropped): %+v", h1.Images)
	}
	if h2.Online {
		t.Error("host-2 became online from host-1's heartbeat")
	}

	// A host whose last heartbeat is old counts as offline.
	if _, err := app.pool.Exec(context.Background(), `UPDATE hosts SET last_heartbeat_at = now() - interval '46 seconds' WHERE name = 'host-1'`); err != nil {
		t.Fatal(err)
	}
	for _, h := range app.listHosts(admin) {
		if h.Name == "host-1" && h.Online {
			t.Error("host-1 still online 46 seconds after its last heartbeat")
		}
	}
}

func TestPatchHost(t *testing.T) {
	app := newTestApp(t)
	admin := app.loginAs("admin@example.com", true)
	created := app.createHost(admin, "host-1", "http://127.0.0.1:9090")
	path := "/api/admin/hosts/" + created.Host.ID.String()

	res := admin.do("PATCH", path, map[string]any{"admin_state": "draining", "cpu_overcommit": 2.5})
	if res.Status != 200 || !strings.Contains(res.Raw, `"admin_state":"draining"`) || !strings.Contains(res.Raw, `"cpu_overcommit":2.5`) {
		t.Fatalf("patch: %d %s", res.Status, res.Raw)
	}
	// Fields that were not sent keep their value.
	if !strings.Contains(res.Raw, `"reserved_ram_mb":4096`) {
		t.Errorf("reserved_ram_mb changed: %s", res.Raw)
	}
	if res := admin.do("PATCH", path, map[string]any{"admin_state": "exploded"}); res.Status != 422 {
		t.Errorf("bad state: %d", res.Status)
	}
	if res := admin.do("PATCH", "/api/admin/hosts/99999999-9999-4999-8999-999999999999", map[string]any{"admin_state": "active"}); res.Status != 404 {
		t.Errorf("unknown host: %d", res.Status)
	}
	if res := admin.do("PATCH", "/api/admin/hosts/not-a-uuid", map[string]any{"admin_state": "active"}); res.Status != 404 {
		t.Errorf("bad id: %d", res.Status)
	}
}

// A real agent (mock driver) heartbeats to the real API, and the API's agent
// client drives a VM on it. This is milestone 2's "done when" in one test.
func TestAgentAndControlPlaneTogether(t *testing.T) {
	app := newTestApp(t)
	admin := app.loginAs("admin@example.com", true)

	// The agent needs its token before it starts, and the API needs the
	// agent's URL to create the host, so start the HTTP server first with a
	// handler that is filled in afterwards.
	var agentHandler http.Handler
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { agentHandler.ServeHTTP(w, r) }))
	t.Cleanup(agentSrv.Close)
	created := app.createHost(admin, "local", agentSrv.URL)

	drv, err := mock.New(mock.Options{DataDir: t.TempDir(), VNCAddr: "127.0.0.1:5901", Images: mock.DefaultImages})
	if err != nil {
		t.Fatal(err)
	}
	log := logging.NewTo(io.Discard, "test-agent")
	agentHandler = agentserver.NewRouter(agentserver.Deps{
		Config: agentconfig.Config{VMDriver: "mock", Token: created.AgentToken},
		Logger: log,
		Driver: drv,
	})

	sender := &heartbeat.Sender{
		HostID: created.Host.ID.String(), Version: agentserver.Version, APIURL: app.srv.URL,
		Token: created.AgentToken, Interval: time.Second, Driver: drv, Logger: log,
	}
	ctx := context.Background()
	if err := sender.SendOnce(ctx); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	hosts := app.listHosts(admin)
	if len(hosts) != 1 || !hosts[0].Online || hosts[0].TotalRAMMB != 32768 || len(hosts[0].Images) != 2 {
		t.Fatalf("host after the agent's heartbeat: %+v", hosts)
	}

	// The control plane's client drives a mock VM on the agent.
	client := agentclient.New(agentSrv.URL, created.AgentToken)
	const id = "11111111-1111-4111-8111-111111111111"
	spec := vmtypes.VMSpec{
		ID: id, ImageSlug: "ubuntu-24.04-xfce", ImageVersion: "2026.11.1", RAMMB: 2048, VCPUs: 1, DiskGB: 40,
		Hostname: "zynthera-111111", Username: "tester", BridgeToken: strings.Repeat("a", 40),
		PasswordHash: "$6$zyntheratestsalt$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789./abcdefghijklmnopqrstuv",
	}
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	must("create", client.Create(ctx, spec))
	must("start", client.Start(ctx, id))
	must("suspend", client.Suspend(ctx, id))
	st, err := client.Status(ctx, id)
	must("status", err)
	if st.Status != vmtypes.StatusSuspended || !st.HasSavedState {
		t.Fatalf("status = %+v", st)
	}
	list, err := client.List(ctx)
	must("list", err)
	if len(list) != 1 {
		t.Fatalf("list = %+v", list)
	}

	// Errors come back typed.
	if err := client.Reboot(ctx, id); !agentclient.IsInvalidState(err) {
		t.Errorf("reboot of a suspended vm: %v", err)
	}
	if err := client.Start(ctx, "22222222-2222-4222-8222-222222222222"); !agentclient.IsNotFound(err) {
		t.Errorf("start of an unknown vm: %v", err)
	}
	wrong := agentclient.New(agentSrv.URL, "wrong-token")
	if err := wrong.Start(ctx, id); !agentclient.HasCode(err, "unauthenticated") {
		t.Errorf("wrong token: %v", err)
	}

	must("delete", client.Delete(ctx, id))
	st, err = client.Status(ctx, id)
	must("status after delete", err)
	if st.Status != vmtypes.StatusMissing {
		t.Fatalf("after delete: %+v", st)
	}
}
