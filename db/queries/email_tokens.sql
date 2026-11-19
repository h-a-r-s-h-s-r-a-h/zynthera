-- name: CreateEmailToken :exec
INSERT INTO email_tokens (user_id, purpose, token_hash, expires_at)
VALUES ($1, $2, $3, $4);

-- name: ConsumeEmailToken :one
-- Marks a valid, unused token as used and returns its owner. Because the
-- check and the update are one statement, a token can only ever be used once.
UPDATE email_tokens
SET used_at = now()
WHERE token_hash = $1
  AND purpose = $2
  AND used_at IS NULL
  AND expires_at > now()
RETURNING user_id;

-- name: InvalidateEmailTokens :exec
-- Retires older unused tokens when a new one is issued.
UPDATE email_tokens
SET used_at = now()
WHERE user_id = $1 AND purpose = $2 AND used_at IS NULL;

-- name: DeleteExpiredEmailTokens :execrows
DELETE FROM email_tokens WHERE expires_at <= now();
