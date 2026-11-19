-- name: CreateHost :one
INSERT INTO hosts (name, agent_url, public_url, agent_token_enc, stream_secret_enc)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: UpsertHostWithID :one
-- Used by `seed` for the development host, whose id and secrets come from .env.
INSERT INTO hosts (id, name, agent_url, public_url, agent_token_enc, stream_secret_enc)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (id) DO UPDATE
SET name = EXCLUDED.name,
    agent_url = EXCLUDED.agent_url,
    public_url = EXCLUDED.public_url,
    agent_token_enc = EXCLUDED.agent_token_enc,
    stream_secret_enc = EXCLUDED.stream_secret_enc
RETURNING *;

-- name: GetHost :one
SELECT * FROM hosts WHERE id = $1;

-- name: ListHosts :many
SELECT * FROM hosts ORDER BY name;

-- name: RecordHeartbeat :exec
UPDATE hosts
SET total_ram_mb = $2,
    total_vcpus = $3,
    total_disk_gb = $4,
    free_disk_gb = $5,
    images = $6,
    agent_version = $7,
    last_heartbeat_at = now()
WHERE id = $1;

-- name: UpdateHostAdmin :one
UPDATE hosts
SET admin_state = COALESCE(sqlc.narg('admin_state'), admin_state),
    reserved_ram_mb = COALESCE(sqlc.narg('reserved_ram_mb'), reserved_ram_mb),
    cpu_overcommit = COALESCE(sqlc.narg('cpu_overcommit')::float8, cpu_overcommit)
WHERE id = sqlc.arg('id')
RETURNING *;
