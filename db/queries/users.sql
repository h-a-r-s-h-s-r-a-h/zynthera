-- name: CreateUser :one
INSERT INTO users (email, password_hash)
VALUES ($1, $2)
ON CONFLICT (email) DO NOTHING
RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: MarkEmailVerified :exec
UPDATE users
SET email_verified_at = COALESCE(email_verified_at, now()), updated_at = now()
WHERE id = $1;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1;

-- name: UpsertAdminUser :one
-- Used by `seed`. Creates the admin, or promotes and verifies an existing
-- account with that email. An existing password is never overwritten.
INSERT INTO users (email, password_hash, role, email_verified_at)
VALUES ($1, $2, 'admin', now())
ON CONFLICT (email) DO UPDATE
SET role = 'admin',
    email_verified_at = COALESCE(users.email_verified_at, now()),
    updated_at = now()
RETURNING *;

-- name: RequestAccountDeletion :exec
-- Disables the account immediately and marks it for deletion.
UPDATE users
SET disabled_at = COALESCE(disabled_at, now()), deletion_requested_at = now(), updated_at = now()
WHERE id = $1;

-- name: ListUsersPendingDeletion :many
SELECT * FROM users WHERE deletion_requested_at IS NOT NULL AND email NOT LIKE 'deleted-%@deleted.invalid';

-- name: ListVMsOfUser :many
SELECT * FROM vms WHERE user_id = $1 AND deleted_at IS NULL;

-- name: AnonymizeUser :exec
-- Removes everything that identifies the person. The row itself stays,
-- because deleted PCs and usage records (kept for billing) refer to it.
UPDATE users
SET email = 'deleted-' || id::text || '@deleted.invalid',
    password_hash = '!',
    email_verified_at = NULL,
    max_vms = NULL, max_ram_mb = NULL, max_vcpus = NULL, max_disk_gb = NULL,
    disabled_at = COALESCE(disabled_at, now()),
    updated_at = now()
WHERE id = $1;

-- name: DeleteUserEmailTokens :exec
DELETE FROM email_tokens WHERE user_id = $1;

-- name: ScrubUserVMNames :exec
-- PC names and Linux usernames were chosen by the person and may identify them.
UPDATE vms SET name = 'deleted', linux_username = 'deleted' WHERE user_id = $1;

-- name: CountAdmins :one
SELECT count(*)::int FROM users WHERE role = 'admin' AND disabled_at IS NULL;
