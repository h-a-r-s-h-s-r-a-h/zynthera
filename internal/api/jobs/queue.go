// Package jobs runs the slow parts of the VM lifecycle in the background.
// An HTTP handler moves a VM into a transitional state and queues a task; a
// worker here calls the host agent and settles the VM into its next state.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

// Task types (spec section 10.5).
const (
	TypeProvision       = "vm:provision"
	TypeStart           = "vm:start"
	TypeSuspend         = "vm:suspend"
	TypeShutdown        = "vm:shutdown"
	TypeForceStop       = "vm:force_stop"
	TypeReboot          = "vm:reboot"
	TypeDiscard         = "vm:discard_saved_state"
	TypeResize          = "vm:resize"
	TypeDelete          = "vm:delete"
	TypeSnapshotCreate  = "snapshot:create"
	TypeSnapshotRestore = "snapshot:restore"
	TypeSnapshotDelete  = "snapshot:delete"

	TypeCronIdleSweep      = "cron:idle_sweep"
	TypeCronReconcile      = "cron:reconcile"
	TypeCronStuckVMs       = "cron:stuck_vms"
	TypeCronSessionCleanup = "cron:session_cleanup"
)

// Queue names, most urgent first.
const (
	QueueCritical = "critical"
	QueueDefault  = "default"
	QueueLow      = "low"
)

// taskSpec is one row of the table in spec section 10.5.
type taskSpec struct {
	queue   string
	timeout time.Duration
	retries int
}

var taskSpecs = map[string]taskSpec{
	TypeProvision:       {QueueDefault, 10 * time.Minute, 2},
	TypeStart:           {QueueCritical, 5 * time.Minute, 1},
	TypeSuspend:         {QueueCritical, 10 * time.Minute, 2},
	TypeShutdown:        {QueueDefault, 3 * time.Minute, 0},
	TypeForceStop:       {QueueCritical, time.Minute, 3},
	TypeReboot:          {QueueDefault, 3 * time.Minute, 1},
	TypeDiscard:         {QueueDefault, time.Minute, 3},
	TypeResize:          {QueueDefault, 5 * time.Minute, 1},
	TypeDelete:          {QueueDefault, 5 * time.Minute, 5},
	TypeSnapshotCreate:  {QueueDefault, 10 * time.Minute, 1},
	TypeSnapshotRestore: {QueueDefault, 10 * time.Minute, 1},
	TypeSnapshotDelete:  {QueueDefault, 5 * time.Minute, 3},
}

// TaskTimeout returns how long a task may run. The stuck-VM check uses it.
func TaskTimeout(taskType string) time.Duration { return taskSpecs[taskType].timeout }

// Payload is the content of every VM task. It never contains a password or
// any other secret.
type Payload struct {
	VMID uuid.UUID `json:"vm_id"`
	// Actor is who asked: user, admin or system.
	Actor string `json:"actor"`
	// Reason of a suspend: user, logout, idle or admin.
	Reason string `json:"reason,omitempty"`
	// SnapshotID for snapshot tasks.
	SnapshotID uuid.UUID `json:"snapshot_id,omitempty"`
	// ReturnTo is the state a snapshot task puts the VM back into.
	ReturnTo string `json:"return_to,omitempty"`
	// New sizes for vm:resize.
	RAMMB  int `json:"ram_mb,omitempty"`
	VCPUs  int `json:"vcpus,omitempty"`
	DiskGB int `json:"disk_gb,omitempty"`
}

// Enqueuer queues VM tasks. Handlers depend on this interface.
type Enqueuer interface {
	Enqueue(ctx context.Context, taskType string, p Payload) error
}

// Queue is the asynq-backed Enqueuer.
type Queue struct {
	client *asynq.Client
}

// NewQueue wraps an asynq client.
func NewQueue(client *asynq.Client) *Queue { return &Queue{client: client} }

// Enqueue queues one task with the queue, timeout and retry count from the
// spec table.
func (q *Queue) Enqueue(ctx context.Context, taskType string, p Payload) error {
	spec, ok := taskSpecs[taskType]
	if !ok {
		return fmt.Errorf("jobs: unknown task type %q", taskType)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("jobs: encode payload: %w", err)
	}
	_, err = q.client.EnqueueContext(ctx, asynq.NewTask(taskType, raw),
		asynq.Queue(spec.queue),
		asynq.Timeout(spec.timeout),
		asynq.MaxRetry(spec.retries),
		// Finished tasks are kept briefly for debugging, then removed.
		asynq.Retention(10*time.Minute),
	)
	if err != nil {
		return fmt.Errorf("jobs: enqueue %s: %w", taskType, err)
	}
	return nil
}
