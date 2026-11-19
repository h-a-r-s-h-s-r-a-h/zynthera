package jobs

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// slogAdapter routes asynq's own log lines into our structured log. Its
// debug and info chatter is dropped.
type slogAdapter struct{ log *slog.Logger }

func (a slogAdapter) Debug(...any)      {}
func (a slogAdapter) Info(...any)       {}
func (a slogAdapter) Warn(args ...any)  { a.log.Warn("asynq: " + fmt.Sprint(args...)) }
func (a slogAdapter) Error(args ...any) { a.log.Error("asynq: " + fmt.Sprint(args...)) }
func (a slogAdapter) Fatal(args ...any) { a.log.Error("asynq fatal: " + fmt.Sprint(args...)) }

// retryDelay keeps retries quick. While a task waits for its retry the VM
// stays locked in its transitional state, so long back-offs would only make
// the user wait.
func retryDelay(n int, _ error, _ *asynq.Task) time.Duration {
	d := time.Duration(1<<min(n, 5)) * time.Second
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// ServerOptions tunes the job server.
type ServerOptions struct {
	// Concurrency is how many tasks run at once. Zero means 10.
	Concurrency int
	// PollInterval is how often idle workers look for new tasks, and how
	// often tasks waiting for a retry are checked. It is the delay a user
	// can feel between clicking a button and the job starting. Zero means
	// 200 milliseconds.
	PollInterval time.Duration
}

// NewServer builds the asynq server with the three queues. critical is
// served first, but the weights keep the others from starving.
func NewServer(rdb *redis.Client, log *slog.Logger, opts ServerOptions) *asynq.Server {
	if opts.Concurrency <= 0 {
		opts.Concurrency = 10
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 200 * time.Millisecond
	}
	return asynq.NewServerFromRedisClient(rdb, asynq.Config{
		Concurrency:              opts.Concurrency,
		TaskCheckInterval:        opts.PollInterval,
		DelayedTaskCheckInterval: opts.PollInterval,
		Queues:                   map[string]int{QueueCritical: 6, QueueDefault: 3, QueueLow: 1},
		RetryDelayFunc:           retryDelay,
		Logger:                   slogAdapter{log},
		ShutdownTimeout:          20 * time.Second,
	})
}

// periodic is one row of the periodic task table in spec section 10.5.
var periodic = []struct {
	spec     string
	taskType string
	timeout  time.Duration
}{
	{"@every 1m", TypeCronIdleSweep, 50 * time.Second},
	{"@every 30s", TypeCronReconcile, 25 * time.Second},
	{"@every 2m", TypeCronStuckVMs, 100 * time.Second},
	{"@every 1h", TypeCronSessionCleanup, 5 * time.Minute},
}

// NewScheduler builds the periodic task scheduler. Only one instance may run
// in the whole deployment, or every periodic task would be queued twice.
func NewScheduler(rdb *redis.Client, log *slog.Logger) (*asynq.Scheduler, error) {
	s := asynq.NewSchedulerFromRedisClient(rdb, &asynq.SchedulerOpts{Logger: slogAdapter{log}})
	for _, p := range periodic {
		_, err := s.Register(p.spec, asynq.NewTask(p.taskType, nil),
			asynq.Queue(QueueLow),
			asynq.MaxRetry(0),
			asynq.Timeout(p.timeout),
			// If a run is still queued or active, skip this tick instead
			// of piling up.
			asynq.Unique(p.timeout),
		)
		if err != nil {
			return nil, fmt.Errorf("jobs: register %s: %w", p.taskType, err)
		}
	}
	return s, nil
}
