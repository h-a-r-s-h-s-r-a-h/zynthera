package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"zynthera/internal/agent/agentconfig"
	"zynthera/internal/agent/driver/mock"
	"zynthera/internal/shared/httpx"
	"zynthera/internal/shared/logging"
	"zynthera/internal/shared/vmtypes"
)

const (
	testToken = "test-agent-token"
	vmID      = "11111111-1111-4111-8111-111111111111"
	testHash  = "$6$zyntheratestsalt$" +
		"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789./abcdefghijklmnopqrstuv"
)

func newTestServer(t *testing.T, delays mock.Delays, fail ...string) *httptest.Server {
	t.Helper()
	drv, err := mock.New(mock.Options{
		DataDir: t.TempDir(),
		VNCAddr: "127.0.0.1:5901",
		Images:  []vmtypes.ImageRef{{Slug: "ubuntu-24.04-xfce", Version: "2026.11.1"}},
		Delays:  delays,
		Fail:    fail,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewRouter(Deps{
		Config: agentconfig.Config{VMDriver: "mock", Token: testToken},
		Logger: logging.NewTo(io.Discard, "test"),
		Driver: drv,
	}))
	t.Cleanup(srv.Close)
	return srv
}

type result struct {
	Status int
	Raw    string
	Error  httpx.ErrorDetail
}

func call(t *testing.T, srv *httptest.Server, token, method, path string, body any) result {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := result{Status: res.StatusCode, Raw: string(raw)}
	var env httpx.ErrorBody
	if json.Unmarshal(raw, &env) == nil {
		out.Error = env.Error
	}
	return out
}

func testSpec() vmtypes.VMSpec {
	return vmtypes.VMSpec{
		ID: vmID, ImageSlug: "ubuntu-24.04-xfce", ImageVersion: "2026.11.1",
		RAMMB: 2048, VCPUs: 1, DiskGB: 40, Hostname: "zynthera-111111", Username: "tester",
		PasswordHash: testHash, BridgeToken: strings.Repeat("a", 40),
	}
}

func TestHealthNeedsNoToken(t *testing.T) {
	srv := newTestServer(t, mock.Delays{})
	res := call(t, srv, "", "GET", "/health", nil)
	if res.Status != 200 || !strings.Contains(res.Raw, `"driver":"mock"`) {
		t.Fatalf("health: %d %s", res.Status, res.Raw)
	}
}

func TestControlPlaneRoutesNeedTheToken(t *testing.T) {
	srv := newTestServer(t, mock.Delays{})
	routes := []struct{ method, path string }{
		{"GET", "/capacity"},
		{"GET", "/vms"},
		{"POST", "/vms"},
		{"GET", "/vms/" + vmID},
		{"POST", "/vms/" + vmID + "/start"},
		{"DELETE", "/vms/" + vmID},
		{"GET", "/vms/" + vmID + "/snapshots"},
	}
	for _, rt := range routes {
		for name, token := range map[string]string{"no token": "", "wrong token": "nope", "prefix of the token": testToken[:5]} {
			res := call(t, srv, token, rt.method, rt.path, nil)
			if res.Status != 401 || res.Error.Code != "unauthenticated" {
				t.Errorf("%s %s with %s: %d %s", rt.method, rt.path, name, res.Status, res.Raw)
			}
		}
	}
}

func TestLifecycleOverHTTP(t *testing.T) {
	srv := newTestServer(t, mock.Delays{})
	do := func(method, path string, body any) result {
		t.Helper()
		res := call(t, srv, testToken, method, path, body)
		if res.Status != 200 {
			t.Fatalf("%s %s: %d %s", method, path, res.Status, res.Raw)
		}
		return res
	}
	status := func() vmtypes.VMStatus {
		t.Helper()
		var st vmtypes.VMStatus
		if err := json.Unmarshal([]byte(do("GET", "/vms/"+vmID, nil).Raw), &st); err != nil {
			t.Fatal(err)
		}
		return st
	}

	if !strings.Contains(do("GET", "/capacity", nil).Raw, `"total_ram_mb":32768`) {
		t.Error("capacity lacks total_ram_mb")
	}
	do("POST", "/vms", testSpec())
	if st := status(); st.Status != vmtypes.StatusStopped {
		t.Fatalf("after create: %+v", st)
	}
	do("POST", "/vms/"+vmID+"/start", nil)
	if st := status(); st.Status != vmtypes.StatusRunning {
		t.Fatalf("after start: %+v", st)
	}
	do("POST", "/vms/"+vmID+"/suspend", nil)
	if st := status(); st.Status != vmtypes.StatusSuspended || !st.HasSavedState {
		t.Fatalf("after suspend: %+v", st)
	}

	snap := "22222222-2222-4222-8222-222222222222"
	do("POST", "/vms/"+vmID+"/snapshots", map[string]string{"snapshot_id": snap})
	if !strings.Contains(do("GET", "/vms/"+vmID+"/snapshots", nil).Raw, snap) {
		t.Error("snapshot not listed")
	}
	do("POST", "/vms/"+vmID+"/snapshots/"+snap+"/restore", nil)
	if st := status(); st.Status != vmtypes.StatusStopped || st.HasSavedState {
		t.Fatalf("after restore: %+v", st)
	}
	do("POST", "/vms/"+vmID+"/resize", vmtypes.ResizeRequest{RAMMB: 4096, VCPUs: 2, DiskGB: 80})
	do("DELETE", "/vms/"+vmID+"/snapshots/"+snap, nil)

	if !strings.Contains(do("GET", "/vms", nil).Raw, vmID) {
		t.Error("vm not in list")
	}
	do("DELETE", "/vms/"+vmID, nil)
	if st := status(); st.Status != vmtypes.StatusMissing {
		t.Fatalf("after delete: %+v", st)
	}
}

func TestErrorMapping(t *testing.T) {
	srv := newTestServer(t, mock.Delays{}, "suspend")
	post := func(path string, body any) result { return call(t, srv, testToken, "POST", path, body) }

	if res := post("/vms/"+vmID+"/start", nil); res.Status != 404 || res.Error.Code != "not_found" {
		t.Errorf("start of an unknown vm: %d %s", res.Status, res.Raw)
	}
	if res := post("/vms", testSpec()); res.Status != 200 {
		t.Fatalf("create: %d %s", res.Status, res.Raw)
	}
	if res := post("/vms/"+vmID+"/reboot", nil); res.Status != 409 || res.Error.Code != "invalid_state" {
		t.Errorf("reboot of a stopped vm: %d %s", res.Status, res.Raw)
	}
	post("/vms/"+vmID+"/start", nil)
	res := post("/vms/"+vmID+"/suspend", nil)
	if res.Status != 500 || res.Error.Code != "driver_error" || res.Error.Details["detail"] == nil {
		t.Errorf("injected failure: %d %s", res.Status, res.Raw)
	}
}

func TestIDsAndBodiesAreValidated(t *testing.T) {
	srv := newTestServer(t, mock.Delays{})
	post := func(path string, body any) result { return call(t, srv, testToken, "POST", path, body) }

	for _, bad := range []string{"not-a-uuid", "..%2F..%2Fetc", "11111111-1111-4111-8111-11111111111", strings.ToUpper("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")} {
		if res := post("/vms/"+bad+"/start", nil); res.Status != 400 && res.Status != 404 {
			t.Errorf("vm id %q: %d %s", bad, res.Status, res.Raw)
		}
	}

	spec := testSpec()
	spec.Username = "root; rm -rf /"
	if res := post("/vms", spec); res.Status != 422 || res.Error.Code != "validation_failed" {
		t.Errorf("bad username: %d %s", res.Status, res.Raw)
	}

	post("/vms", testSpec())
	if res := post("/vms/"+vmID+"/snapshots", map[string]string{"snapshot_id": "../x"}); res.Status != 400 {
		t.Errorf("bad snapshot id: %d %s", res.Status, res.Raw)
	}
	if res := post("/vms/"+vmID+"/snapshots/not-a-uuid/restore", nil); res.Status != 400 {
		t.Errorf("bad snapshot id in path: %d %s", res.Status, res.Raw)
	}
	if res := post("/vms/"+vmID+"/resize", map[string]int{"ram_mb": 0}); res.Status != 422 {
		t.Errorf("empty resize: %d %s", res.Status, res.Raw)
	}
}

func TestSecondOperationOnABusyVMIsRefused(t *testing.T) {
	srv := newTestServer(t, mock.Delays{Start: 400 * time.Millisecond})
	if res := call(t, srv, testToken, "POST", "/vms", testSpec()); res.Status != 200 {
		t.Fatalf("create: %d %s", res.Status, res.Raw)
	}

	var wg sync.WaitGroup
	first := make(chan result, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		first <- call(t, srv, testToken, "POST", "/vms/"+vmID+"/start", nil)
	}()

	// Give the first request time to take the lock.
	time.Sleep(120 * time.Millisecond)
	second := call(t, srv, testToken, "POST", "/vms/"+vmID+"/suspend", nil)
	if second.Status != 409 || second.Error.Code != "busy" {
		t.Errorf("second operation: %d %s", second.Status, second.Raw)
	}
	// Reading status is never blocked by the lock.
	if res := call(t, srv, testToken, "GET", "/vms/"+vmID, nil); res.Status != 200 {
		t.Errorf("status while busy: %d", res.Status)
	}

	wg.Wait()
	if res := <-first; res.Status != 200 {
		t.Errorf("first operation: %d %s", res.Status, res.Raw)
	}
	// The lock is released afterwards.
	if res := call(t, srv, testToken, "POST", "/vms/"+vmID+"/suspend", nil); res.Status != 200 {
		t.Errorf("after the first finished: %d %s", res.Status, res.Raw)
	}
}
