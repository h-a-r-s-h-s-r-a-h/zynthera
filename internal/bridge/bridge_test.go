package bridge

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const token = "bridge-token"

type env struct {
	t       *testing.T
	home    string
	outside string
	srv     *httptest.Server
}

// newEnv builds a home directory with a few files, plus a directory next to
// it that must never be reachable.
func newEnv(t *testing.T, maxUpload int64) *env {
	t.Helper()
	root := t.TempDir()
	home, outside := filepath.Join(root, "home", "alex"), filepath.Join(root, "outside")
	for _, dir := range []string{filepath.Join(home, "Documents"), outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(home, "notes.txt"):            "hello",
		filepath.Join(home, "Documents", "plan.md"): "the plan",
		filepath.Join(home, ".bashrc"):              "hidden",
		filepath.Join(outside, "secret.txt"):        "TOP SECRET",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Symlinks: one that stays inside, two that try to leave.
	links := map[string]string{
		filepath.Join(home, "plan-link"):       filepath.Join(home, "Documents", "plan.md"),
		filepath.Join(home, "escape-file"):     filepath.Join(outside, "secret.txt"),
		filepath.Join(home, "escape-dir"):      outside,
		filepath.Join(home, "Documents", "up"): root,
		filepath.Join(home, "dangling"):        filepath.Join(root, "nowhere"),
	}
	for link, target := range links {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(home, token, maxUpload)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &env{t: t, home: home, outside: outside, srv: srv}
}

func (e *env) do(method, pathAndQuery, tok string, body io.Reader) (int, http.Header, string) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+pathAndQuery, body)
	if err != nil {
		e.t.Fatal(err)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, string(raw)
}

func (e *env) get(pathAndQuery string) (int, http.Header, string) {
	return e.do("GET", pathAndQuery, token, nil)
}

func q(key, value string) string { return key + "=" + url.QueryEscape(value) }

func TestTokenIsRequiredEverywhere(t *testing.T) {
	e := newEnv(t, 0)
	for _, path := range []string{"/v1/health", "/v1/list?path=", "/v1/download?path=notes.txt"} {
		for name, tok := range map[string]string{"none": "", "wrong": "nope", "prefix": token[:4]} {
			if status, _, _ := e.do("GET", path, tok, nil); status != 401 {
				t.Errorf("GET %s with %s token: %d", path, name, status)
			}
		}
	}
	if status, _, _ := e.do("PUT", "/v1/upload?name=x.txt", "", strings.NewReader("x")); status != 401 {
		t.Errorf("upload without token: %d", status)
	}
	if status, _, _ := e.get("/v1/health"); status != 200 {
		t.Errorf("health with the token: %d", status)
	}
}

func TestList(t *testing.T) {
	e := newEnv(t, 0)
	status, _, body := e.get("/v1/list?path=")
	if status != 200 {
		t.Fatalf("list: %d %s", status, body)
	}
	var out struct {
		Entries []Entry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range out.Entries {
		names = append(names, entry.Name+":"+entry.Type)
	}
	// Folders first; hidden files, links that leave home and dangling
	// links are not listed.
	if got := strings.Join(names, " "); got != "Documents:dir notes.txt:file plan-link:file" {
		t.Errorf("entries = %s", got)
	}
	if out.Entries[1].Size != 5 {
		t.Errorf("size of notes.txt = %d", out.Entries[1].Size)
	}

	_, _, hidden := e.get("/v1/list?path=&hidden=1")
	if !strings.Contains(hidden, ".bashrc") {
		t.Error("hidden files are not listed when asked for")
	}
	if status, _, _ := e.get("/v1/list?" + q("path", "notes.txt")); status != 400 {
		t.Errorf("listing a file: %d", status)
	}
	if status, _, _ := e.get("/v1/list?" + q("path", "no-such-dir")); status != 404 {
		t.Errorf("listing a missing folder: %d", status)
	}
}

func TestDownload(t *testing.T) {
	e := newEnv(t, 0)
	status, header, body := e.get("/v1/download?" + q("path", "Documents/plan.md"))
	if status != 200 || body != "the plan" {
		t.Fatalf("download: %d %q", status, body)
	}
	if cd := header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, "plan.md") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if header.Get("X-Content-Type-Options") != "nosniff" || header.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("unsafe content headers: %v", header)
	}
	// A link that stays inside home works.
	if status, _, body := e.get("/v1/download?" + q("path", "plan-link")); status != 200 || body != "the plan" {
		t.Errorf("download through an inside link: %d %q", status, body)
	}
	if status, _, _ := e.get("/v1/download?" + q("path", "Documents")); status != 400 {
		t.Errorf("downloading a folder: %d", status)
	}
	if status, _, _ := e.get("/v1/download?" + q("path", "nope.txt")); status != 404 {
		t.Errorf("downloading a missing file: %d", status)
	}
}

// Every way of naming a file outside the home directory must fail, and the
// secret must never appear in a response.
func TestPathsCannotLeaveHome(t *testing.T) {
	e := newEnv(t, 0)
	attempts := []string{
		"../../outside/secret.txt",
		"..",
		"../..",
		"Documents/../../../outside/secret.txt",
		"/etc/passwd",
		e.outside + "/secret.txt",
		"escape-file",
		"escape-dir/secret.txt",
		"escape-dir",
		"Documents/up/outside/secret.txt",
		"Documents/up",
		"./././../outside/secret.txt",
		"notes.txt/../../../outside/secret.txt",
		`..\..\outside\secret.txt`,
		"\x00/etc/passwd",
		"....//....//outside/secret.txt",
	}
	for _, attempt := range attempts {
		for _, endpoint := range []string{"download", "list"} {
			status, _, body := e.get("/v1/" + endpoint + "?" + q("path", attempt))
			if strings.Contains(body, "TOP SECRET") || strings.Contains(body, "secret.txt") || strings.Contains(body, "root:") {
				t.Errorf("%s %q leaked data: %d %s", endpoint, attempt, status, body)
			}
			if status == 200 && endpoint == "download" {
				t.Errorf("download %q succeeded", attempt)
			}
			// Paths are re-rooted at the home directory, the way a chroot
			// would: ".." from the top stays at the top. So a listing of
			// "../.." is a listing of home itself (checked above to leak
			// nothing), and "/etc/passwd" is simply a file that is not there.
			allowed := status == 403 || status == 404 || status == 400 || (endpoint == "list" && status == 200)
			if !allowed {
				t.Errorf("%s %q: unexpected status %d", endpoint, attempt, status)
			}
		}
	}
	// Separators that arrive percent-encoded are decoded by the URL parser
	// and treated like any others.
	for _, raw := range []string{"path=..%2F..%2Foutside%2Fsecret.txt", "path=%2e%2e%2f%2e%2e%2foutside%2fsecret.txt", "path=..%252F..%252Foutside"} {
		status, _, body := e.get("/v1/download?" + raw)
		if status == 200 || strings.Contains(body, "TOP SECRET") {
			t.Errorf("download with %s: %d %s", raw, status, body)
		}
	}
}

func TestUpload(t *testing.T) {
	e := newEnv(t, 0)
	put := func(name, content string) (int, string) {
		status, _, body := e.do("PUT", "/v1/upload?"+q("name", name), token, strings.NewReader(content))
		return status, body
	}
	read := func(name string) string {
		raw, err := os.ReadFile(filepath.Join(e.home, "Uploads", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	if status, body := put("report.pdf", "first"); status != 201 || !strings.Contains(body, `"name":"report.pdf"`) {
		t.Fatalf("upload: %d %s", status, body)
	}
	// The same name again does not overwrite.
	if status, body := put("report.pdf", "second"); status != 201 || !strings.Contains(body, `"name":"report (1).pdf"`) {
		t.Fatalf("second upload: %d %s", status, body)
	}
	if status, body := put("report.pdf", "third"); status != 201 || !strings.Contains(body, `"name":"report (2).pdf"`) {
		t.Fatalf("third upload: %d %s", status, body)
	}
	if read("report.pdf") != "first" || read("report (1).pdf") != "second" || read("report (2).pdf") != "third" {
		t.Error("uploads overwrote each other")
	}

	// Directories in the name are stripped: the file lands in Uploads.
	if status, body := put("../../outside/evil.txt", "x"); status != 201 || !strings.Contains(body, `"name":"evil.txt"`) {
		t.Errorf("upload with a path as name: %d %s", status, body)
	}
	if _, err := os.Stat(filepath.Join(e.outside, "evil.txt")); err == nil {
		t.Fatal("an upload was written outside the home directory")
	}
	if status, body := put(`C:\Users\x\photo.png`, "x"); status != 201 || !strings.Contains(body, `"name":"photo.png"`) {
		t.Errorf("upload with a Windows path as name: %d %s", status, body)
	}
	for _, bad := range []string{"", ".", "..", ".bashrc", "/", "..\\.."} {
		if status, _ := put(bad, "x"); status != 400 {
			t.Errorf("upload named %q: %d", bad, status)
		}
	}

	// Nothing temporary is left in the folder.
	entries, _ := os.ReadDir(filepath.Join(e.home, "Uploads"))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".upload-") {
			t.Errorf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestUploadLimitLeavesNothingBehind(t *testing.T) {
	e := newEnv(t, 16)
	status, _, body := e.do("PUT", "/v1/upload?name=big.bin", token, bytes.NewReader(make([]byte, 64)))
	if status != 413 {
		t.Fatalf("upload over the limit: %d %s", status, body)
	}
	entries, _ := os.ReadDir(filepath.Join(e.home, "Uploads"))
	if len(entries) != 0 {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("a refused upload left files behind: %v", names)
	}
	if status, _, _ := e.do("PUT", "/v1/upload?name=small.bin", token, bytes.NewReader(make([]byte, 16))); status != 201 {
		t.Errorf("upload exactly at the limit: %d", status)
	}
}

func TestUploadsFolderCannotBeRedirected(t *testing.T) {
	e := newEnv(t, 0)
	// The user (or something in the PC) replaced Uploads with a link.
	if err := os.Symlink(e.outside, filepath.Join(e.home, "Uploads")); err != nil {
		t.Fatal(err)
	}
	status, _, _ := e.do("PUT", "/v1/upload?name=x.txt", token, strings.NewReader("x"))
	if status != 403 {
		t.Errorf("upload through a redirected Uploads folder: %d", status)
	}
	if _, err := os.Stat(filepath.Join(e.outside, "x.txt")); err == nil {
		t.Fatal("an upload followed the link out of the home directory")
	}
}

func TestOnlyKnownMethods(t *testing.T) {
	e := newEnv(t, 0)
	if status, _, _ := e.do("DELETE", "/v1/download?path=notes.txt", token, nil); status != 405 {
		t.Errorf("DELETE: %d", status)
	}
	if status, _, _ := e.do("GET", "/v1/exec?cmd=id", token, nil); status != 404 {
		t.Errorf("unknown route: %d", status)
	}
}
