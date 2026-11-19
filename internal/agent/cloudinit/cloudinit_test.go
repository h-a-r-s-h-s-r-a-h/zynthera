package cloudinit

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"zynthera/internal/agent/run"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var params = Params{
	VMID:         "6f1c0b1e-2222-4333-8444-555555555555",
	Hostname:     "zynthera-6f1c0b",
	Username:     "alex",
	PasswordHash: "$6$zyntheratestsalt$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789./abcdefghijklmnopqrstuv",
	BridgeToken:  "bridge-token-0123456789abcdefghijklmnopqrstuv",
	BridgePort:   7070,
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go test ./internal/agent/cloudinit -update` to create it)", err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs from the golden file.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestGoldenFiles(t *testing.T) {
	user, err := UserData(params)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := MetaData(params)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "user-data.golden", user)
	golden(t, "meta-data.golden", meta)
	if !strings.HasPrefix(string(user), "#cloud-config\n") {
		t.Error("user-data does not start with #cloud-config")
	}
}

// Whatever ends up in the parameters, the document must parse back to
// exactly those values and nothing else may appear in it.
func TestValuesCannotBreakOutOfTheirField(t *testing.T) {
	hostile := params
	hostile.Username = "alex\nruncmd:\n  - [rm, -rf, /]\n#"
	hostile.Hostname = "host: {a: b}\n- injected"
	hostile.PasswordHash = "x'\"\n  sudo: ALL\n"
	hostile.BridgeToken = "tok\nZYN_EVIL=1"

	raw, err := UserData(hostile)
	if err != nil {
		t.Fatal(err)
	}
	var doc userData
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("hostile values produced invalid YAML: %v\n%s", err, raw)
	}
	if doc.Hostname != hostile.Hostname || doc.Users[0].Name != hostile.Username || doc.Users[0].Passwd != hostile.PasswordHash {
		t.Errorf("values did not survive the round trip")
	}
	// Still exactly the three commands we wrote, and one user.
	if len(doc.Runcmd) != 3 || len(doc.Users) != 1 || len(doc.WriteFiles) != 3 {
		t.Errorf("structure changed: %d commands, %d users, %d files", len(doc.Runcmd), len(doc.Users), len(doc.WriteFiles))
	}
	if doc.Users[0].Sudo != "ALL=(ALL:ALL) ALL" || doc.SSHPwauth {
		t.Errorf("fixed fields were altered: %+v", doc.Users[0])
	}
	// The generic top-level view must show only the keys we emit.
	var generic map[string]any
	if err := yaml.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	if len(generic) != 7 {
		t.Errorf("top-level keys = %d, want 7: %v", len(generic), generic)
	}
}

func TestMissingParametersAreRefused(t *testing.T) {
	for name, mutate := range map[string]func(*Params){
		"no username": func(p *Params) { p.Username = "" },
		"no hash":     func(p *Params) { p.PasswordHash = "" },
		"no token":    func(p *Params) { p.BridgeToken = "" },
		"no port":     func(p *Params) { p.BridgePort = 0 },
		"no hostname": func(p *Params) { p.Hostname = "" },
	} {
		p := params
		mutate(&p)
		if _, err := UserData(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBuildSeed(t *testing.T) {
	if _, err := exec.LookPath("cloud-localds"); err != nil {
		t.Skip("cloud-localds is not installed")
	}
	dir := t.TempDir()
	iso, err := BuildSeed(context.Background(), run.Exec{}, dir, params)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(iso)
	if err != nil || st.Size() == 0 {
		t.Fatalf("seed.iso: %v", err)
	}
	// Nothing but the ISO stays behind: the rendered files hold secrets.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "seed.iso" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("files left in the vm dir: %v", names)
	}
	// The seed carries the NoCloud volume label cloud-init looks for.
	raw, err := os.ReadFile(iso)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(string(raw)), "cidata") {
		t.Error("seed has no cidata label")
	}
}
