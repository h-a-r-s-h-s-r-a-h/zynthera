package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"zynthera/internal/api/jobs"
	"zynthera/internal/shared/vmtypes"
)

type adminVMList struct {
	VMs        []adminVMJSON `json:"vms"`
	NextCursor string        `json:"next_cursor"`
}

func (v *vmApp) adminVMs(query string) adminVMList {
	v.t.Helper()
	res := v.admin.get("/api/admin/vms" + query)
	if res.Status != 200 {
		v.t.Fatalf("admin vms%s: %d %s", query, res.Status, res.Raw)
	}
	var out adminVMList
	if err := json.Unmarshal([]byte(res.Raw), &out); err != nil {
		v.t.Fatal(err)
	}
	return out
}

func TestAdminSeesAndControlsEveryPC(t *testing.T) {
	v := newVMApp(t)
	alex := v.verifiedUser("alex@example.com")
	sam := v.verifiedUser("sam@example.com")
	a1 := v.createVM(alex, "alex one").ID.String()
	a2 := v.createVM(alex, "alex two").ID.String()
	s1 := v.createVM(sam, "sam one").ID.String()

	// Normal users are kept out of every admin route.
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/admin/vms"}, {"GET", "/api/admin/users"}, {"GET", "/api/admin/usage"},
		{"POST", "/api/admin/vms/" + s1 + "/suspend"}, {"POST", "/api/admin/vms/" + s1 + "/force-stop"},
		{"PATCH", "/api/admin/users/" + s1},
	} {
		if res := alex.do(route.method, route.path, map[string]any{}); res.Status != 403 {
			t.Errorf("%s %s as a normal user: %d", route.method, route.path, res.Status)
		}
	}

	all := v.adminVMs("")
	if len(all.VMs) != 3 || all.NextCursor != "" {
		t.Fatalf("admin list: %d vms, cursor %q", len(all.VMs), all.NextCursor)
	}
	// Newest first, with owner and host.
	if all.VMs[0].ID.String() != s1 || all.VMs[0].UserEmail != "sam@example.com" || all.VMs[0].HostName != "local" {
		t.Errorf("first row: %+v", all.VMs[0])
	}

	// Filters.
	if got := v.adminVMs("?email=alex"); len(got.VMs) != 2 {
		t.Errorf("filter by email: %d", len(got.VMs))
	}
	if got := v.adminVMs("?email=ALEX%40EXAMPLE"); len(got.VMs) != 2 {
		t.Errorf("email filter is case sensitive: %d", len(got.VMs))
	}
	if got := v.adminVMs("?state=suspended"); len(got.VMs) != 0 {
		t.Errorf("filter by state: %d", len(got.VMs))
	}
	if got := v.adminVMs("?host=" + v.hostID); len(got.VMs) != 3 {
		t.Errorf("filter by host: %d", len(got.VMs))
	}
	if got := v.adminVMs("?host=99999999-9999-4999-8999-999999999999"); len(got.VMs) != 0 {
		t.Errorf("filter by another host: %d", len(got.VMs))
	}
	for _, bad := range []string{"?state=exploded", "?host=nope", "?cursor=nope", "?email=%25%27%3B+drop+table+vms"} {
		res := v.admin.get("/api/admin/vms" + bad)
		if strings.HasPrefix(bad, "?email") {
			if res.Status != 200 {
				t.Errorf("hostile email filter: %d", res.Status)
			}
			continue
		}
		if res.Status != 422 {
			t.Errorf("bad query %s: %d", bad, res.Status)
		}
	}

	// Pagination: pages of two, no row twice, none missing.
	first := v.adminVMs("?limit=2")
	if len(first.VMs) != 2 || first.NextCursor == "" {
		t.Fatalf("first page: %d rows, cursor %q", len(first.VMs), first.NextCursor)
	}
	second := v.adminVMs("?limit=2&cursor=" + first.NextCursor)
	if len(second.VMs) != 1 || second.NextCursor != "" {
		t.Fatalf("second page: %d rows, cursor %q", len(second.VMs), second.NextCursor)
	}
	seen := map[string]bool{}
	for _, vm := range append(first.VMs, second.VMs...) {
		seen[vm.ID.String()] = true
	}
	if len(seen) != 3 {
		t.Errorf("pagination returned %d distinct vms, want 3", len(seen))
	}

	// The admin suspends one PC and force stops another.
	if res := v.admin.post("/api/admin/vms/"+a1+"/suspend", nil); res.Status != 202 {
		t.Fatalf("admin suspend: %d %s", res.Status, res.Raw)
	}
	v.waitState(alex, a1, "suspended")
	if res := v.admin.post("/api/admin/vms/"+a2+"/force-stop", nil); res.Status != 202 {
		t.Fatalf("admin force stop: %d %s", res.Status, res.Raw)
	}
	v.waitState(alex, a2, "stopped")
	if res := v.admin.post("/api/admin/vms/"+a1+"/suspend", nil); res.Status != 409 {
		t.Errorf("suspending a suspended pc: %d", res.Status)
	}
	if res := v.admin.post("/api/admin/vms/99999999-9999-4999-8999-999999999999/suspend", nil); res.Status != 404 {
		t.Errorf("unknown pc: %d", res.Status)
	}
	// The owner's history says who did it.
	var actor, message string
	err := v.pool.QueryRow(context.Background(),
		`SELECT actor, message FROM vm_events WHERE vm_id = $1 AND type = 'suspended'`, a1).Scan(&actor, &message)
	if err != nil || actor != "admin" || !strings.Contains(message, "administrator") {
		t.Errorf("suspend event: actor=%q message=%q err=%v", actor, message, err)
	}

	// Host capacity in the host list reflects what is running: only sam's PC.
	var hosts struct {
		Hosts []hostJSON `json:"hosts"`
	}
	if err := json.Unmarshal([]byte(v.admin.get("/api/admin/hosts").Raw), &hosts); err != nil {
		t.Fatal(err)
	}
	h := hosts.Hosts[0]
	if h.UsedRAMMB != 2048 || h.UsedVCPUs != 1 || h.VMCount != 3 || h.CommittedDiskGB != 3*(40+2) {
		t.Errorf("host usage: ram %d, vcpus %d, vms %d, disk %d", h.UsedRAMMB, h.UsedVCPUs, h.VMCount, h.CommittedDiskGB)
	}
}

func TestAdminUsersLimitsAndDisable(t *testing.T) {
	v := newVMApp(t)
	alex := v.verifiedUser("alex@example.com")
	id := v.createVM(alex, "alex one").ID.String()

	var list struct {
		Users []adminUserJSON `json:"users"`
	}
	if err := json.Unmarshal([]byte(v.admin.get("/api/admin/users?email=alex").Raw), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Users) != 1 || list.Users[0].VMCount != 1 || list.Users[0].UsedRAMMB != 2048 || list.Users[0].Disabled {
		t.Fatalf("users: %+v", list.Users)
	}
	userPath := "/api/admin/users/" + list.Users[0].ID.String()

	// Set two limits; the others keep their value. null clears an override.
	res := v.admin.do("PATCH", userPath, map[string]any{"max_vms": 1, "max_ram_mb": 4096})
	if res.Status != 200 || !strings.Contains(res.Raw, `"max_vms":1`) || !strings.Contains(res.Raw, `"max_ram_mb":4096`) || !strings.Contains(res.Raw, `"max_disk_gb":2000`) {
		t.Fatalf("set limits: %d %s", res.Status, res.Raw)
	}
	if res := alex.post("/api/vms", createBody("second")); res.Status != 422 || res.Error.Code != "limit_exceeded" {
		t.Errorf("creating beyond the new limit: %d %s", res.Status, res.Raw)
	}
	res = v.admin.do("PATCH", userPath, map[string]any{"max_vms": nil})
	if res.Status != 200 || !strings.Contains(res.Raw, `"max_vms":null`) {
		t.Fatalf("clear a limit: %d %s", res.Status, res.Raw)
	}
	for _, bad := range []map[string]any{{"max_vms": -1}, {"max_ram_mb": "lots"}, {"disabled": "yes"}, {"role": "admin"}} {
		if res := v.admin.do("PATCH", userPath, bad); res.Status != 422 && res.Status != 400 {
			t.Errorf("bad patch %v: %d", bad, res.Status)
		}
	}
	// A PATCH cannot change a role, whatever is sent.
	var role string
	if err := v.pool.QueryRow(context.Background(), `SELECT role FROM users WHERE email = 'alex@example.com'`).Scan(&role); err != nil || role != "user" {
		t.Errorf("role = %q, %v", role, err)
	}

	// Disabling: logged out at once, running PC suspended, nothing deleted.
	if res := v.admin.do("PATCH", userPath, map[string]any{"disabled": true}); res.Status != 200 || !strings.Contains(res.Raw, `"disabled":true`) {
		t.Fatalf("disable: %d %s", res.Status, res.Raw)
	}
	if res := alex.get("/api/auth/me"); res.Status != 401 {
		t.Errorf("disabled user still has a session: %d", res.Status)
	}
	if res := v.newBrowser().post("/api/auth/login", creds{"alex@example.com", "a-good-password"}); res.Status != 401 {
		t.Errorf("disabled user can log in: %d", res.Status)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		got := v.adminVMs("?email=alex")
		if len(got.VMs) == 1 && got.VMs[0].State == "suspended" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the disabled user's pc was not suspended: %+v", got.VMs)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if st := v.hostStatus(id); st.Status != "suspended" {
		t.Errorf("host says %s", st.Status)
	}

	// Enabling again restores access.
	if res := v.admin.do("PATCH", userPath, map[string]any{"disabled": false}); res.Status != 200 {
		t.Fatalf("enable: %d", res.Status)
	}
	v.newBrowser().mustPost(200, "/api/auth/login", creds{"alex@example.com", "a-good-password"})

	// An admin cannot lock themselves out.
	var adminID string
	if err := v.pool.QueryRow(context.Background(), `SELECT id FROM users WHERE email = 'admin@example.com'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if res := v.admin.do("PATCH", "/api/admin/users/"+adminID, map[string]any{"disabled": true}); res.Status != 422 {
		t.Errorf("admin disabling themselves: %d %s", res.Status, res.Raw)
	}
}

func TestAdminUsage(t *testing.T) {
	v := newVMApp(t)
	alex := v.verifiedUser("alex@example.com")
	id := v.createVM(alex, "alex one").ID.String()
	ctx := context.Background()

	// Replace the live interval with known history:
	//   2 GB: 10:00-13:00 on Nov 10 (3 h), and 22:00 Nov 30 - 02:00 Dec 1 (2 h in November)
	//   4 GB: 08:00-09:30 on Nov 20 (1.5 h)
	if _, err := v.pool.Exec(ctx, `DELETE FROM usage_intervals`); err != nil {
		t.Fatal(err)
	}
	insert := func(ram int, from, to string) {
		_, err := v.pool.Exec(ctx,
			`INSERT INTO usage_intervals (vm_id, user_id, host_id, ram_mb, vcpus, started_at, ended_at)
			 SELECT id, user_id, host_id, $2, 1, $3::timestamptz, $4::timestamptz FROM vms WHERE id = $1`, id, ram, from, to)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert(2048, "2026-11-10T10:00:00Z", "2026-11-10T13:00:00Z")
	insert(2048, "2026-11-30T22:00:00Z", "2026-12-01T02:00:00Z")
	insert(4096, "2026-11-20T08:00:00Z", "2026-11-20T09:30:00Z")

	var usage struct {
		Rows       []usageRowJSON `json:"rows"`
		TotalHours float64        `json:"total_hours"`
	}
	res := v.admin.get("/api/admin/usage?from=2026-11-01&to=2026-12-01")
	if res.Status != 200 {
		t.Fatalf("usage: %d %s", res.Status, res.Raw)
	}
	if err := json.Unmarshal([]byte(res.Raw), &usage); err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, row := range usage.Rows {
		got[fmt.Sprintf("%s/%d", row.UserEmail, row.RAMMB)] = row.Hours
	}
	// The interval that crosses midnight on Dec 1 only counts its 2 hours in November.
	if got["alex@example.com/2048"] != 5 || got["alex@example.com/4096"] != 1.5 || usage.TotalHours != 6.5 {
		t.Errorf("usage = %v, total %v", got, usage.TotalHours)
	}
	// December sees the other 2 hours of that interval and nothing else.
	_ = json.Unmarshal([]byte(v.admin.get("/api/admin/usage?from=2026-12-01&to=2027-01-01").Raw), &usage)
	if len(usage.Rows) != 1 || usage.Rows[0].Hours != 2 {
		t.Errorf("december usage = %+v", usage.Rows)
	}

	// CSV export.
	csvRes := v.admin.get("/api/admin/usage?from=2026-11-01&to=2026-12-01&format=csv")
	if csvRes.Status != 200 || !strings.HasPrefix(csvRes.Header.Get("Content-Type"), "text/csv") ||
		!strings.Contains(csvRes.Header.Get("Content-Disposition"), "zynthera-usage-2026-11-01-to-2026-12-01.csv") {
		t.Fatalf("csv headers: %d %v", csvRes.Status, csvRes.Header)
	}
	want := "user_email,ram_mb,vcpus,hours\nalex@example.com,2048,1,5.00\nalex@example.com,4096,1,1.50\n"
	if csvRes.Raw != want {
		t.Errorf("csv =\n%s\nwant\n%s", csvRes.Raw, want)
	}

	for _, bad := range []string{"?from=yesterday", "?from=2026-12-01&to=2026-11-01", "?from=2020-01-01&to=2026-11-01"} {
		if res := v.admin.get("/api/admin/usage" + bad); res.Status != 422 {
			t.Errorf("bad range %s: %d", bad, res.Status)
		}
	}
	if csvSafe("=cmd|calc") != "'=cmd|calc" || csvSafe("alex@example.com") != "alex@example.com" {
		t.Error("csvSafe does not neutralise formulas")
	}
}

func TestAccountDeletion(t *testing.T) {
	v := newVMApp(t)
	alex := v.verifiedUser("alex@example.com")
	running := v.createVM(alex, "Alex's secret project").ID.String()
	stopped := v.createVM(alex, "second").ID.String()
	v.act(alex, stopped, "force-stop", 202)
	v.waitState(alex, stopped, "stopped")
	ctx := context.Background()

	// The password is asked again; a wrong one changes nothing.
	if res := alex.post("/api/auth/delete-account", map[string]string{"password": "not-my-password"}); res.Status != 422 {
		t.Fatalf("wrong password: %d %s", res.Status, res.Raw)
	}
	if res := alex.get("/api/auth/me"); res.Status != 200 {
		t.Fatalf("still logged in after a refused deletion: %d", res.Status)
	}
	if res := v.newBrowser().post("/api/auth/delete-account", map[string]string{"password": "a-good-password"}); res.Status != 401 {
		t.Errorf("deletion without a session: %d", res.Status)
	}

	alex.mustPost(200, "/api/auth/delete-account", map[string]string{"password": "a-good-password"})

	// Locked out at once, everywhere.
	if res := alex.get("/api/auth/me"); res.Status != 401 {
		t.Errorf("session survived account deletion: %d", res.Status)
	}
	if res := v.newBrowser().post("/api/auth/login", creds{"alex@example.com", "a-good-password"}); res.Status != 401 {
		t.Errorf("login after deletion: %d", res.Status)
	}

	// Both PCs disappear from the host, including the running one.
	deadline := time.Now().Add(20 * time.Second)
	for {
		var left int
		if err := v.pool.QueryRow(ctx, `SELECT count(*) FROM vms WHERE deleted_at IS NULL`).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d pcs were not deleted", left)
		}
		time.Sleep(25 * time.Millisecond)
	}
	for _, id := range []string{running, stopped} {
		if st := v.hostStatus(id); st.Status != "missing" {
			t.Errorf("pc %s still on the host: %s", id, st.Status)
		}
	}

	// The periodic task then removes what identifies the person.
	v.runCron(jobs.TypeCronAccountCleanup)
	var email, hash string
	var verified *time.Time
	err := v.pool.QueryRow(ctx,
		`SELECT email, password_hash, email_verified_at FROM users WHERE deletion_requested_at IS NOT NULL`).Scan(&email, &hash, &verified)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(email, "alex") || !strings.HasSuffix(email, "@deleted.invalid") || hash != "!" || verified != nil {
		t.Errorf("user not anonymized: email=%q hash=%q verified=%v", email, hash, verified)
	}
	var names int
	if err := v.pool.QueryRow(ctx, `SELECT count(*) FROM vms WHERE name LIKE '%secret%' OR linux_username = 'alex'`).Scan(&names); err != nil {
		t.Fatal(err)
	}
	if names != 0 {
		t.Errorf("%d pc rows still carry the person's own names", names)
	}
	var sessions, tokens, usage int
	_ = v.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM sessions s JOIN users u ON u.id = s.user_id WHERE u.email LIKE 'deleted-%'),
		(SELECT count(*) FROM email_tokens e JOIN users u ON u.id = e.user_id WHERE u.email LIKE 'deleted-%'),
		(SELECT count(*) FROM usage_intervals)`).Scan(&sessions, &tokens, &usage)
	if sessions != 0 || tokens != 0 {
		t.Errorf("sessions=%d email tokens=%d left for the deleted account", sessions, tokens)
	}
	// Usage records stay (billing), now pointing at an anonymous row.
	if usage == 0 {
		t.Error("usage records were deleted with the account")
	}
	// Running the task again is harmless.
	v.runCron(jobs.TypeCronAccountCleanup)

	// The address is free again: the same person can sign up afresh.
	fresh := v.newBrowser()
	fresh.mustPost(202, "/api/auth/signup", creds{"alex@example.com", "another-password-1"})
	fresh.mustPost(200, "/api/auth/login", creds{"alex@example.com", "another-password-1"})
	if list := fresh.get("/api/vms").Raw; strings.Contains(list, running) {
		t.Error("the new account sees the old account's pcs")
	}

	// The only administrator cannot delete their own account.
	if res := v.admin.post("/api/auth/delete-account", map[string]string{"password": "a-good-password"}); res.Status != 409 || res.Error.Code != "last_admin" {
		t.Errorf("last admin deleting themselves: %d %s", res.Status, res.Raw)
	}
}

// Milestone 7's "done when": a second host can be registered and receives
// new PCs while the first is draining; PCs on the draining host still start.
func TestDrainingHost(t *testing.T) {
	v := newVMApp(t)
	alex := v.verifiedUser("alex@example.com")
	ctx := context.Background()

	onFirst := v.createVM(alex, "on the first host").ID.String()
	v.act(alex, onFirst, "force-stop", 202)
	v.waitState(alex, onFirst, "stopped")

	// A second host comes online with the same image.
	second := v.createHost(v.admin, "host-2", "http://127.0.0.1:1")
	hb := vmtypes.Heartbeat{
		HostID: second.Host.ID.String(), AgentVersion: "0.1.0",
		Capacity: vmtypes.Capacity{TotalRAMMB: 65536, TotalVCPUs: 32, TotalDiskGB: 1000, FreeDiskGB: 1000},
		Images:   []vmtypes.ImageRef{{Slug: "ubuntu-24.04-xfce", Version: "2026.11.1"}},
		VMs:      []vmtypes.VMStatus{},
	}
	if got := v.postHeartbeat(second.AgentToken, hb); got != 200 {
		t.Fatalf("second host heartbeat: %d", got)
	}

	hostOf := func(id string) string {
		var name string
		if err := v.pool.QueryRow(ctx, `SELECT h.name FROM vms JOIN hosts h ON h.id = vms.host_id WHERE vms.id = $1`, id).Scan(&name); err != nil {
			t.Fatal(err)
		}
		return name
	}

	// Drain the first host.
	if res := v.admin.do("PATCH", "/api/admin/hosts/"+v.hostID, map[string]any{"admin_state": "draining"}); res.Status != 200 {
		t.Fatalf("drain: %d %s", res.Status, res.Raw)
	}

	// A new PC must land on the second host. (That host has no real agent
	// in this test, so the PC cannot be set up; only its placement matters.)
	res := alex.post("/api/vms", createBody("after draining"))
	if res.Status != 202 {
		t.Fatalf("create while draining: %d %s", res.Status, res.Raw)
	}
	if got := hostOf(decodeVM(t, res.Raw).ID.String()); got != "host-2" {
		t.Errorf("new pc was placed on %q, want host-2", got)
	}

	// The PC that already lives on the draining host still starts, so its
	// owner is not locked out.
	v.act(alex, onFirst, "start", 202)
	v.waitState(alex, onFirst, "running")
	if got := hostOf(onFirst); got != "local" {
		t.Errorf("existing pc moved to %q", got)
	}

	// With both hosts unable to take new PCs, creation is refused clearly.
	if res := v.admin.do("PATCH", "/api/admin/hosts/"+second.Host.ID.String(), map[string]any{"admin_state": "draining"}); res.Status != 200 {
		t.Fatalf("drain second: %d", res.Status)
	}
	if res := alex.post("/api/vms", createBody("nowhere to go")); res.Status != 503 || res.Error.Code != "no_capacity" {
		t.Errorf("create with every host draining: %d %s", res.Status, res.Raw)
	}

	// A disabled host starts nothing.
	v.act(alex, onFirst, "force-stop", 202)
	v.waitState(alex, onFirst, "stopped")
	if res := v.admin.do("PATCH", "/api/admin/hosts/"+v.hostID, map[string]any{"admin_state": "disabled"}); res.Status != 200 {
		t.Fatalf("disable: %d", res.Status)
	}
	if res := alex.post("/api/vms/"+onFirst+"/start", nil); res.Status != 503 || res.Error.Code != "host_unavailable" {
		t.Errorf("start on a disabled host: %d %s", res.Status, res.Raw)
	}
}
