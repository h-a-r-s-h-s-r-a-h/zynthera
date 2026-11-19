// Package vmtypes holds the types exchanged between the control plane and the
// host agent (spec section 11.2).
package vmtypes

import "regexp"

// VMSpec describes a VM to create. JSON keys are snake_case.
type VMSpec struct {
	ID           string `json:"id"` // VM UUID
	ImageSlug    string `json:"image_slug"`
	ImageVersion string `json:"image_version"`
	RAMMB        int    `json:"ram_mb"`
	VCPUs        int    `json:"vcpus"`
	DiskGB       int    `json:"disk_gb"`
	Hostname     string `json:"hostname"`
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"` // SHA-512 crypt; never the plain password
	BridgeToken  string `json:"bridge_token"`  // secret for the guest file bridge
}

// VM statuses reported by the agent.
const (
	StatusRunning   = "running"
	StatusStopped   = "stopped"
	StatusSuspended = "suspended"
	StatusPaused    = "paused"
	StatusCrashed   = "crashed"
	StatusMissing   = "missing"
)

// VMStatus is what the agent knows about one VM right now.
type VMStatus struct {
	ID              string `json:"id"`
	Status          string `json:"status"` // one of the Status* constants
	HasSavedState   bool   `json:"has_saved_state"`
	ViewerConnected bool   `json:"viewer_connected"`
	DiskUsedBytes   int64  `json:"disk_used_bytes"`
	PrivateIP       string `json:"private_ip,omitempty"`
}

// DisplayEndpoint is where the VM's VNC server listens on the host.
type DisplayEndpoint struct {
	Network string `json:"network"` // "tcp" or "unix"
	Address string `json:"address"` // "127.0.0.1:5903" or a socket path
}

// Capacity is the size of a host.
type Capacity struct {
	TotalRAMMB  int `json:"total_ram_mb"`
	TotalVCPUs  int `json:"total_vcpus"`
	TotalDiskGB int `json:"total_disk_gb"`
	FreeDiskGB  int `json:"free_disk_gb"`
}

// ImageRef names one golden image version present on a host.
type ImageRef struct {
	Slug    string `json:"slug"`
	Version string `json:"version"`
}

// SnapshotInfo describes one disk snapshot of a VM.
type SnapshotInfo struct {
	ID        string `json:"id"`
	SizeBytes int64  `json:"size_bytes"`
}

// Heartbeat is posted by the agent to the control plane (spec section 10.7).
type Heartbeat struct {
	HostID       string     `json:"host_id"`
	AgentVersion string     `json:"agent_version"`
	Capacity     Capacity   `json:"capacity"`
	Images       []ImageRef `json:"images"`
	VMs          []VMStatus `json:"vms"`
}

// StreamEvent tells the control plane that a viewer connected or left.
type StreamEvent struct {
	HostID string `json:"host_id"` // which host is reporting, so the API can check its token
	VMID   string `json:"vm_id"`
	Event  string `json:"event"` // "connected" | "disconnected"
}

// ResizeRequest is the body of the agent's resize call.
type ResizeRequest struct {
	RAMMB  int `json:"ram_mb"`
	VCPUs  int `json:"vcpus"`
	DiskGB int `json:"disk_gb"`
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// IsUUID reports whether s is a lower-case UUID. Every id that reaches a file
// path, a libvirt name or a command line is checked with this first.
func IsUUID(s string) bool { return uuidRe.MatchString(s) }

// Patterns for the other VMSpec fields that end up on disk or in a guest.
var (
	imageSlugRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,62}$`)
	imageVersionRe = regexp.MustCompile(`^[0-9][0-9a-z.-]{0,30}$`)
	hostnameRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	usernameRe     = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,30}$`)
	cryptHashRe    = regexp.MustCompile(`^\$6\$(rounds=[0-9]{4,9}\$)?[./A-Za-z0-9]{1,16}\$[./A-Za-z0-9]{86}$`)
	bridgeTokenRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{32,128}$`)
)

// IsImageSlug reports whether s is a valid image slug.
func IsImageSlug(s string) bool { return imageSlugRe.MatchString(s) }

// IsImageVersion reports whether s is a valid image version.
func IsImageVersion(s string) bool { return imageVersionRe.MatchString(s) }

// IsLinuxUsername reports whether s is an acceptable guest username.
func IsLinuxUsername(s string) bool { return usernameRe.MatchString(s) }

// Validate checks a VMSpec received over the network. It returns a short
// reason, or "" when the spec is acceptable. The agent runs as root, so it
// never trusts that the control plane validated anything.
func (s VMSpec) Validate() string {
	switch {
	case !IsUUID(s.ID):
		return "id must be a UUID"
	case !IsImageSlug(s.ImageSlug):
		return "image_slug is invalid"
	case !IsImageVersion(s.ImageVersion):
		return "image_version is invalid"
	case s.RAMMB < 512 || s.RAMMB > 1024*1024:
		return "ram_mb is out of range"
	case s.VCPUs < 1 || s.VCPUs > 256:
		return "vcpus is out of range"
	case s.DiskGB < 1 || s.DiskGB > 16384:
		return "disk_gb is out of range"
	case !hostnameRe.MatchString(s.Hostname):
		return "hostname is invalid"
	case !IsLinuxUsername(s.Username):
		return "username is invalid"
	case !cryptHashRe.MatchString(s.PasswordHash):
		return "password_hash must be a SHA-512 crypt hash"
	case !bridgeTokenRe.MatchString(s.BridgeToken):
		return "bridge_token is invalid"
	}
	return ""
}
