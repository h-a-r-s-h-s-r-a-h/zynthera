// Command zynthera-agent runs on each VM host. It is the only component that
// touches libvirt.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"zynthera/internal/agent/agentconfig"
	"zynthera/internal/agent/driver"
	"zynthera/internal/agent/driver/libvirt"
	"zynthera/internal/agent/driver/mock"
	"zynthera/internal/agent/files"
	"zynthera/internal/agent/heartbeat"
	"zynthera/internal/agent/netalloc"
	agentrun "zynthera/internal/agent/run"
	"zynthera/internal/agent/server"
	"zynthera/internal/agent/storage/qcow2"
	"zynthera/internal/agent/stream"
	"zynthera/internal/bridge"
	"zynthera/internal/shared/config"
	"zynthera/internal/shared/httpx"
	"zynthera/internal/shared/logging"
	"zynthera/internal/shared/vmtypes"
)

func main() {
	log := logging.New("zynthera-agent")
	if err := run(log); err != nil {
		log.Error("fatal", "error", err.Error())
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	// In development the settings come from .env in the working directory.
	// On a real host systemd loads /etc/zynthera/agent.env instead.
	if err := config.LoadDotEnv(".env"); err != nil {
		return err
	}
	cfg, err := agentconfig.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("starting", "env", cfg.Env, "version", server.Version, "vm_driver", cfg.VMDriver, "host_id", cfg.HostID)

	drv, resolver, err := newDriver(ctx, cfg)
	if err != nil {
		return err
	}
	fileProxy := files.New([]byte(cfg.StreamSecret), cfg.WebOrigin, drv, resolver, log, int64(cfg.MaxUploadMB)<<20)

	notifier := &streamNotifier{hostID: cfg.HostID, apiURL: cfg.APIURL, token: cfg.Token, log: log}
	proxy := stream.New([]byte(cfg.StreamSecret), cfg.WebOrigin, drv, log, notifier.notify)

	hb := &heartbeat.Sender{
		HostID:   cfg.HostID,
		Version:  server.Version,
		APIURL:   cfg.APIURL,
		Token:    cfg.Token,
		Interval: time.Duration(cfg.HeartbeatSeconds) * time.Second,
		Driver:   drv,
		Logger:   log,
		// Reported in every heartbeat, so a lost notification corrects itself.
		ViewerConnected: proxy.ViewerConnected,
	}
	go hb.Run(ctx)

	router := server.NewRouter(server.Deps{
		Config: cfg, Logger: log, Driver: drv,
		Stream:      proxy,
		Files:       fileProxy.Handler(),
		OnVMStopped: proxy.CloseVM,
	})
	return httpx.Serve(ctx, log, cfg.Addr, router)
}

// newDriver builds the VM driver and the way to reach each VM's file bridge.
func newDriver(ctx context.Context, cfg agentconfig.Config) (driver.Driver, files.Resolver, error) {
	switch cfg.VMDriver {
	case "mock":
		drv, err := mock.New(mock.Options{
			DataDir: cfg.DataDir,
			VNCAddr: cfg.MockVNCAddr,
			Fail:    cfg.MockFail,
			Delays:  mock.RealisticDelays,
			Images:  mock.DefaultImages,
		})
		if err != nil {
			return nil, nil, err
		}
		resolver, err := mockFileBridges(ctx, drv)
		return drv, resolver, err

	case "libvirt":
		if cfg.StorageDriver != "qcow2" {
			return nil, nil, fmt.Errorf("ZYN_STORAGE_DRIVER=%s is not available yet: the ZFS driver arrives in milestone 7", cfg.StorageDriver)
		}
		store, err := qcow2.New(cfg.DataDir, agentrun.Exec{})
		if err != nil {
			return nil, nil, err
		}
		// The DHCP range of the zynthera network (infra/host-setup/network.xml).
		alloc, err := netalloc.New(cfg.DataDir, "10.210.0.10", "10.210.255.250")
		if err != nil {
			return nil, nil, err
		}
		drv, err := libvirt.New(libvirt.Options{
			URI:         cfg.LibvirtURI,
			Network:     cfg.LibvirtNetwork,
			CPUMode:     cfg.VMCPUMode,
			BandwidthKB: cfg.VMBandwidthKB,
			BridgePort:  cfg.FileBridgePort,
			Store:       store,
			Alloc:       alloc,
			Runner:      agentrun.Exec{},
		})
		if err != nil {
			return nil, nil, err
		}
		return drv, files.GuestResolver(cfg.FileBridgePort, drv.GuestAddress, drv.BridgeToken), nil
	}
	return nil, nil, fmt.Errorf("unknown ZYN_VM_DRIVER %q", cfg.VMDriver)
}

// mockFileBridges runs the stand-in file bridges of mock VMs on a loopback
// port chosen by the system, and returns how to reach them.
func mockFileBridges(ctx context.Context, drv *mock.Driver) (files.Resolver, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("mock file bridge: %w", err)
	}
	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: drv.FileBridgeHandler(func(home, token string) (http.Handler, error) {
			b, err := bridge.New(home, token, 0)
			if err != nil {
				return nil, err
			}
			return b.Handler(), nil
		}),
	}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	base := "http://" + ln.Addr().String()
	return files.Static(func(_ context.Context, vmID string) (string, string, error) {
		return base + "/" + vmID, mock.MockBridgeToken, nil
	}), nil
}

// streamNotifier tells the control plane when a desktop viewer connects or
// leaves, so idle detection reacts at once instead of at the next heartbeat.
type streamNotifier struct {
	hostID string
	apiURL string
	token  string
	log    *slog.Logger
}

func (n *streamNotifier) notify(vmID, event string) {
	// Sent in the background: the stream must not wait for the control plane.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		raw, err := json.Marshal(vmtypes.StreamEvent{HostID: n.hostID, VMID: vmID, Event: event})
		if err != nil {
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.apiURL+"/internal/agent/stream-event", bytes.NewReader(raw))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+n.token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			n.log.Warn("stream event not delivered", "event", event, "vm_id", vmID, "error", err.Error())
			return
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			n.log.Warn("stream event refused", "event", event, "vm_id", vmID, "status", res.StatusCode)
		}
	}()
}
