// Package domainxml builds the libvirt domain definition of a VM (spec
// section 12.2). The XML is produced by encoding/xml from structs, never by
// joining strings, so no value can break out of its element or attribute.
package domainxml

import (
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"

	"zynthera/internal/agent/storage"
	"zynthera/internal/shared/vmtypes"
)

// Params is everything a domain definition is built from.
type Params struct {
	ID       string // VM UUID; the domain is named zyn-<ID>
	RAMMB    int
	VCPUs    int
	Disk     storage.DiskRef
	SeedPath string // the cloud-init seed ISO
	MAC      string
	IP       string
	Network  string // libvirt network name
	// CPUMode is "host-model", "host-passthrough", or a named CPU model for
	// hosts that must be able to exchange saved VMs.
	CPUMode string
	// BandwidthKB caps the VM's network speed, in kilobytes per second.
	BandwidthKB int
}

// Name is the libvirt name of a VM's domain.
func Name(vmID string) string { return "zyn-" + vmID }

type domain struct {
	XMLName       xml.Name `xml:"domain"`
	Type          string   `xml:"type,attr"`
	Name          string   `xml:"name"`
	UUID          string   `xml:"uuid"`
	Memory        memory   `xml:"memory"`
	CurrentMemory memory   `xml:"currentMemory"`
	VCPU          vcpu     `xml:"vcpu"`
	OS            osConfig `xml:"os"`
	Features      features `xml:"features"`
	CPU           cpu      `xml:"cpu"`
	Clock         clock    `xml:"clock"`
	PM            pm       `xml:"pm"`
	OnPoweroff    string   `xml:"on_poweroff"`
	OnReboot      string   `xml:"on_reboot"`
	OnCrash       string   `xml:"on_crash"`
	Devices       devices  `xml:"devices"`
}

type memory struct {
	Unit  string `xml:"unit,attr"`
	Value int    `xml:",chardata"`
}

type vcpu struct {
	Placement string `xml:"placement,attr"`
	Value     int    `xml:",chardata"`
}

type osConfig struct {
	Type osType `xml:"type"`
	Boot boot   `xml:"boot"`
}

type osType struct {
	Arch    string `xml:"arch,attr"`
	Machine string `xml:"machine,attr"`
	Value   string `xml:",chardata"`
}

type boot struct {
	Dev string `xml:"dev,attr"`
}

type empty struct{}

type features struct {
	ACPI empty `xml:"acpi"`
	APIC empty `xml:"apic"`
}

type cpu struct {
	Mode  string `xml:"mode,attr"`
	Match string `xml:"match,attr,omitempty"`
	Model string `xml:"model,omitempty"`
}

type clock struct {
	Offset string  `xml:"offset,attr"`
	Timers []timer `xml:"timer"`
}

type timer struct {
	Name       string `xml:"name,attr"`
	TickPolicy string `xml:"tickpolicy,attr,omitempty"`
	Present    string `xml:"present,attr,omitempty"`
}

type enabled struct {
	Enabled string `xml:"enabled,attr"`
}

type pm struct {
	SuspendToMem  enabled `xml:"suspend-to-mem"`
	SuspendToDisk enabled `xml:"suspend-to-disk"`
}

type devices struct {
	Disks      []disk     `xml:"disk"`
	Interface  iface      `xml:"interface"`
	Channel    channel    `xml:"channel"`
	Input      input      `xml:"input"`
	Graphics   graphics   `xml:"graphics"`
	Video      video      `xml:"video"`
	RNG        rng        `xml:"rng"`
	MemBalloon memBalloon `xml:"memballoon"`
}

type disk struct {
	Type     string     `xml:"type,attr"`
	Device   string     `xml:"device,attr"`
	Driver   diskDriver `xml:"driver"`
	Source   diskSource `xml:"source"`
	Target   diskTarget `xml:"target"`
	ReadOnly *empty     `xml:"readonly"`
}

type diskDriver struct {
	Name    string `xml:"name,attr"`
	Type    string `xml:"type,attr"`
	Cache   string `xml:"cache,attr,omitempty"`
	Discard string `xml:"discard,attr,omitempty"`
}

type diskSource struct {
	File string `xml:"file,attr,omitempty"`
	Dev  string `xml:"dev,attr,omitempty"`
}

type diskTarget struct {
	Dev string `xml:"dev,attr"`
	Bus string `xml:"bus,attr"`
}

type iface struct {
	Type      string      `xml:"type,attr"`
	MAC       address     `xml:"mac"`
	Source    ifaceSource `xml:"source"`
	Model     typed       `xml:"model"`
	Port      port        `xml:"port"`
	FilterRef filterRef   `xml:"filterref"`
	Bandwidth bandwidth   `xml:"bandwidth"`
}

type address struct {
	Address string `xml:"address,attr"`
}

type ifaceSource struct {
	Network string `xml:"network,attr"`
}

type typed struct {
	Type string `xml:"type,attr"`
}

type port struct {
	Isolated string `xml:"isolated,attr"`
}

type filterRef struct {
	Filter     string      `xml:"filter,attr"`
	Parameters []parameter `xml:"parameter"`
}

type parameter struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type bandwidth struct {
	Inbound  rate `xml:"inbound"`
	Outbound rate `xml:"outbound"`
}

type rate struct {
	Average int `xml:"average,attr"`
}

type channel struct {
	Type   string        `xml:"type,attr"`
	Target channelTarget `xml:"target"`
}

type channelTarget struct {
	Type string `xml:"type,attr"`
	Name string `xml:"name,attr"`
}

type input struct {
	Type string `xml:"type,attr"`
	Bus  string `xml:"bus,attr"`
}

type graphics struct {
	Type     string `xml:"type,attr"`
	Port     string `xml:"port,attr"`
	AutoPort string `xml:"autoport,attr"`
	Listen   string `xml:"listen,attr"`
}

type video struct {
	Model videoModel `xml:"model"`
}

type videoModel struct {
	Type       string     `xml:"type,attr"`
	Heads      string     `xml:"heads,attr"`
	Primary    string     `xml:"primary,attr"`
	Resolution resolution `xml:"resolution"`
}

type resolution struct {
	X int `xml:"x,attr"`
	Y int `xml:"y,attr"`
}

type rng struct {
	Model   string     `xml:"model,attr"`
	Backend rngBackend `xml:"backend"`
}

type rngBackend struct {
	Model string `xml:"model,attr"`
	Value string `xml:",chardata"`
}

type memBalloon struct {
	Model string `xml:"model,attr"`
}

func validate(p Params) error {
	switch {
	case !vmtypes.IsUUID(p.ID):
		return errors.New("domainxml: id must be a UUID")
	case p.RAMMB < 256 || p.VCPUs < 1:
		return errors.New("domainxml: ram and vcpus must be positive")
	case p.Disk.Path == "" || p.SeedPath == "":
		return errors.New("domainxml: disk and seed paths are required")
	case p.Disk.Kind != "file" && p.Disk.Kind != "block":
		return fmt.Errorf("domainxml: unknown disk kind %q", p.Disk.Kind)
	case p.Disk.Format != "qcow2" && p.Disk.Format != "raw":
		return fmt.Errorf("domainxml: unknown disk format %q", p.Disk.Format)
	case p.MAC == "" || p.IP == "" || p.Network == "":
		return errors.New("domainxml: mac, ip and network are required")
	case p.CPUMode == "":
		return errors.New("domainxml: cpu mode is required")
	case p.BandwidthKB < 1:
		return errors.New("domainxml: bandwidth must be positive")
	}
	return nil
}

// Build returns the domain XML.
func Build(p Params) ([]byte, error) {
	if err := validate(p); err != nil {
		return nil, err
	}

	// A named model keeps the virtual CPU identical on every host, which a
	// saved memory image needs in order to be restored elsewhere.
	cpuDef := cpu{Mode: p.CPUMode}
	if p.CPUMode != "host-model" && p.CPUMode != "host-passthrough" {
		cpuDef = cpu{Mode: "custom", Match: "exact", Model: p.CPUMode}
	}

	system := disk{
		Type:   p.Disk.Kind,
		Device: "disk",
		Driver: diskDriver{Name: "qemu", Type: p.Disk.Format, Cache: "none", Discard: "unmap"},
		Target: diskTarget{Dev: "vda", Bus: "virtio"},
	}
	if p.Disk.Kind == "block" {
		system.Source.Dev = p.Disk.Path
	} else {
		system.Source.File = p.Disk.Path
	}
	// The seed stays attached for the life of the VM: a saved memory image
	// can only be restored into the same virtual hardware.
	seed := disk{
		Type:     "file",
		Device:   "cdrom",
		Driver:   diskDriver{Name: "qemu", Type: "raw"},
		Source:   diskSource{File: p.SeedPath},
		Target:   diskTarget{Dev: "sda", Bus: "sata"},
		ReadOnly: &empty{},
	}

	d := domain{
		Type:          "kvm",
		Name:          Name(p.ID),
		UUID:          p.ID,
		Memory:        memory{Unit: "MiB", Value: p.RAMMB},
		CurrentMemory: memory{Unit: "MiB", Value: p.RAMMB},
		VCPU:          vcpu{Placement: "static", Value: p.VCPUs},
		OS:            osConfig{Type: osType{Arch: "x86_64", Machine: "q35", Value: "hvm"}, Boot: boot{Dev: "hd"}},
		CPU:           cpuDef,
		Clock: clock{Offset: "utc", Timers: []timer{
			{Name: "rtc", TickPolicy: "catchup"},
			{Name: "pit", TickPolicy: "delay"},
			{Name: "hpet", Present: "no"},
		}},
		// The host does the suspending. Guest-level sleep would fight it.
		PM:         pm{SuspendToMem: enabled{"no"}, SuspendToDisk: enabled{"no"}},
		OnPoweroff: "destroy",
		OnReboot:   "restart",
		OnCrash:    "destroy",
		Devices: devices{
			Disks: []disk{system, seed},
			Interface: iface{
				Type:   "network",
				MAC:    address{p.MAC},
				Source: ifaceSource{p.Network},
				Model:  typed{"virtio"},
				// VMs cannot talk to each other...
				Port: port{Isolated: "yes"},
				// ...and cannot fake another VM's address.
				FilterRef: filterRef{Filter: "clean-traffic", Parameters: []parameter{{Name: "IP", Value: p.IP}}},
				Bandwidth: bandwidth{Inbound: rate{p.BandwidthKB}, Outbound: rate{p.BandwidthKB}},
			},
			Channel: channel{Type: "unix", Target: channelTarget{Type: "virtio", Name: "org.qemu.guest_agent.0"}},
			// Without a tablet the pointer in a VNC session drifts away
			// from the real pointer.
			Input: input{Type: "tablet", Bus: "usb"},
			// Loopback only: guests cannot reach it and nothing outside the
			// host can. The agent's proxy is the only way in.
			Graphics: graphics{Type: "vnc", Port: "-1", AutoPort: "yes", Listen: "127.0.0.1"},
			Video: video{Model: videoModel{
				Type: "virtio", Heads: "1", Primary: "yes", Resolution: resolution{X: 1920, Y: 1080},
			}},
			RNG:        rng{Model: "virtio", Backend: rngBackend{Model: "random", Value: "/dev/urandom"}},
			MemBalloon: memBalloon{Model: "none"},
		},
	}

	out, err := xml.MarshalIndent(d, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("domainxml: encode: %w", err)
	}
	return append(out, '\n'), nil
}

// DHCPHost is the static DHCP entry that pins a VM's IP address:
// <host mac='…' ip='…'/>. It is passed to `virsh net-update`.
func DHCPHost(mac, ip string) (string, error) {
	type host struct {
		XMLName xml.Name `xml:"host"`
		MAC     string   `xml:"mac,attr"`
		IP      string   `xml:"ip,attr"`
	}
	out, err := xml.Marshal(host{MAC: mac, IP: ip})
	if err != nil {
		return "", fmt.Errorf("domainxml: dhcp host: %w", err)
	}
	return string(out), nil
}

// VNCPort turns the output of `virsh vncdisplay` ("127.0.0.1:3" or ":3")
// into a TCP address: the port is 5900 plus the display number.
func VNCPort(display string) (string, error) {
	for i := len(display) - 1; i >= 0; i-- {
		if display[i] == ':' {
			n, err := strconv.Atoi(display[i+1:])
			if err != nil || n < 0 || n > 59635 {
				break
			}
			return "127.0.0.1:" + strconv.Itoa(5900+n), nil
		}
	}
	return "", fmt.Errorf("domainxml: cannot parse vnc display %q", display)
}
