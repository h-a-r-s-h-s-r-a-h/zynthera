// Package cloudinit renders the NoCloud seed that sets up a new VM at first
// boot: hostname, the user's Linux account, the file bridge and desktop
// autologin (spec section 12.6).
//
// Everything is produced by a YAML serializer from structs. No value is ever
// pasted into text, so nothing a user typed can break out of its field.
package cloudinit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"

	"zynthera/internal/agent/run"
)

// Params is what a seed is rendered from.
type Params struct {
	VMID     string
	Hostname string
	Username string
	// PasswordHash is a SHA-512 crypt hash. The plain password never
	// reaches the host.
	PasswordHash string
	BridgeToken  string
	BridgePort   int
}

type metaData struct {
	InstanceID    string `yaml:"instance-id"`
	LocalHostname string `yaml:"local-hostname"`
}

type user struct {
	Name       string   `yaml:"name"`
	Gecos      string   `yaml:"gecos"`
	Shell      string   `yaml:"shell"`
	Groups     []string `yaml:"groups,flow"`
	Sudo       string   `yaml:"sudo"`
	LockPasswd bool     `yaml:"lock_passwd"`
	Passwd     string   `yaml:"passwd"`
}

type growpart struct {
	Mode    string   `yaml:"mode"`
	Devices []string `yaml:"devices,flow"`
}

type writeFile struct {
	Path        string `yaml:"path"`
	Permissions string `yaml:"permissions,omitempty"`
	Content     string `yaml:"content"`
}

type userData struct {
	Hostname       string      `yaml:"hostname"`
	ManageEtcHosts bool        `yaml:"manage_etc_hosts"`
	SSHPwauth      bool        `yaml:"ssh_pwauth"`
	Users          []user      `yaml:"users"`
	Growpart       growpart    `yaml:"growpart"`
	WriteFiles     []writeFile `yaml:"write_files"`
	Runcmd         [][]string  `yaml:"runcmd"`
}

// MetaData renders the meta-data file.
func MetaData(p Params) ([]byte, error) {
	out, err := yaml.Marshal(metaData{InstanceID: p.VMID, LocalHostname: p.Hostname})
	if err != nil {
		return nil, fmt.Errorf("cloudinit: meta-data: %w", err)
	}
	return out, nil
}

// UserData renders the user-data file.
func UserData(p Params) ([]byte, error) {
	if p.VMID == "" || p.Hostname == "" || p.Username == "" || p.PasswordHash == "" || p.BridgeToken == "" || p.BridgePort <= 0 {
		return nil, errors.New("cloudinit: every parameter is required")
	}
	doc := userData{
		Hostname:       p.Hostname,
		ManageEtcHosts: true,
		SSHPwauth:      false,
		Users: []user{{
			Name:       p.Username,
			Gecos:      p.Username,
			Shell:      "/bin/bash",
			Groups:     []string{"sudo", "adm", "audio", "video", "plugdev"},
			Sudo:       "ALL=(ALL:ALL) ALL",
			LockPasswd: false,
			Passwd:     p.PasswordHash,
		}},
		Growpart: growpart{Mode: "auto", Devices: []string{"/"}},
		WriteFiles: []writeFile{
			{
				// Read by systemd as root (EnvironmentFile=) before the
				// service drops to the user, so 0600 root is enough.
				Path:        "/etc/zynthera/file-bridge.env",
				Permissions: "0600",
				Content:     "ZYN_BRIDGE_TOKEN=" + p.BridgeToken + "\nZYN_BRIDGE_PORT=" + strconv.Itoa(p.BridgePort) + "\n",
			},
			{
				Path:    "/etc/systemd/system/zynthera-file-bridge.service.d/10-user.conf",
				Content: "[Service]\nUser=" + p.Username + "\n",
			},
			{
				Path:    "/etc/lightdm/lightdm.conf.d/50-zynthera-autologin.conf",
				Content: "[Seat:*]\nautologin-user=" + p.Username + "\nautologin-user-timeout=0\n",
			},
		},
		// Each command is an argument list, so cloud-init runs it without
		// a shell.
		Runcmd: [][]string{
			{"systemctl", "daemon-reload"},
			{"systemctl", "enable", "--now", "zynthera-file-bridge.service"},
			{"systemctl", "restart", "lightdm.service"},
		},
	}
	body, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("cloudinit: user-data: %w", err)
	}
	return append([]byte("#cloud-config\n"), body...), nil
}

// BuildSeed writes <dir>/seed.iso from the parameters with cloud-localds.
// The rendered user-data contains the password hash and the bridge token, so
// it lives in a private temporary directory and is removed afterwards.
func BuildSeed(ctx context.Context, runner run.Runner, dir string, p Params) (string, error) {
	user, err := UserData(p)
	if err != nil {
		return "", err
	}
	meta, err := MetaData(p)
	if err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(dir, ".seed-")
	if err != nil {
		return "", fmt.Errorf("cloudinit: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	userPath, metaPath := filepath.Join(tmp, "user-data"), filepath.Join(tmp, "meta-data")
	if err := os.WriteFile(userPath, user, 0o600); err != nil {
		return "", fmt.Errorf("cloudinit: write user-data: %w", err)
	}
	if err := os.WriteFile(metaPath, meta, 0o600); err != nil {
		return "", fmt.Errorf("cloudinit: write meta-data: %w", err)
	}
	iso := filepath.Join(dir, "seed.iso")
	if _, err := runner.Output(ctx, "cloud-localds", iso, userPath, metaPath); err != nil {
		return "", fmt.Errorf("cloudinit: build seed: %w", err)
	}
	// The seed holds the password hash and the file bridge token, so only
	// its owner may read it. QEMU runs as another user and still can: when
	// the VM starts, libvirt hands every disk of the domain (this one too)
	// to that user, and takes it back afterwards.
	if err := os.Chmod(iso, 0o600); err != nil {
		return "", fmt.Errorf("cloudinit: chmod seed: %w", err)
	}
	return iso, nil
}
