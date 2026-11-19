// Package agentclient is the control plane's typed HTTP client for the host
// agent API (spec section 11.6).
package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"zynthera/internal/shared/httpx"
	"zynthera/internal/shared/vmtypes"
)

// Error is an error answer from the agent.
type Error struct {
	Status  int
	Code    string // busy, not_found, invalid_state, shutdown_timeout, driver_error, ...
	Message string
	// Detail is the driver's own error text. Log it; never show it to users.
	Detail string
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("agent: %s (%d): %s: %s", e.Code, e.Status, e.Message, e.Detail)
	}
	return fmt.Sprintf("agent: %s (%d): %s", e.Code, e.Status, e.Message)
}

// ErrUnavailable means the agent could not be reached at all.
var ErrUnavailable = errors.New("agent: host is unreachable")

// HasCode reports whether err is an agent Error with the given code.
func HasCode(err error, code string) bool {
	var ae *Error
	return errors.As(err, &ae) && ae.Code == code
}

// IsBusy reports whether the agent refused because another operation is
// running on the VM. The caller should retry a little later.
func IsBusy(err error) bool { return HasCode(err, "busy") }

// IsNotFound reports whether the VM or snapshot does not exist on the host.
func IsNotFound(err error) bool { return HasCode(err, "not_found") }

// IsInvalidState reports whether the operation is impossible in the VM's state.
func IsInvalidState(err error) bool { return HasCode(err, "invalid_state") }

// IsShutdownTimeout reports whether the guest ignored a clean shutdown.
func IsShutdownTimeout(err error) bool { return HasCode(err, "shutdown_timeout") }

// Client talks to one agent.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// sharedHTTP has no overall timeout: every call carries its own deadline in
// the context (the job's timeout), and VM operations can take minutes.
var sharedHTTP = &http.Client{
	Transport: &http.Transport{
		Proxy:               nil, // agents are always reached directly
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	},
}

// New returns a client for the agent at baseURL.
func New(baseURL, token string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: sharedHTTP}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("agent: encode request: %w", err)
		}
		rd = bytes.NewReader(raw)
	} else if method != http.MethodGet && method != http.MethodDelete {
		rd = strings.NewReader("{}")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return fmt.Errorf("agent: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	res, err := c.http.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("agent: %s %s: %w", method, path, ctxErr)
		}
		return fmt.Errorf("%w: %s %s: %w", ErrUnavailable, method, path, err)
	}
	defer func() { _ = res.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("agent: read response: %w", err)
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("agent: decode response of %s %s: %w", method, path, err)
		}
		return nil
	}

	var env httpx.ErrorBody
	if json.Unmarshal(raw, &env) == nil && env.Error.Code != "" {
		detail, _ := env.Error.Details["detail"].(string)
		return &Error{Status: res.StatusCode, Code: env.Error.Code, Message: env.Error.Message, Detail: detail}
	}
	// Not our envelope: a proxy in front of the agent answered.
	if res.StatusCode == http.StatusBadGateway || res.StatusCode == http.StatusServiceUnavailable || res.StatusCode == http.StatusGatewayTimeout {
		return fmt.Errorf("%w: %s %s: status %d", ErrUnavailable, method, path, res.StatusCode)
	}
	return &Error{Status: res.StatusCode, Code: "unexpected_response", Message: "unexpected answer from the agent"}
}

// Health checks that the agent answers.
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/health", nil, nil)
}

// Capacity returns the host's size.
func (c *Client) Capacity(ctx context.Context) (vmtypes.Capacity, error) {
	var out vmtypes.Capacity
	err := c.do(ctx, http.MethodGet, "/capacity", nil, &out)
	return out, err
}

// List returns every VM on the host.
func (c *Client) List(ctx context.Context) ([]vmtypes.VMStatus, error) {
	var out struct {
		VMs []vmtypes.VMStatus `json:"vms"`
	}
	err := c.do(ctx, http.MethodGet, "/vms", nil, &out)
	return out.VMs, err
}

// Create defines a new VM. It is safe to repeat.
func (c *Client) Create(ctx context.Context, spec vmtypes.VMSpec) error {
	return c.do(ctx, http.MethodPost, "/vms", spec, nil)
}

// Status returns one VM's status. A VM the host does not know has status
// vmtypes.StatusMissing.
func (c *Client) Status(ctx context.Context, id string) (vmtypes.VMStatus, error) {
	var out vmtypes.VMStatus
	err := c.do(ctx, http.MethodGet, "/vms/"+id, nil, &out)
	return out, err
}

// Start boots the VM, or resumes it when it has a saved state.
func (c *Client) Start(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/vms/"+id+"/start", nil, nil)
}

// Suspend saves the VM's memory to disk and stops it.
func (c *Client) Suspend(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/vms/"+id+"/suspend", nil, nil)
}

// Shutdown asks the guest to shut down and waits up to timeout.
func (c *Client) Shutdown(ctx context.Context, id string, timeout time.Duration) error {
	body := map[string]int{"timeout_seconds": int(timeout / time.Second)}
	return c.do(ctx, http.MethodPost, "/vms/"+id+"/shutdown", body, nil)
}

// ForceStop powers the VM off and discards any saved state.
func (c *Client) ForceStop(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/vms/"+id+"/force-stop", nil, nil)
}

// Reboot restarts the guest.
func (c *Client) Reboot(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/vms/"+id+"/reboot", nil, nil)
}

// DiscardSavedState removes the saved memory image.
func (c *Client) DiscardSavedState(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/vms/"+id+"/discard-saved-state", nil, nil)
}

// Resize changes RAM, vCPUs and disk of a VM that is off.
func (c *Client) Resize(ctx context.Context, id string, ramMB, vcpus, diskGB int) error {
	body := vmtypes.ResizeRequest{RAMMB: ramMB, VCPUs: vcpus, DiskGB: diskGB}
	return c.do(ctx, http.MethodPost, "/vms/"+id+"/resize", body, nil)
}

// SyncClock sets the guest clock after a resume.
func (c *Client) SyncClock(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/vms/"+id+"/sync-clock", nil, nil)
}

// Delete removes the VM and everything belonging to it from the host.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/vms/"+id, nil, nil)
}

// SnapshotList returns the VM's disk snapshots.
func (c *Client) SnapshotList(ctx context.Context, id string) ([]vmtypes.SnapshotInfo, error) {
	var out struct {
		Snapshots []vmtypes.SnapshotInfo `json:"snapshots"`
	}
	err := c.do(ctx, http.MethodGet, "/vms/"+id+"/snapshots", nil, &out)
	return out.Snapshots, err
}

// SnapshotCreate takes a disk snapshot.
func (c *Client) SnapshotCreate(ctx context.Context, id, snapshotID string) error {
	return c.do(ctx, http.MethodPost, "/vms/"+id+"/snapshots", map[string]string{"snapshot_id": snapshotID}, nil)
}

// SnapshotRestore rolls the disk back to a snapshot.
func (c *Client) SnapshotRestore(ctx context.Context, id, snapshotID string) error {
	return c.do(ctx, http.MethodPost, "/vms/"+id+"/snapshots/"+snapshotID+"/restore", nil, nil)
}

// SnapshotDelete removes a snapshot.
func (c *Client) SnapshotDelete(ctx context.Context, id, snapshotID string) error {
	return c.do(ctx, http.MethodDelete, "/vms/"+id+"/snapshots/"+snapshotID, nil, nil)
}
