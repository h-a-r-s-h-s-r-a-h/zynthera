// Package agentconfig is the typed configuration of zynthera-agent.
package agentconfig

import (
	"strings"

	"zynthera/internal/shared/config"
)

// Config holds every setting of the host agent (spec section 8.3).
type Config struct {
	Env          string
	Addr         string
	HostID       string
	Token        string
	StreamSecret string
	APIURL       string
	WebOrigin    string

	VMDriver      string // mock | libvirt
	StorageDriver string // qcow2 | zfs
	DataDir       string
	ZFSDataset    string

	LibvirtURI     string
	LibvirtNetwork string
	VMCPUMode      string
	VMBandwidthKB  int
	FileBridgePort int

	MockVNCAddr string
	MockFail    []string

	HeartbeatSeconds int
}

// IsProduction reports whether the agent runs on a real host for real users.
func (c Config) IsProduction() bool { return c.Env == "production" }

// Load reads and validates the configuration from the environment.
func Load() (Config, error) {
	var l config.Loader
	env := l.OneOf("ZYN_ENV", "development", "development", "production")
	defaultDataDir := "./.data/agent"
	if env == "production" {
		defaultDataDir = "/var/lib/zynthera"
	}

	c := Config{
		Env:          env,
		Addr:         l.String("ZYN_AGENT_ADDR", "127.0.0.1:9090"),
		HostID:       l.Required("ZYN_AGENT_HOST_ID"),
		Token:        l.Required("ZYN_AGENT_TOKEN"),
		StreamSecret: l.Required("ZYN_AGENT_STREAM_SECRET"),
		APIURL:       strings.TrimRight(l.String("ZYN_AGENT_API_URL", "http://127.0.0.1:8080"), "/"),
		WebOrigin:    strings.TrimRight(l.String("ZYN_AGENT_WEB_ORIGIN", "http://localhost:3000"), "/"),

		VMDriver:      l.OneOf("ZYN_VM_DRIVER", "mock", "mock", "libvirt"),
		StorageDriver: l.OneOf("ZYN_STORAGE_DRIVER", "qcow2", "qcow2", "zfs"),
		DataDir:       l.String("ZYN_DATA_DIR", defaultDataDir),
		ZFSDataset:    l.String("ZYN_ZFS_DATASET", "tank/zynthera"),

		LibvirtURI:     l.String("ZYN_LIBVIRT_URI", "qemu:///system"),
		LibvirtNetwork: l.String("ZYN_LIBVIRT_NETWORK", "zynthera"),
		VMCPUMode:      l.String("ZYN_VM_CPU_MODE", "host-model"),
		VMBandwidthKB:  l.Int("ZYN_VM_BANDWIDTH_KBYTES", 12500),
		FileBridgePort: l.Int("ZYN_FILE_BRIDGE_PORT", 7070),

		MockVNCAddr: l.String("ZYN_MOCK_VNC_ADDR", "127.0.0.1:5901"),

		HeartbeatSeconds: l.Int("ZYN_HEARTBEAT_SECONDS", 10),
	}

	for _, op := range strings.Split(l.String("ZYN_MOCK_FAIL", ""), ",") {
		if op = strings.TrimSpace(op); op != "" {
			c.MockFail = append(c.MockFail, op)
		}
	}
	if c.HeartbeatSeconds == 0 {
		l.Fail("ZYN_HEARTBEAT_SECONDS must be at least 1")
	}

	if c.IsProduction() {
		l.NotPlaceholder("ZYN_AGENT_TOKEN", []byte(c.Token))
		l.NotPlaceholder("ZYN_AGENT_STREAM_SECRET", []byte(c.StreamSecret))
		if c.VMDriver == "mock" {
			l.Fail("ZYN_VM_DRIVER=mock is not allowed in production")
		}
	}

	return c, l.Err()
}
