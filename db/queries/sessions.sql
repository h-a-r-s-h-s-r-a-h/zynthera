-- name: CreateSession :one
INSERT INTO sessions (user_id, token_hash, expires_at, ip, user_agent)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetSessionUser :one
-- The user behind a live session. Disabled users have no live sessions.
SELECT sqlc.embed(users), sessions.id AS session_id, sessions.last_seen_at AS session_last_seen_at
FROM sessions
JOIN users ON users.id = sessions.user_id
WHERE sessions.token_hash = $1
  AND sessions.expires_at > now()
  AND users.disabled_at IS NULL;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = now() WHERE id = $1;

-- name: DeleteSessionByTokenHash :exec
DELETE FROM sessions WHERE token_hash = $1;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = $1;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= now();
