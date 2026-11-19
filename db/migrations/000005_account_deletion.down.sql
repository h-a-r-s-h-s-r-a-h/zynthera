DROP INDEX IF EXISTS users_deletion_requested_idx;
ALTER TABLE users DROP COLUMN IF EXISTS deletion_requested_at;
