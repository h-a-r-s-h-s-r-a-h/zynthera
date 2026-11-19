CREATE TABLE hosts (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name               text NOT NULL UNIQUE,
  agent_url          text NOT NULL,   -- where the API calls the agent
  public_url         text NOT NULL,   -- where browsers reach the agent (https/wss base URL)
  agent_token_enc    bytea NOT NULL,  -- AES-GCM with ZYN_SECRETS_KEY
  stream_secret_enc  bytea NOT NULL,  -- AES-GCM with ZYN_SECRETS_KEY
  admin_state        text NOT NULL DEFAULT 'active'
                       CHECK (admin_state IN ('active', 'draining', 'disabled')),
  total_ram_mb       int NOT NULL DEFAULT 0,      -- reported by the agent
  total_vcpus        int NOT NULL DEFAULT 0,
  total_disk_gb      int NOT NULL DEFAULT 0,
  free_disk_gb       int NOT NULL DEFAULT 0,
  reserved_ram_mb    int NOT NULL DEFAULT 4096,   -- kept for the host OS
  cpu_overcommit     numeric(4,2) NOT NULL DEFAULT 4.0,
  images             jsonb NOT NULL DEFAULT '[]', -- [{slug, version}] present on the host
  agent_version      text,
  last_heartbeat_at  timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now()
);
