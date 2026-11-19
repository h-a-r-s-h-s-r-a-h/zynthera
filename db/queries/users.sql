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
