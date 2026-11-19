// Command zynthera-api is the Zynthera control plane.
//
// Usage:
//
//	zynthera-api serve [--no-worker] [--no-scheduler]
//	zynthera-api migrate up | down [N] | version
//	zynthera-api seed
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"zynthera/internal/api/appconfig"
	"zynthera/internal/api/auth"
	"zynthera/internal/api/events"
	"zynthera/internal/api/httpapi"
	"zynthera/internal/api/jobs"
	"zynthera/internal/api/mail"
	"zynthera/internal/api/secrets"
	"zynthera/internal/api/store"
	"zynthera/internal/api/store/sqlcgen"
	"zynthera/internal/api/vmsvc"
	"zynthera/internal/shared/config"
	"zynthera/internal/shared/httpx"
	"zynthera/internal/shared/logging"
)

const usage = `usage:
  zynthera-api serve [--no-worker] [--no-scheduler]
  zynthera-api migrate up | down [N] | version
  zynthera-api seed`

func main() {
	log := logging.New("zynthera-api")
	if err := run(log, os.Args[1:]); err != nil {
		log.Error("fatal", "error", err.Error())
		os.Exit(1)
	}
}

func run(log *slog.Logger, args []string) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	// In development the settings come from .env in the working directory.
	// Real environment variables always take priority.
	if err := config.LoadDotEnv(".env"); err != nil {
		return err
	}
	cfg, err := appconfig.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch args[0] {
	case "serve":
		return serve(ctx, log, cfg, args[1:])
	case "migrate":
		return migrateCmd(log, cfg, args[1:])
	case "seed":
		return seed(ctx, log, cfg)
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}

type redisPinger struct{ c *redis.Client }

func (p redisPinger) Ping(ctx context.Context) error { return p.c.Ping(ctx).Err() }

func serve(ctx context.Context, log *slog.Logger, cfg appconfig.Config, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	// Both flags exist so the worker and scheduler can run as separate
	// processes later. They take effect once jobs arrive in milestone 3.
	noWorker := fs.Bool("no-worker", false, "do not run job workers in this process")
	noScheduler := fs.Bool("no-scheduler", false, "do not run the periodic task scheduler in this process")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("parse ZYN_REDIS_URL: %w", err)
	}
	rdb := redis.NewClient(redisOpts)
	defer func() { _ = rdb.Close() }()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("connect to redis: %w", err)
	}

	log.Info("starting", "env", cfg.Env, "worker", !*noWorker, "scheduler", !*noScheduler)

	box, err := secrets.New(cfg.SecretsKey)
	if err != nil {
		return err
	}

	q := sqlcgen.New(pool)
	bus := events.NewBus(rdb)
	svc := &vmsvc.Service{Pool: pool, Q: q, Config: cfg, Secrets: box, Logger: log, Events: bus}
	asynqClient := asynq.NewClientFromRedisClient(rdb)
	queue := jobs.NewQueue(asynqClient)

	if !*noWorker {
		worker := &jobs.Worker{Svc: svc, Queue: queue, Redis: rdb, Logger: log}
		srv := jobs.NewServer(rdb, log, jobs.ServerOptions{})
		if err := srv.Start(worker.Mux()); err != nil {
			return fmt.Errorf("start job workers: %w", err)
		}
		defer srv.Shutdown()
	}
	if !*noScheduler {
		sched, err := jobs.NewScheduler(rdb, log)
		if err != nil {
			return err
		}
		if err := sched.Start(); err != nil {
			return fmt.Errorf("start scheduler: %w", err)
		}
		defer sched.Shutdown()
	}

	router, err := httpapi.NewRouter(httpapi.Deps{
		Secrets: box,
		Svc:     svc,
		Queue:   queue,
		Bus:     bus,
		RDB:     rdb,
		Config:  cfg,
		Logger:  log,
		DB:      pool,
		Q:       q,
		Redis:   redisPinger{rdb},
		Limiter: auth.NewRedisLimiter(rdb),
		Mailer: mail.SMTPSender{
			Host: cfg.SMTPHost,
			Port: cfg.SMTPPort,
			User: cfg.SMTPUser,
			Pass: cfg.SMTPPass,
			From: cfg.MailFrom,
		},
	})
	if err != nil {
		return err
	}
	return httpx.Serve(ctx, log, cfg.Addr, router)
}

func migrateCmd(log *slog.Logger, cfg appconfig.Config, args []string) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "up":
		if err := store.MigrateUp(cfg.DatabaseURL); err != nil {
			return err
		}
	case "down":
		steps := 1
		if len(args) > 1 {
			n, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("migrate down: %q is not a number", args[1])
			}
			steps = n
		}
		if err := store.MigrateDown(cfg.DatabaseURL, steps); err != nil {
			return err
		}
	case "version":
	default:
		return fmt.Errorf("unknown migrate command %q\n%s", args[0], usage)
	}

	version, dirty, err := store.MigrateVersion(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	log.Info("schema version", "version", version, "dirty", dirty)
	return nil
}

// seed creates the data a fresh install needs. It is safe to run repeatedly.
// It gains more content with each milestone: the local host (M2), OS images (M3).
func seed(ctx context.Context, log *slog.Logger, cfg appconfig.Config) error {
	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	q := sqlcgen.New(pool)

	if cfg.SeedAdminPassword == "" {
		return errors.New("seed: ZYN_SEED_ADMIN_PASSWORD is required")
	}
	if n := len(cfg.SeedAdminPassword); n < auth.MinPasswordLength || n > auth.MaxPasswordLength {
		return fmt.Errorf("seed: ZYN_SEED_ADMIN_PASSWORD must be %d to %d characters", auth.MinPasswordLength, auth.MaxPasswordLength)
	}
	hash, err := auth.HashPassword(cfg.SeedAdminPassword, auth.DefaultArgon2Params)
	if err != nil {
		return err
	}
	admin, err := q.UpsertAdminUser(ctx, sqlcgen.UpsertAdminUserParams{Email: cfg.SeedAdminEmail, PasswordHash: hash})
	if err != nil {
		return fmt.Errorf("seed admin user: %w", err)
	}
	log.Info("seed: admin user ready", "email", admin.Email)

	if err := seedImages(ctx, q); err != nil {
		return err
	}
	log.Info("seed: OS images ready", "count", len(seedOSImages))

	if cfg.IsProduction() {
		return nil
	}
	return seedDevHost(ctx, log, cfg, q)
}

// seedDevHost registers the developer's own machine as the host "local", with
// the id and secrets from .env, so the API and the local agent already agree.
func seedDevHost(ctx context.Context, log *slog.Logger, cfg appconfig.Config, q *sqlcgen.Queries) error {
	if cfg.AgentHostID == "" || cfg.AgentToken == "" || cfg.AgentStreamSecret == "" {
		log.Info("seed: no development host (ZYN_AGENT_HOST_ID, ZYN_AGENT_TOKEN or ZYN_AGENT_STREAM_SECRET is empty)")
		return nil
	}
	id, err := uuid.Parse(cfg.AgentHostID)
	if err != nil {
		return fmt.Errorf("seed: ZYN_AGENT_HOST_ID is not a UUID: %w", err)
	}
	box, err := secrets.New(cfg.SecretsKey)
	if err != nil {
		return err
	}
	tokenEnc, err := box.Seal([]byte(cfg.AgentToken))
	if err != nil {
		return err
	}
	secretEnc, err := box.Seal([]byte(cfg.AgentStreamSecret))
	if err != nil {
		return err
	}
	agentURL := "http://" + cfg.AgentAddr
	host, err := q.UpsertHostWithID(ctx, sqlcgen.UpsertHostWithIDParams{
		ID:              id,
		Name:            "local",
		AgentUrl:        agentURL,
		PublicUrl:       agentURL,
		AgentTokenEnc:   tokenEnc,
		StreamSecretEnc: secretEnc,
	})
	if err != nil {
		return fmt.Errorf("seed development host: %w", err)
	}
	log.Info("seed: development host ready", "name", host.Name, "agent_url", host.AgentUrl)
	return nil
}

// seedOSImages are the V1 images (spec section 13). Only Ubuntu is enabled
// until the others have been built.
var seedOSImages = []sqlcgen.UpsertOSImageParams{
	{
		Slug: "ubuntu-24.04-xfce", DisplayName: "Ubuntu 24.04", CurrentVersion: "2026.11.1",
		Description: "Ubuntu 24.04 LTS with the XFCE desktop. The best-supported choice.",
		MinRamMb:    2048, MinDiskGb: 40, Enabled: true, SortOrder: 10,
	},
	{
		Slug: "debian-12-xfce", DisplayName: "Debian 12", CurrentVersion: "2026.11.1",
		Description: "Debian 12 with the XFCE desktop.",
		MinRamMb:    2048, MinDiskGb: 40, Enabled: false, SortOrder: 20,
	},
	{
		Slug: "fedora-xfce", DisplayName: "Fedora", CurrentVersion: "2026.11.1",
		Description: "Fedora with the XFCE desktop.",
		MinRamMb:    2048, MinDiskGb: 40, Enabled: false, SortOrder: 30,
	},
}

func seedImages(ctx context.Context, q *sqlcgen.Queries) error {
	for _, img := range seedOSImages {
		if err := q.UpsertOSImage(ctx, img); err != nil {
			return fmt.Errorf("seed image %s: %w", img.Slug, err)
		}
	}
	return nil
}
