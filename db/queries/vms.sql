-- name: InsertVM :one
INSERT INTO vms (user_id, host_id, os_image_id, image_version, name, ram_mb, vcpus, disk_gb,
                 linux_username, pending_password_hash, state)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'provisioning')
RETURNING *;

-- name: GetVM :one
SELECT * FROM vms WHERE id = $1 AND deleted_at IS NULL;

-- name: GetVMForUser :one
-- Ownership is part of the lookup, so someone else's VM looks exactly like a
-- VM that does not exist.
SELECT * FROM vms WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: ListVMsForUser :many
SELECT * FROM vms WHERE user_id = $1 AND deleted_at IS NULL ORDER BY created_at;

-- name: ListVMsByHost :many
SELECT * FROM vms WHERE host_id = $1 AND deleted_at IS NULL;

-- name: TransitionVM :one
-- The compare-and-set that starts every action. It moves a VM out of a
-- stable state only if it is still in one of the expected states. If no row
-- comes back, someone else got there first.
UPDATE vms
SET state = sqlc.arg('new_state'),
    state_changed_at = now(),
    state_detail = sqlc.narg('state_detail'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND deleted_at IS NULL
  AND state = ANY(sqlc.arg('from_states')::text[])
  AND (sqlc.narg('has_saved_state')::boolean IS NULL OR has_saved_state = sqlc.narg('has_saved_state')::boolean)
RETURNING *;

-- name: SettleVM :one
-- Used by jobs to leave a transitional state. It only applies while the VM
-- is still in the state the job expects.
UPDATE vms
SET state = sqlc.arg('new_state'),
    state_changed_at = now(),
    state_detail = sqlc.narg('state_detail'),
    has_saved_state = COALESCE(sqlc.narg('has_saved_state')::boolean, has_saved_state),
    last_active_at = COALESCE(sqlc.narg('last_active_at')::timestamptz, last_active_at),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND deleted_at IS NULL
  AND state = sqlc.arg('expected_state')
RETURNING *;

-- name: ForceVMState :one
-- Used by the reconciler and the stuck-VM check, which correct the record to
-- match what the host reports. Guarded by the state they looked at.
UPDATE vms
SET state = sqlc.arg('new_state'),
    state_changed_at = now(),
    state_detail = sqlc.narg('state_detail'),
    has_saved_state = sqlc.arg('has_saved_state'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND deleted_at IS NULL
  AND state = sqlc.arg('expected_state')
RETURNING *;

-- name: ClearPendingPassword :exec
UPDATE vms SET pending_password_hash = NULL, updated_at = now() WHERE id = $1;

-- name: UpdateVMName :one
UPDATE vms SET name = $2, updated_at = now() WHERE id = $1 AND deleted_at IS NULL RETURNING *;

-- name: SetVMKeepAlive :one
UPDATE vms SET keep_alive_until = $2, updated_at = now() WHERE id = $1 AND deleted_at IS NULL RETURNING *;

-- name: UpdateVMSize :exec
UPDATE vms SET ram_mb = $2, vcpus = $3, disk_gb = $4, updated_at = now() WHERE id = $1;

-- name: MarkVMDeleted :one
UPDATE vms
SET state = 'deleted', state_changed_at = now(), state_detail = NULL, deleted_at = now(),
    has_saved_state = false, viewer_connected = false, pending_password_hash = NULL, updated_at = now()
WHERE id = $1 AND state = 'deleting'
RETURNING *;

-- name: UpdateVMFromHeartbeat :exec
UPDATE vms
SET disk_used_bytes = $2,
    viewer_connected = $3,
    last_active_at = CASE WHEN $3 THEN now() ELSE last_active_at END
WHERE id = $1 AND host_id = $4 AND deleted_at IS NULL
  AND (disk_used_bytes <> $2 OR viewer_connected <> $3 OR $3);

-- name: SetVMViewer :one
-- A viewer connecting or leaving both count as activity.
UPDATE vms
SET viewer_connected = $2, last_active_at = now()
WHERE id = $1 AND host_id = $3 AND deleted_at IS NULL
RETURNING *;

-- name: UserVMTotals :one
-- What a user's VMs add up to, for the per-user limits.
SELECT count(*)::int AS vm_count,
       COALESCE(sum(ram_mb), 0)::int AS ram_mb,
       COALESCE(sum(vcpus), 0)::int AS vcpus,
       COALESCE(sum(disk_gb), 0)::int AS disk_gb
FROM vms
WHERE user_id = $1 AND deleted_at IS NULL;

-- name: LockHost :one
-- Taken before every capacity decision, so two requests cannot both take
-- the last free RAM or disk.
SELECT * FROM hosts WHERE id = $1 FOR UPDATE;

-- name: HostResourceUsage :one
-- RAM and vCPUs held by VMs that are running or about to run.
SELECT COALESCE(sum(ram_mb), 0)::int AS ram_mb,
       COALESCE(sum(vcpus), 0)::int AS vcpus
FROM vms
WHERE host_id = $1 AND deleted_at IS NULL AND state = ANY(sqlc.arg('resource_states')::text[]);

-- name: HostDiskCommittedMB :one
-- Disk promised to every VM on the host, in MB: its disk plus room for its
-- saved memory image.
SELECT COALESCE(sum(disk_gb::bigint * 1024 + ram_mb), 0)::bigint AS committed_mb
FROM vms
WHERE host_id = $1 AND deleted_at IS NULL;

-- name: HostUsageSummary :many
-- Per-host totals for the admin area.
SELECT host_id,
       COALESCE(sum(ram_mb) FILTER (WHERE state = ANY(sqlc.arg('resource_states')::text[])), 0)::int AS used_ram_mb,
       COALESCE(sum(vcpus) FILTER (WHERE state = ANY(sqlc.arg('resource_states')::text[])), 0)::int AS used_vcpus,
       COALESCE(sum(disk_gb::bigint * 1024 + ram_mb), 0)::bigint AS committed_disk_mb,
       count(*)::int AS vm_count
FROM vms
WHERE deleted_at IS NULL
GROUP BY host_id;

-- name: ListRunningVMsForUser :many
SELECT * FROM vms WHERE user_id = $1 AND deleted_at IS NULL AND state = 'running';

-- name: ListIdleVMs :many
-- Running VMs nobody is looking at, idle since before the cutoff, without an
-- active keep-alive.
SELECT * FROM vms
WHERE deleted_at IS NULL
  AND state = 'running'
  AND NOT viewer_connected
  AND COALESCE(last_active_at, state_changed_at) < sqlc.arg('idle_before')::timestamptz
  AND (keep_alive_until IS NULL OR keep_alive_until <= now());

-- name: ListTransitionalVMs :many
SELECT * FROM vms
WHERE deleted_at IS NULL AND state = ANY(sqlc.arg('states')::text[])
ORDER BY state_changed_at;

-- name: InsertVMEvent :exec
INSERT INTO vm_events (vm_id, type, actor, message) VALUES ($1, $2, $3, $4);

-- name: ListVMEvents :many
SELECT * FROM vm_events WHERE vm_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2;

-- name: OpenUsageInterval :exec
-- At most one open interval per VM: opening twice is harmless.
INSERT INTO usage_intervals (vm_id, user_id, host_id, ram_mb, vcpus, started_at)
VALUES ($1, $2, $3, $4, $5, now())
ON CONFLICT (vm_id) WHERE ended_at IS NULL DO NOTHING;

-- name: CloseUsageInterval :exec
UPDATE usage_intervals SET ended_at = now() WHERE vm_id = $1 AND ended_at IS NULL;

-- name: InsertSnapshot :one
INSERT INTO snapshots (vm_id, name) VALUES ($1, $2) RETURNING *;

-- name: GetSnapshot :one
SELECT * FROM snapshots WHERE id = $1 AND vm_id = $2;

-- name: ListSnapshots :many
SELECT * FROM snapshots WHERE vm_id = $1 ORDER BY created_at;

-- name: CountSnapshots :one
SELECT count(*)::int FROM snapshots WHERE vm_id = $1 AND status <> 'error';

-- name: SetSnapshotStatus :exec
UPDATE snapshots SET status = $2, size_bytes = $3 WHERE id = $1;

-- name: DeleteSnapshotRow :exec
DELETE FROM snapshots WHERE id = $1;
