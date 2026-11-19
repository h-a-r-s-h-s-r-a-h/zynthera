-- When a user asks for their account to be deleted, the account is disabled
-- at once and this is set. A periodic task deletes their PCs and, once none
-- is left, removes everything that identifies the person.
ALTER TABLE users ADD COLUMN deletion_requested_at timestamptz;
CREATE INDEX users_deletion_requested_idx ON users (deletion_requested_at) WHERE deletion_requested_at IS NOT NULL;
