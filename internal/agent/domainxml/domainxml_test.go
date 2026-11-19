package domainxml

import (
	"encoding/xml"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"zynthera/internal/agent/storage"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func base() Params {
	return Params{
		ID:          "6f1c0b1e-2222-4333-8444-555555555555",
		RAMMB:       4096,
		VCPUs:       2,
		Disk:        storage.DiskRef{Kind: "file", Path: "/var/lib/zynthera/vms/6f1c0b1e-2222-4333-8444-555555555555/disk.qcow2", Format: "qcow2"},
		SeedPath:    "/var/lib/zynthera/vms/6f1c0b1e-2222-4333-8444-555555555555/seed.iso",
		MAC:         "52:54:00:ab:cd:ef",
		IP:          "10.210.0.10",
		Network:     "zynthera",
		CPUMode:     "host-model",
		BandwidthKB: 12500,
	}
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
		t.Fatalf("%v (run `go test ./internal/agent/domainxml -update` to create it)", err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs from the golden file.\n--- got ---\n%s", name, got)
	}
}

// validateWithLibvirt checks the XML against libvirt's own schema when the
// tool is installed.
func validateWithLibvirt(t *testing.T, doc []byte) {
	t.Helper()
	if _, err := exec.LookPath("virt-xml-validate"); err != nil {
		t.Log("virt-xml-validate is not installed; schema check skipped")
		return
	}
	path := filepath.Join(t.TempDir(), "domain.xml")
	if err := os.WriteFile(path, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("virt-xml-validate", path, "domain").CombinedOutput(); err != nil {
		t.Errorf("libvirt rejects the XML: %v\n%s", err, out)
	}
}

func TestGoldenQcow2(t *testing.T) {
	doc, err := Build(base())
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "qcow2.golden.xml", doc)
	validateWithLibvirt(t, doc)
}

func TestGoldenBlockDeviceAndNamedCPU(t *testing.T) {
	p := base()
	p.Disk = storage.DiskRef{Kind: "block", Path: "/dev/zvol/tank/zynthera/vms/6f1c0b1e-2222-4333-8444-555555555555", Format: "raw"}
	p.CPUMode = "Skylake-Client-noTSX-IBRS"
	doc, err := Build(p)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "zfs-named-cpu.golden.xml", doc)
	validateWithLibvirt(t, doc)
}

// The choices spec section 12.2 explains, checked one by one.
func TestSecurityRelevantChoices(t *testing.T) {
	raw, err := Build(base())
	if err != nil {
		t.Fatal(err)
	}
	var d domain
	if err := xml.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	dev := d.Devices
	checks := map[string]bool{
		"domain is named after our uuid":      d.Name == "zyn-6f1c0b1e-2222-4333-8444-555555555555" && d.UUID == base().ID,
		"vnc listens on loopback only":        dev.Graphics.Type == "vnc" && dev.Graphics.Listen == "127.0.0.1" && dev.Graphics.AutoPort == "yes",
		"vms are isolated from each other":    dev.Interface.Port.Isolated == "yes",
		"address spoofing is filtered":        dev.Interface.FilterRef.Filter == "clean-traffic" && dev.Interface.FilterRef.Parameters[0].Value == "10.210.0.10",
		"bandwidth is capped both ways":       dev.Interface.Bandwidth.Inbound.Average == 12500 && dev.Interface.Bandwidth.Outbound.Average == 12500,
		"tablet input":                        dev.Input.Type == "tablet",
		"no memory balloon":                   dev.MemBalloon.Model == "none",
		"guest sleep states are off":          d.PM.SuspendToMem.Enabled == "no" && d.PM.SuspendToDisk.Enabled == "no",
		"seed is attached read-only":          len(dev.Disks) == 2 && dev.Disks[1].Device == "cdrom" && dev.Disks[1].ReadOnly != nil,
		"system disk is virtio with discard":  dev.Disks[0].Target.Bus == "virtio" && dev.Disks[0].Driver.Discard == "unmap",
		"guest agent channel":                 dev.Channel.Target.Name == "org.qemu.guest_agent.0",
		"memory and current memory are equal": d.Memory.Value == 4096 && d.CurrentMemory.Value == 4096,
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("not satisfied: %s", name)
		}
	}
}

func TestHostileValuesStayInsideTheirAttribute(t *testing.T) {
	p := base()
	p.Network = `zynthera'/><graphics type='vnc' listen='0.0.0.0'/><x a='`
	p.Disk.Path = `/x'/></disk><disk type='file'><source file='/etc/shadow`
	raw, err := Build(p)
	if err != nil {
		t.Fatal(err)
	}
	var d domain
	if err := xml.Unmarshal(raw, &d); err != nil {
		t.Fatalf("hostile values produced invalid XML: %v", err)
	}
	if d.Devices.Interface.Source.Network != p.Network || d.Devices.Disks[0].Source.File != p.Disk.Path {
		t.Error("values did not survive the round trip")
	}
	if len(d.Devices.Disks) != 2 || d.Devices.Graphics.Listen != "127.0.0.1" {
		t.Error("a value changed the structure of the document")
	}
	if strings.Contains(string(raw), "listen='0.0.0.0'") {
		t.Error("an injected attribute appears unescaped")
	}
}

func TestInvalidParams(t *testing.T) {
	for name, mutate := range map[string]func(*Params){
		"id is not a uuid": func(p *Params) { p.ID = "../x" },
		"no ram":           func(p *Params) { p.RAMMB = 0 },
		"no vcpus":         func(p *Params) { p.VCPUs = 0 },
		"no disk":          func(p *Params) { p.Disk.Path = "" },
		"bad disk kind":    func(p *Params) { p.Disk.Kind = "floppy" },
		"bad disk format":  func(p *Params) { p.Disk.Format = "vmdk" },
		"no seed":          func(p *Params) { p.SeedPath = "" },
		"no mac":           func(p *Params) { p.MAC = "" },
		"no cpu mode":      func(p *Params) { p.CPUMode = "" },
		"no bandwidth":     func(p *Params) { p.BandwidthKB = 0 },
	} {
		p := base()
		mutate(&p)
		if _, err := Build(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDHCPHost(t *testing.T) {
	got, err := DHCPHost("52:54:00:ab:cd:ef", "10.210.0.10")
	if err != nil {
		t.Fatal(err)
	}
	if got != `<host mac="52:54:00:ab:cd:ef" ip="10.210.0.10"></host>` {
		t.Errorf("dhcp host = %s", got)
	}
}

func TestVNCPort(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:3\n": "",
		"127.0.0.1:3":   "127.0.0.1:5903",
		":0":            "127.0.0.1:5900",
		"127.0.0.1:12":  "127.0.0.1:5912",
	}
	for in, want := range cases {
		got, err := VNCPort(strings.TrimSpace(in))
		if want == "" {
			want = "127.0.0.1:5903"
		}
		if err != nil || got != want {
			t.Errorf("VNCPort(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "nonsense", "127.0.0.1:", "127.0.0.1:x", ":-1"} {
		if _, err := VNCPort(bad); err == nil {
			t.Errorf("VNCPort(%q) accepted", bad)
		}
	}
}
