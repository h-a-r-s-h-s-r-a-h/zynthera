// Command zynthera-agent runs on each VM host. It is the only component that
// touches libvirt.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"zynthera/internal/agent/agentconfig"
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

	router := server.NewRouter(server.Deps{Config: cfg, Logger: log})
	return httpx.Serve(ctx, log, cfg.Addr, router)
}
