package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	"zynthera/internal/agent/agentconfig"
	"zynthera/internal/agent/driver/mock"
	"zynthera/internal/agent/heartbeat"
	agentserver "zynthera/internal/agent/server"
	"zynthera/internal/api/jobs"
	"zynthera/internal/api/vmsvc"
	"zynthera/internal/shared/logging"
	"zynthera/internal/shared/vmtypes"
)

// vmApp is a testApp with one online host: a real agent with the mock driver.
type vmApp struct {
	*testApp
	admin  *browser
	drv    *mock.Driver
	sender *heartbeat.Sender
	hostID string
}

func newVMApp(t *testing.T) *vmApp {
	t.Helper()
	app := newTestApp(t)
	admin := app.loginAs("admin@example.com", true)

	var agentHandler http.Handler
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { agentHandler.ServeHTTP(w, r) }))
	t.Cleanup(agentSrv.Close)
	created := app.createHost(admin, "local", agentSrv.URL)

	drv, err := mock.New(mock.Options{
		DataDir: t.TempDir(), VNCAddr: "127.0.0.1:5901",
		Images: []vmtypes.ImageRef{{Slug: "ubuntu-24.04-xfce", Version: "2026.11.1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	log := logging.NewTo(io.Discard, "test-agent")
	agentHandler = agentserver.NewRouter(agentserver.Deps{
		Config: agentconfig.Config{VMDriver: "mock", Token: created.AgentToken}, Logger: log, Driver: drv,
	})
	sender := &heartbeat.Sender{
		HostID: created.Host.ID.String(), Version: agentserver.Version, APIURL: app.srv.URL,
		Token: created.AgentToken, Interval: time.Second, Driver: drv, Logger: log,
	}
	v := &vmApp{testApp: app, admin: admin, drv: drv, sender: sender, hostID: created.Host.ID.String()}
	v.beat()

	_, err = app.pool.Exec(context.Background(),
		`INSERT INTO os_images (slug, display_name, current_version, min_ram_mb, min_disk_gb, enabled)
		 VALUES ('ubuntu-24.04-xfce', 'Ubuntu 24.04', '2026.11.1', 2048, 40, true),
		        ('debian-12-xfce', 'Debian 12', '2026.11.1', 2048, 40, false)`)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// beat sends one heartbeat from the agent.
func (v *vmApp) beat() {
	v.t.Helper()
	if err := v.sender.SendOnce(context.Background()); err != nil {
		v.t.Fatalf("heartbeat: %v", err)
	}
}

// verifiedUser signs up, verifies and logs in a user with room for several PCs.
func (v *vmApp) verifiedUser(email string) *browser {
	v.t.Helper()
	b := v.loginAs(email, false)
	_, err := v.pool.Exec(context.Background(),
		`UPDATE users SET email_verified_at = now(), max_vms = 10, max_ram_mb = 131072, max_vcpus = 64, max_disk_gb = 2000 WHERE email = $1`, email)
	if err != nil {
		v.t.Fatal(err)
	}
	return b
}

func createBody(name string) map[string]any {
	return map[string]any{
		"name": name, "image": "ubuntu-24.04-xfce", "ram_mb": 2048, "vcpus": 1, "disk_gb": 40,
		"linux_username": "alex", "linux_password": "linux-pass-123",
	}
}

type vmEnvelope struct {
	VM vmsvc.VMJSON `json:"vm"`
}

func decodeVM(t *testing.T, raw string) vmsvc.VMJSON {
	t.Helper()
	var env vmEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("decode vm: %v: %s", err, raw)
	}
	return env.VM
}

func (v *vmApp) getVM(b *browser, id string) vmsvc.VMJSON {
	v.t.Helper()
	res := b.get("/api/vms/" + id)
	if res.Status != 200 {
		v.t.Fatalf("get vm: %d %s", res.Status, res.Raw)
	}
	return decodeVM(v.t, res.Raw)
}

// waitState polls until the VM reaches the state, or fails the test.
func (v *vmApp) waitState(b *browser, id, want string) vmsvc.VMJSON {
	v.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last vmsvc.VMJSON
	for time.Now().Before(deadline) {
		last = v.getVM(b, id)
		if last.State == want {
			return last
		}
		time.Sleep(25 * time.Millisecond)
	}
	detail := ""
	if last.StateDetail != nil {
		detail = *last.StateDetail
	}
	v.t.Fatalf("vm %s: state = %s (%s), want %s", id, last.State, detail, want)
	return last
}

// createVM creates a PC and waits until it runs.
func (v *vmApp) createVM(b *browser, name string) vmsvc.VMJSON {
	v.t.Helper()
	res := b.post("/api/vms", createBody(name))
	if res.Status != 202 {
		v.t.Fatalf("create vm: %d %s", res.Status, res.Raw)
	}
	vm := decodeVM(v.t, res.Raw)
	if vm.State != "provisioning" || len(vm.AllowedActions) != 0 {
		v.t.Fatalf("new vm: state %s, actions %v", vm.State, vm.AllowedActions)
	}
	return v.waitState(b, vm.ID.String(), "running")
}

func (v *vmApp) act(b *browser, id, action string, want int) response {
	v.t.Helper()
	res := b.post("/api/vms/"+id+"/"+action, nil)
	if res.Status != want {
		v.t.Fatalf("%s: status = %d, want %d: %s", action, res.Status, want, res.Raw)
	}
	return res
}

func (v *vmApp) eventTypes(id string) []string {
	v.t.Helper()
	rows, err := v.pool.Query(context.Background(), `SELECT type FROM vm_events WHERE vm_id = $1 ORDER BY id`, id)
	if err != nil {
		v.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			v.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func (v *vmApp) usage(id string) (total, open int) {
	v.t.Helper()
	err := v.pool.QueryRow(context.Background(),
		`SELECT count(*), count(*) FILTER (WHERE ended_at IS NULL) FROM usage_intervals WHERE vm_id = $1`, id).Scan(&total, &open)
	if err != nil {
		v.t.Fatal(err)
	}
	return total, open
}

func (v *vmApp) hostStatus(id string) vmtypes.VMStatus {
	v.t.Helper()
	st, err := v.drv.Status(context.Background(), id)
	if err != nil {
		v.t.Fatal(err)
	}
	return st
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// The whole lifecycle through the API, with the database checked at each step.
func TestVMLifecycle(t *testing.T) {
	v := newVMApp(t)
	b := v.verifiedUser("alex@example.com")

	// ---- create
	vm := v.createVM(b, "My PC")
	id := vm.ID.String()
	if vm.Image.Slug != "ubuntu-24.04-xfce" || vm.RAMMB != 2048 || vm.LinuxUsername != "alex" || vm.LastActiveAt == nil {
		t.Errorf("vm after create: %+v", vm)
	}
	for _, want := range []string{"connect", "suspend", "shutdown", "force_stop", "reboot", "snapshot", "delete"} {
		if !contains(vm.AllowedActions, want) {
			t.Errorf("running vm lacks action %q: %v", want, vm.AllowedActions)
		}
	}
	if st := v.hostStatus(id); st.Status != vmtypes.StatusRunning {
		t.Fatalf("host says %s after create", st.Status)
	}
	// The password hash is cleared once provisioning succeeded, and the
	// plain password was never stored anywhere.
	var pending *string
	if err := v.pool.QueryRow(context.Background(), `SELECT pending_password_hash FROM vms WHERE id = $1`, id).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != nil {
		t.Error("pending_password_hash still set after provisioning")
	}
	if total, open := v.usage(id); total != 1 || open != 1 {
		t.Errorf("usage after create: total %d, open %d", total, open)
	}

	// ---- suspend and resume
	v.act(b, id, "suspend", 202)
	vm = v.waitState(b, id, "suspended")
	if !vm.HasSavedState || !contains(vm.AllowedActions, "start") || !contains(vm.AllowedActions, "discard_saved_state") {
		t.Errorf("suspended vm: %+v", vm)
	}
	if st := v.hostStatus(id); st.Status != vmtypes.StatusSuspended {
		t.Fatalf("host says %s after suspend", st.Status)
	}
	if total, open := v.usage(id); total != 1 || open != 0 {
		t.Errorf("usage after suspend: total %d, open %d", total, open)
	}
	v.act(b, id, "start", 202)
	vm = v.waitState(b, id, "running")
	if vm.HasSavedState {
		t.Error("has_saved_state still true after resume")
	}
	if total, open := v.usage(id); total != 2 || open != 1 {
		t.Errorf("usage after resume: total %d, open %d", total, open)
	}

	// ---- reboot keeps the usage interval open
	v.act(b, id, "reboot", 202)
	v.waitState(b, id, "running")
	if total, open := v.usage(id); total != 2 || open != 1 {
		t.Errorf("usage after reboot: total %d, open %d", total, open)
	}

	// ---- snapshot of a running PC: suspend, snapshot, start again
	res := b.post("/api/vms/"+id+"/snapshots", map[string]string{"name": "before the experiment"})
	if res.Status != 202 {
		t.Fatalf("create snapshot: %d %s", res.Status, res.Raw)
	}
	var snapEnv struct {
		Snapshot snapshotJSON `json:"snapshot"`
	}
	if err := json.Unmarshal([]byte(res.Raw), &snapEnv); err != nil {
		t.Fatal(err)
	}
	snapID := snapEnv.Snapshot.ID.String()
	v.waitState(b, id, "running")
	if list := b.get("/api/vms/" + id + "/snapshots").Raw; !strings.Contains(list, `"status":"ready"`) || !strings.Contains(list, "before the experiment") {
		t.Errorf("snapshot list: %s", list)
	}
	// Restoring needs the PC off: not allowed while running.
	if res := b.post("/api/vms/"+id+"/snapshots/"+snapID+"/restore", nil); res.Status != 409 || res.Error.Code != "vm_invalid_state" {
		t.Errorf("restore while running: %d %s", res.Status, res.Raw)
	}

	// ---- shut down, restore, resize
	v.act(b, id, "shutdown", 202)
	vm = v.waitState(b, id, "stopped")
	if !contains(vm.AllowedActions, "resize") {
		t.Errorf("stopped vm lacks resize: %v", vm.AllowedActions)
	}
	if res := b.post("/api/vms/"+id+"/snapshots/"+snapID+"/restore", nil); res.Status != 202 {
		t.Fatalf("restore: %d %s", res.Status, res.Raw)
	}
	v.waitState(b, id, "stopped")

	if res := b.do("PATCH", "/api/vms/"+id, map[string]any{"disk_gb": 20}); res.Status != 422 {
		t.Errorf("shrinking the disk: %d %s", res.Status, res.Raw)
	}
	if res := b.do("PATCH", "/api/vms/"+id, map[string]any{"ram_mb": 4096, "vcpus": 2, "disk_gb": 80}); res.Status != 202 {
		t.Fatalf("resize: %d %s", res.Status, res.Raw)
	}
	vm = v.waitState(b, id, "stopped")
	if vm.RAMMB != 4096 || vm.VCPUs != 2 || vm.DiskGB != 80 {
		t.Errorf("after resize: %d MB, %d vCPUs, %d GB", vm.RAMMB, vm.VCPUs, vm.DiskGB)
	}

	// ---- rename and keep-alive need no job
	res = b.do("PATCH", "/api/vms/"+id, map[string]any{"name": "  Renamed  ", "keep_alive_hours": 4})
	if res.Status != 200 {
		t.Fatalf("rename: %d %s", res.Status, res.Raw)
	}
	if vm = decodeVM(t, res.Raw); vm.Name != "Renamed" || vm.KeepAliveUntil == nil {
		t.Errorf("after rename: %+v", vm)
	}
	if res := b.do("PATCH", "/api/vms/"+id, map[string]any{"keep_alive_hours": 99}); res.Status != 422 {
		t.Errorf("keep-alive beyond the maximum: %d", res.Status)
	}

	// ---- start again with the new size, then delete while running
	v.act(b, id, "start", 202)
	v.waitState(b, id, "running")
	if res := b.do("DELETE", "/api/vms/"+id+"/snapshots/"+snapID, nil); res.Status != 409 {
		t.Errorf("delete snapshot while running: %d", res.Status)
	}
	if res := b.do("DELETE", "/api/vms/"+id, nil); res.Status != 202 {
		t.Fatalf("delete: %d %s", res.Status, res.Raw)
	}
	deadline := time.Now().Add(15 * time.Second)
	for b.get("/api/vms/"+id).Status != 404 {
		if time.Now().After(deadline) {
			t.Fatal("vm still visible after delete")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if st := v.hostStatus(id); st.Status != vmtypes.StatusMissing {
		t.Errorf("host still has the vm after delete: %s", st.Status)
	}
	if list := b.get("/api/vms").Raw; strings.Contains(list, id) {
		t.Error("deleted vm still listed")
	}

	// ---- what the database recorded
	events := v.eventTypes(id)
	for _, want := range []string{"created", "provisioned", "started", "suspended", "resumed", "rebooted",
		"snapshot_created", "stopped", "snapshot_restored", "resized", "deleted"} {
		if !contains(events, want) {
			t.Errorf("event %q missing from %v", want, events)
		}
	}
	// One interval per period of running, all closed now:
	// create, resume, after the snapshot, after the final start.
	if total, open := v.usage(id); total != 4 || open != 0 {
		t.Errorf("usage at the end: total %d, open %d", total, open)
	}
	var badIntervals int
	if err := v.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM usage_intervals WHERE vm_id = $1 AND (ended_at IS NULL OR ended_at < started_at)`, id).Scan(&badIntervals); err != nil {
		t.Fatal(err)
	}
	if badIntervals != 0 {
		t.Errorf("%d usage intervals are open or end before they start", badIntervals)
	}
}

func TestFailedJobLandsInErrorAndRecovers(t *testing.T) {
	v := newVMApp(t)
	b := v.verifiedUser("alex@example.com")
	vm := v.createVM(b, "My PC")
	id := vm.ID.String()

	v.drv.SetFail("suspend", true)
	v.act(b, id, "suspend", 202)
	vm = v.waitState(b, id, "error")
	if vm.StateDetail == nil || *vm.StateDetail == "" {
		t.Fatal("error state without a readable detail")
	}
	if strings.Contains(*vm.StateDetail, "mock") || strings.Contains(*vm.StateDetail, "ZYN_") {
		t.Errorf("internal error text shown to the user: %q", *vm.StateDetail)
	}
	for _, want := range []string{"start", "force_stop", "delete"} {
		if !contains(vm.AllowedActions, want) {
			t.Errorf("error state lacks recovery action %q: %v", want, vm.AllowedActions)
		}
	}
	if !contains(v.eventTypes(id), "error") {
		t.Error("no error event recorded")
	}

	// Recovery: the host still runs the VM; force stop, then start.
	v.drv.SetFail("suspend", false)
	v.act(b, id, "force-stop", 202)
	v.waitState(b, id, "stopped")
	v.act(b, id, "start", 202)
	vm = v.waitState(b, id, "running")
	if vm.StateDetail != nil {
		t.Errorf("stale detail after recovery: %q", *vm.StateDetail)
	}
	if _, open := v.usage(id); open != 1 {
		t.Errorf("open usage intervals after recovery = %d, want 1", open)
	}
}

func TestProvisioningFailure(t *testing.T) {
	v := newVMApp(t)
	b := v.verifiedUser("alex@example.com")
	v.drv.SetFail("create", true)

	res := b.post("/api/vms", createBody("Doomed"))
	if res.Status != 202 {
		t.Fatalf("create: %d %s", res.Status, res.Raw)
	}
	id := decodeVM(t, res.Raw).ID.String()
	vm := v.waitState(b, id, "error")
	if !contains(vm.AllowedActions, "delete") {
		t.Errorf("failed vm cannot be deleted: %v", vm.AllowedActions)
	}
	if total, _ := v.usage(id); total != 0 {
		t.Errorf("a vm that never ran has %d usage intervals", total)
	}
	if res := b.do("DELETE", "/api/vms/"+id, nil); res.Status != 202 {
		t.Fatalf("delete: %d %s", res.Status, res.Raw)
	}
}

func TestShutdownThatTheGuestIgnores(t *testing.T) {
	v := newVMApp(t)
	b := v.verifiedUser("alex@example.com")
	id := v.createVM(b, "My PC").ID.String()

	v.drv.SetFail("shutdown_timeout", true)
	v.act(b, id, "shutdown", 202)
	// It goes through "stopping" and comes back to running with a hint.
	deadline := time.Now().Add(15 * time.Second)
	for {
		vm := v.getVM(b, id)
		if vm.State == "running" && vm.StateDetail != nil {
			if !strings.Contains(*vm.StateDetail, "Force stop") {
				t.Errorf("detail lacks the force stop hint: %q", *vm.StateDetail)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("vm did not return to running: %+v", vm)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if _, open := v.usage(id); open != 1 {
		t.Errorf("open usage intervals = %d, want 1 (the pc never stopped)", open)
	}
}

func TestTwoSimultaneousStartsExactlyOneWins(t *testing.T) {
	v := newVMApp(t)
	b := v.verifiedUser("alex@example.com")
	id := v.createVM(b, "My PC").ID.String()
	v.act(b, id, "suspend", 202)
	v.waitState(b, id, "suspended")

	const n = 8
	statuses := make([]int, n)
	codes := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res := b.post("/api/vms/"+id+"/start", nil)
			statuses[i], codes[i] = res.Status, res.Error.Code
		}(i)
	}
	wg.Wait()
	won := 0
	for i, status := range statuses {
		switch {
		case status == 202:
			won++
		case status == 409 && codes[i] == "vm_invalid_state":
		default:
			t.Errorf("request %d: unexpected answer %d %s", i, status, codes[i])
		}
	}
	if won != 1 {
		t.Fatalf("%d start requests were accepted, want exactly 1", won)
	}
	v.waitState(b, id, "running")
	if total, open := v.usage(id); total != 2 || open != 1 {
		t.Errorf("usage: total %d, open %d", total, open)
	}
}

func TestStartRefusedWithoutCapacity(t *testing.T) {
	v := newVMApp(t)
	b := v.verifiedUser("alex@example.com")

	// Four stopped PCs of 2 GB each.
	var ids []string
	for _, name := range []string{"a", "b", "c", "d"} {
		id := v.createVM(b, name).ID.String()
		v.act(b, id, "force-stop", 202)
		v.waitState(b, id, "stopped")
		ids = append(ids, id)
	}
	// Leave room for exactly one of them: the mock host has 32768 MB.
	res := v.admin.do("PATCH", "/api/admin/hosts/"+v.hostID, map[string]any{"reserved_ram_mb": 32768 - 2048})
	if res.Status != 200 {
		t.Fatalf("patch host: %d %s", res.Status, res.Raw)
	}

	// All four start at the same moment. The host row lock makes sure only
	// one of them takes the last free RAM.
	results := make([]response, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			results[i] = b.post("/api/vms/"+id+"/start", nil)
		}(i, id)
	}
	wg.Wait()
	accepted := 0
	for i, res := range results {
		switch {
		case res.Status == 202:
			accepted++
		case res.Status == 503 && res.Error.Code == "no_capacity":
			if res.Error.Message == "" {
				t.Error("no_capacity without a message")
			}
			// A refused PC stays exactly as it was.
			if vm := v.getVM(b, ids[i]); vm.State != "stopped" {
				t.Errorf("refused pc is in state %s", vm.State)
			}
		default:
			t.Errorf("start %d: unexpected answer %d %s", i, res.Status, res.Raw)
		}
	}
	if accepted != 1 {
		t.Fatalf("%d starts were accepted on a host with room for one", accepted)
	}

	// Creating is still possible (it only needs disk), but the new PC cannot
	// start and settles as stopped with an explanation.
	res = b.post("/api/vms", createBody("e"))
	if res.Status != 202 {
		t.Fatalf("create on a full host: %d %s", res.Status, res.Raw)
	}
	vm := v.waitState(b, decodeVM(t, res.Raw).ID.String(), "stopped")
	if vm.StateDetail == nil || !strings.Contains(*vm.StateDetail, "full") {
		t.Errorf("no explanation for the pc that could not start: %+v", vm.StateDetail)
	}
}

func TestOtherUsersGet404Everywhere(t *testing.T) {
	v := newVMApp(t)
	owner := v.verifiedUser("owner@example.com")
	other := v.verifiedUser("other@example.com")
	id := v.createVM(owner, "Private").ID.String()

	res := owner.post("/api/vms/"+id+"/snapshots", map[string]string{"name": "s"})
	if res.Status != 202 {
		t.Fatalf("snapshot: %d %s", res.Status, res.Raw)
	}
	var snapEnv struct {
		Snapshot snapshotJSON `json:"snapshot"`
	}
	_ = json.Unmarshal([]byte(res.Raw), &snapEnv)
	sid := snapEnv.Snapshot.ID.String()
	v.waitState(owner, id, "running")

	routes := []struct{ method, path string }{
		{"GET", "/api/vms/" + id},
		{"PATCH", "/api/vms/" + id},
		{"DELETE", "/api/vms/" + id},
		{"POST", "/api/vms/" + id + "/start"},
		{"POST", "/api/vms/" + id + "/suspend"},
		{"POST", "/api/vms/" + id + "/shutdown"},
		{"POST", "/api/vms/" + id + "/force-stop"},
		{"POST", "/api/vms/" + id + "/reboot"},
		{"POST", "/api/vms/" + id + "/discard-saved-state"},
		{"GET", "/api/vms/" + id + "/events"},
		{"GET", "/api/vms/" + id + "/snapshots"},
		{"POST", "/api/vms/" + id + "/snapshots"},
		{"POST", "/api/vms/" + id + "/snapshots/" + sid + "/restore"},
		{"DELETE", "/api/vms/" + id + "/snapshots/" + sid},
	}
	for _, rt := range routes {
		var body any
		if rt.method != "GET" {
			body = map[string]any{"name": "x"}
		}
		// The owner's own id and an id that does not exist must look the same.
		for _, target := range []string{rt.path, strings.Replace(rt.path, id, "99999999-9999-4999-8999-999999999999", 1)} {
			res := other.do(rt.method, target, body)
			if res.Status != 404 || res.Error.Code != "not_found" {
				t.Errorf("%s %s as another user: %d %s", rt.method, target, res.Status, res.Raw)
			}
		}
	}
	if list := other.get("/api/vms").Raw; strings.Contains(list, id) {
		t.Error("another user's vm is listed")
	}
	// Nothing happened to the owner's PC.
	if vm := v.getVM(owner, id); vm.State != "running" || vm.Name != "Private" {
		t.Errorf("owner's vm was affected: %+v", vm)
	}
	// A snapshot id from one PC cannot be used through another PC.
	otherID := v.createVM(other, "Mine").ID.String()
	if res := other.post("/api/vms/"+otherID+"/snapshots/"+sid+"/restore", nil); res.Status != 404 {
		t.Errorf("foreign snapshot through own vm: %d %s", res.Status, res.Raw)
	}
}

func TestCreateValidationAndLimits(t *testing.T) {
	v := newVMApp(t)

	// Unverified users cannot create a PC.
	unverified := v.loginAs("new@example.com", false)
	if res := unverified.post("/api/vms", createBody("x")); res.Status != 403 || res.Error.Code != "email_not_verified" {
		t.Errorf("unverified create: %d %s", res.Status, res.Raw)
	}

	b := v.verifiedUser("alex@example.com")
	bad := map[string]func(m map[string]any){
		"name":           func(m map[string]any) { m["name"] = "   " },
		"image":          func(m map[string]any) { m["image"] = "debian-12-xfce" }, // exists but is disabled
		"ram_mb":         func(m map[string]any) { m["ram_mb"] = 3000 },
		"vcpus":          func(m map[string]any) { m["vcpus"] = 3 },
		"disk_gb":        func(m map[string]any) { m["disk_gb"] = 41 },
		"linux_username": func(m map[string]any) { m["linux_username"] = "root" },
		"linux_password": func(m map[string]any) { m["linux_password"] = "short" },
	}
	for field, mutate := range bad {
		body := createBody("x")
		mutate(body)
		res := b.post("/api/vms", body)
		fields, _ := res.Error.Details["fields"].(map[string]any)
		if res.Status != 422 || fields[field] == nil {
			t.Errorf("bad %s: %d %s", field, res.Status, res.Raw)
		}
	}
	for _, username := range []string{"Alex", "a", "al", "1alex", "alex smith", "alex;id", "../x", "admin"} {
		body := createBody("x")
		body["linux_username"] = username
		if res := b.post("/api/vms", body); res.Status != 422 {
			t.Errorf("username %q accepted: %d", username, res.Status)
		}
	}

	// Per-user limits: this user may have one PC.
	if _, err := v.pool.Exec(context.Background(), `UPDATE users SET max_vms = 1 WHERE email = 'alex@example.com'`); err != nil {
		t.Fatal(err)
	}
	v.createVM(b, "first")
	res := b.post("/api/vms", createBody("second"))
	if res.Status != 422 || res.Error.Code != "limit_exceeded" || res.Error.Details["limit"] != "vms" {
		t.Errorf("second vm: %d %s", res.Status, res.Raw)
	}

	// Options reflect limits and usage.
	opts := b.get("/api/options").Raw
	for _, want := range []string{`"max_vms":1`, `"vms":1`, `"ram_mb":2048`, `"allowed_ram_mb":[2048,4096,8192,16384]`} {
		if !strings.Contains(opts, want) {
			t.Errorf("options lack %s: %s", want, opts)
		}
	}
	// Only enabled images that an online host has are offered.
	images := b.get("/api/images").Raw
	if !strings.Contains(images, "ubuntu-24.04-xfce") || strings.Contains(images, "debian") {
		t.Errorf("images: %s", images)
	}
}

func TestNoCapacityWhenNoHostIsOnline(t *testing.T) {
	v := newVMApp(t)
	b := v.verifiedUser("alex@example.com")
	if _, err := v.pool.Exec(context.Background(), `UPDATE hosts SET last_heartbeat_at = now() - interval '5 minutes'`); err != nil {
		t.Fatal(err)
	}
	res := b.post("/api/vms", createBody("x"))
	if res.Status != 503 || res.Error.Code != "no_capacity" {
		t.Fatalf("create with no host online: %d %s", res.Status, res.Raw)
	}
	var n int
	if err := v.pool.QueryRow(context.Background(), `SELECT count(*) FROM vms`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d vm rows left behind by a refused creation", n)
	}
	if images := b.get("/api/images").Raw; strings.Contains(images, "ubuntu") {
		t.Errorf("images offered although no host is online: %s", images)
	}
}

func TestSnapshotLimit(t *testing.T) {
	v := newVMApp(t)
	b := v.verifiedUser("alex@example.com")
	id := v.createVM(b, "My PC").ID.String()
	v.act(b, id, "force-stop", 202)
	v.waitState(b, id, "stopped")

	for i := 0; i < v.cfg.MaxSnapshotsPerVM; i++ {
		if res := b.post("/api/vms/"+id+"/snapshots", map[string]string{"name": "snap"}); res.Status != 202 {
			t.Fatalf("snapshot %d: %d %s", i+1, res.Status, res.Raw)
		}
		// Wait until the job is done and the PC is free again.
		deadline := time.Now().Add(15 * time.Second)
		for strings.Contains(b.get("/api/vms/"+id+"/snapshots").Raw, `"creating"`) || v.getVM(b, id).State != "stopped" {
			if time.Now().After(deadline) {
				t.Fatal("snapshot did not finish")
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	res := b.post("/api/vms/"+id+"/snapshots", map[string]string{"name": "one too many"})
	if res.Status != 422 || res.Error.Code != "limit_exceeded" {
		t.Fatalf("snapshot over the limit: %d %s", res.Status, res.Raw)
	}
	// The refused request left the PC usable.
	if vm := v.getVM(b, id); vm.State != "stopped" {
		t.Errorf("state after refused snapshot: %s", vm.State)
	}
}

func (v *vmApp) runCron(taskType string) {
	v.t.Helper()
	if err := v.worker.Mux().ProcessTask(context.Background(), asynq.NewTask(taskType, nil)); err != nil {
		v.t.Fatalf("%s: %v", taskType, err)
	}
}

func TestReconcileFollowsTheHost(t *testing.T) {
	v := newVMApp(t)
	b := v.verifiedUser("alex@example.com")
	id := v.createVM(b, "My PC").ID.String()
	ctx := context.Background()

	// The user shuts the PC down from inside: the host sees it stop.
	if err := v.drv.ForceStop(ctx, id); err != nil {
		t.Fatal(err)
	}
	// One heartbeat is not enough to act on.
	v.beat()
	v.runCron(jobs.TypeCronReconcile)
	if vm := v.getVM(b, id); vm.State != "running" {
		t.Fatalf("corrected after a single heartbeat: %s", vm.State)
	}
	// Two heartbeats, both taken after the VM's last state change.
	time.Sleep(20 * time.Millisecond)
	v.beat()
	v.beat()
	v.runCron(jobs.TypeCronReconcile)
	vm := v.getVM(b, id)
	if vm.State != "stopped" || vm.HasSavedState {
		t.Fatalf("after reconcile: %+v", vm)
	}
	if _, open := v.usage(id); open != 0 {
		t.Error("usage interval still open after the pc stopped by itself")
	}
	var msg string
	if err := v.pool.QueryRow(ctx, `SELECT message FROM vm_events WHERE vm_id = $1 ORDER BY id DESC LIMIT 1`, id).Scan(&msg); err != nil {
		t.Fatal(err)
	}
	if msg != "Shut down from inside the PC" {
		t.Errorf("event message = %q", msg)
	}

	// A VM the host no longer has becomes "error".
	if err := v.drv.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	v.beat()
	v.beat()
	v.runCron(jobs.TypeCronReconcile)
	vm = v.getVM(b, id)
	if vm.State != "error" || vm.StateDetail == nil || !strings.Contains(*vm.StateDetail, "not found") {
		t.Fatalf("after the host lost the vm: %+v", vm)
	}
}

func TestHeartbeatUpdatesDiskUsage(t *testing.T) {
	v := newVMApp(t)
	b := v.verifiedUser("alex@example.com")
	id := v.createVM(b, "My PC").ID.String()
	v.beat()
	if vm := v.getVM(b, id); vm.DiskUsedBytes != 9<<30 {
		t.Errorf("disk_used_bytes = %d", vm.DiskUsedBytes)
	}
}

func TestIdleSweepAndKeepAlive(t *testing.T) {
	v := newVMApp(t)
	b := v.verifiedUser("alex@example.com")
	idle := v.createVM(b, "idle").ID.String()
	kept := v.createVM(b, "kept").ID.String()
	fresh := v.createVM(b, "fresh").ID.String()
	ctx := context.Background()

	if res := b.do("PATCH", "/api/vms/"+kept, map[string]any{"keep_alive_hours": 4}); res.Status != 200 {
		t.Fatalf("keep-alive: %d %s", res.Status, res.Raw)
	}
	if _, err := v.pool.Exec(ctx, `UPDATE vms SET last_active_at = now() - interval '16 minutes' WHERE id IN ($1, $2)`, idle, kept); err != nil {
		t.Fatal(err)
	}
	v.runCron(jobs.TypeCronIdleSweep)

	v.waitState(b, idle, "suspended")
	var msg string
	if err := v.pool.QueryRow(ctx, `SELECT message FROM vm_events WHERE vm_id = $1 AND type = 'suspended'`, idle).Scan(&msg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "idle") {
		t.Errorf("suspend event message = %q", msg)
	}
	for name, id := range map[string]string{"kept alive": kept, "recently active": fresh} {
		if vm := v.getVM(b, id); vm.State != "running" {
			t.Errorf("the %s pc was suspended: %s", name, vm.State)
		}
	}
}

func TestLogoutSuspendsRunningPCs(t *testing.T) {
	v := newVMApp(t)
	b := v.verifiedUser("alex@example.com")
	plain := v.createVM(b, "plain").ID.String()
	kept := v.createVM(b, "kept").ID.String()
	if res := b.do("PATCH", "/api/vms/"+kept, map[string]any{"keep_alive_hours": 1}); res.Status != 200 {
		t.Fatalf("keep-alive: %d", res.Status)
	}

	b.mustPost(200, "/api/auth/logout", nil)
	b.mustPost(200, "/api/auth/login", creds{"alex@example.com", "a-good-password"})

	v.waitState(b, plain, "suspended")
	if vm := v.getVM(b, kept); vm.State != "running" {
		t.Errorf("the keep-alive pc was suspended on logout: %s", vm.State)
	}
}

func TestEventStream(t *testing.T) {
	v := newVMApp(t)
	b := v.verifiedUser("alex@example.com")
	other := v.verifiedUser("other@example.com")
	id := v.createVM(b, "My PC").ID.String()
	otherID := v.createVM(other, "Not mine").ID.String()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", v.srv.URL+"/api/events", nil)
	res, err := b.client.Do(req) //nolint:bodyclose // closed by the defer below; the linter loses track because a goroutine reads the body
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if res.Header.Get("X-Accel-Buffering") != "no" {
		t.Error("missing X-Accel-Buffering: no")
	}

	type event struct{ name, data string }
	stream := make(chan event, 64)
	go func() {
		defer close(stream)
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var name string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				stream <- event{name, strings.TrimPrefix(line, "data: ")}
			}
		}
	}()
	next := func() event {
		t.Helper()
		select {
		case ev, ok := <-stream:
			if !ok {
				t.Fatal("event stream closed")
			}
			return ev
		case <-time.After(10 * time.Second):
			t.Fatal("no event within 10 seconds")
		}
		return event{}
	}

	// First the current list, then "ready".
	first := next()
	if first.name != "vm" || !strings.Contains(first.data, id) || !strings.Contains(first.data, `"state":"running"`) {
		t.Fatalf("first event: %+v", first)
	}
	if ev := next(); ev.name != "ready" {
		t.Fatalf("expected ready, got %+v", ev)
	}

	// Another user's changes never arrive here.
	v.act(other, otherID, "suspend", 202)
	v.waitState(other, otherID, "suspended")

	// A change of our own PC arrives as it happens.
	v.act(b, id, "suspend", 202)
	var states []string
	for {
		ev := next()
		if strings.Contains(ev.data, otherID) {
			t.Fatalf("received another user's vm: %s", ev.data)
		}
		var vm vmsvc.VMJSON
		if err := json.Unmarshal([]byte(ev.data), &vm); err != nil {
			t.Fatalf("bad event data: %v: %s", err, ev.data)
		}
		states = append(states, vm.State)
		if vm.State == "suspended" {
			if !contains(vm.AllowedActions, "start") {
				t.Errorf("event lacks allowed_actions: %s", ev.data)
			}
			break
		}
	}
	if !contains(states, "suspending") {
		t.Errorf("did not see the transitional state: %v", states)
	}

	// Logged out: no stream.
	if res := v.newBrowser().get("/api/events"); res.Status != 401 {
		t.Errorf("events without login: %d", res.Status)
	}
}
