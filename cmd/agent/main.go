// Command zynthera-agent runs on each VM host. It is the only component that
// touches libvirt.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"zynthera/internal/agent/agentconfig"
	"zynthera/internal/agent/driver"
	"zynthera/internal/agent/driver/mock"
	"zynthera/internal/agent/heartbeat"
	"zynthera/internal/agent/server"
	"zynthera/internal/shared/config"
	"zynthera/internal/shared/httpx"
	"zynthera/internal/shared/logging"
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

	drv, err := newDriver(cfg)
	if err != nil {
		return err
	}

	hb := &heartbeat.Sender{
		HostID:   cfg.HostID,
		Version:  server.Version,
		APIURL:   cfg.APIURL,
		Token:    cfg.Token,
		Interval: time.Duration(cfg.HeartbeatSeconds) * time.Second,
		Driver:   drv,
		Logger:   log,
	}
	go hb.Run(ctx)

	router := server.NewRouter(server.Deps{Config: cfg, Logger: log, Driver: drv})
	return httpx.Serve(ctx, log, cfg.Addr, router)
}

func newDriver(cfg agentconfig.Config) (driver.Driver, error) {
	switch cfg.VMDriver {
	case "mock":
		return mock.New(mock.Options{
			DataDir: cfg.DataDir,
			VNCAddr: cfg.MockVNCAddr,
			Fail:    cfg.MockFail,
			Delays:  mock.RealisticDelays,
			Images:  mock.DefaultImages,
		})
	default:
		return nil, fmt.Errorf("ZYN_VM_DRIVER=%s is not available yet: the libvirt driver arrives in milestone 5", cfg.VMDriver)
	}
}
