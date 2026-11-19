// Package heartbeat regularly tells the control plane that this host is
// alive, how big it is, which images it has and what its VMs are doing.
package heartbeat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"zynthera/internal/agent/driver"
	"zynthera/internal/shared/vmtypes"
)

// Sender posts heartbeats.
type Sender struct {
	HostID   string
	Version  string
	APIURL   string
	Token    string
	Interval time.Duration
	Driver   driver.Driver
	Logger   *slog.Logger
	// ViewerConnected reports whether a desktop viewer is attached to a VM.
	// Nil means "nobody is ever connected" (until the stream proxy exists).
	ViewerConnected func(vmID string) bool
	Client          *http.Client
}

// Build collects the current heartbeat payload from the driver.
func (s *Sender) Build(ctx context.Context) (vmtypes.Heartbeat, error) {
	capacity, err := s.Driver.Capacity(ctx)
	if err != nil {
		return vmtypes.Heartbeat{}, fmt.Errorf("capacity: %w", err)
	}
	images, err := s.Driver.Images(ctx)
	if err != nil {
		return vmtypes.Heartbeat{}, fmt.Errorf("images: %w", err)
	}
	vms, err := s.Driver.List(ctx)
	if err != nil {
		return vmtypes.Heartbeat{}, fmt.Errorf("list vms: %w", err)
	}
	if s.ViewerConnected != nil {
		for i := range vms {
			vms[i].ViewerConnected = s.ViewerConnected(vms[i].ID)
		}
	}
	if images == nil {
		images = []vmtypes.ImageRef{}
	}
	if vms == nil {
		vms = []vmtypes.VMStatus{}
	}
	return vmtypes.Heartbeat{
		HostID:       s.HostID,
		AgentVersion: s.Version,
		Capacity:     capacity,
		Images:       images,
		VMs:          vms,
	}, nil
}

// SendOnce builds and posts one heartbeat.
func (s *Sender) SendOnce(ctx context.Context) error {
	hb, err := s.Build(ctx)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(hb)
	if err != nil {
		return fmt.Errorf("encode heartbeat: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.APIURL+"/internal/agent/heartbeat", bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("build heartbeat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.Token)

	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post heartbeat: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("control plane answered %d to the heartbeat", res.StatusCode)
	}
	return nil
}

// Run sends a heartbeat immediately and then every Interval until ctx ends.
// A failed heartbeat is logged and the loop carries on: the control plane
// may simply be restarting.
func (s *Sender) Run(ctx context.Context) {
	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()
	healthy := true
	for {
		sendCtx, cancel := context.WithTimeout(ctx, s.Interval)
		err := s.SendOnce(sendCtx)
		cancel()
		switch {
		case err != nil && ctx.Err() == nil:
			s.Logger.Warn("heartbeat failed", "error", err.Error())
			healthy = false
		case err == nil && !healthy:
			s.Logger.Info("heartbeat recovered")
			healthy = true
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
