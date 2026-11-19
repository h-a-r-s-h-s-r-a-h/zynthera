-- name: AdminListVMs :many
-- Every PC, newest first, with its owner and host. Keyset pagination on
-- (created_at, id): the cursor is the last row of the previous page.
SELECT sqlc.embed(vms),
       users.email AS user_email,
       hosts.name AS host_name,
       os_images.slug AS image_slug,
       os_images.display_name AS image_display_name
FROM vms
JOIN users ON users.id = vms.user_id
JOIN hosts ON hosts.id = vms.host_id
JOIN os_images ON os_images.id = vms.os_image_id
WHERE vms.deleted_at IS NULL
  AND (sqlc.narg('state')::text IS NULL OR vms.state = sqlc.narg('state')::text)
  AND (sqlc.narg('host_id')::uuid IS NULL OR vms.host_id = sqlc.narg('host_id')::uuid)
  AND (sqlc.narg('email')::text IS NULL OR users.email::text ILIKE '%' || sqlc.narg('email')::text || '%')
  AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
       OR (vms.created_at, vms.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
ORDER BY vms.created_at DESC, vms.id DESC
LIMIT sqlc.arg('page_size');

-- name: AdminListUsers :many
-- Every account, newest first, with what its PCs add up to.
SELECT sqlc.embed(users),
       (SELECT count(*) FROM vms WHERE vms.user_id = users.id AND vms.deleted_at IS NULL)::int AS vm_count,
       (SELECT COALESCE(sum(ram_mb), 0) FROM vms WHERE vms.user_id = users.id AND vms.deleted_at IS NULL)::int AS used_ram_mb,
       (SELECT COALESCE(sum(disk_gb), 0) FROM vms WHERE vms.user_id = users.id AND vms.deleted_at IS NULL)::int AS used_disk_gb
FROM users
WHERE (sqlc.narg('email')::text IS NULL OR users.email::text ILIKE '%' || sqlc.narg('email')::text || '%')
  AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
       OR (users.created_at, users.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
ORDER BY users.created_at DESC, users.id DESC
LIMIT sqlc.arg('page_size');

-- name: AdminSetUserLimits :one
-- Sets all four limit overrides and the disabled flag. NULL means "use the
-- default from the configuration".
UPDATE users
SET max_vms = sqlc.narg('max_vms'),
    max_ram_mb = sqlc.narg('max_ram_mb'),
    max_vcpus = sqlc.narg('max_vcpus'),
    max_disk_gb = sqlc.narg('max_disk_gb'),
    disabled_at = sqlc.narg('disabled_at'),
    updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: AdminUsage :many
-- Hours each user's PCs ran, per size, inside a time range. An interval that
-- crosses the edge of the range only counts for the part inside it, and an
-- interval that is still open counts up to now.
SELECT users.id AS user_id,
       users.email AS user_email,
       usage_intervals.ram_mb,
       usage_intervals.vcpus,
       (sum(extract(epoch FROM (
          LEAST(COALESCE(usage_intervals.ended_at, now()), sqlc.arg('until')::timestamptz)
          - GREATEST(usage_intervals.started_at, sqlc.arg('since')::timestamptz)
       ))) / 3600.0)::float8 AS hours
FROM usage_intervals
JOIN users ON users.id = usage_intervals.user_id
WHERE usage_intervals.started_at < sqlc.arg('until')::timestamptz
  AND COALESCE(usage_intervals.ended_at, now()) > sqlc.arg('since')::timestamptz
GROUP BY users.id, users.email, usage_intervals.ram_mb, usage_intervals.vcpus
ORDER BY users.email, usage_intervals.ram_mb, usage_intervals.vcpus;
