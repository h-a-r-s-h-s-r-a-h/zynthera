-- name: UpsertOSImage :exec
-- Used by `seed`. An existing row keeps its enabled flag and current version,
-- because an operator may have changed them.
INSERT INTO os_images (slug, display_name, description, current_version, min_ram_mb, min_disk_gb, enabled, sort_order)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (slug) DO UPDATE
SET display_name = EXCLUDED.display_name,
    description = EXCLUDED.description,
    min_ram_mb = EXCLUDED.min_ram_mb,
    min_disk_gb = EXCLUDED.min_disk_gb,
    sort_order = EXCLUDED.sort_order;

-- name: ListEnabledImages :many
SELECT * FROM os_images WHERE enabled ORDER BY sort_order, display_name;

-- name: GetImageBySlug :one
SELECT * FROM os_images WHERE slug = $1;

-- name: GetImage :one
SELECT * FROM os_images WHERE id = $1;
