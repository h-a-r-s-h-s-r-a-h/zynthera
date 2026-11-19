CREATE TABLE os_images (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  slug             text NOT NULL UNIQUE,     -- e.g. ubuntu-24.04-xfce
  display_name     text NOT NULL,
  description      text NOT NULL DEFAULT '',
  current_version  text NOT NULL,            -- version new VMs are cloned from
  min_ram_mb       int NOT NULL,
  min_disk_gb      int NOT NULL,
  enabled          boolean NOT NULL DEFAULT true,
  sort_order       int NOT NULL DEFAULT 0,
  created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE vms (
  id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id                uuid NOT NULL REFERENCES users(id),
  host_id                uuid NOT NULL REFERENCES hosts(id),
  os_image_id            uuid NOT NULL REFERENCES os_images(id),
  image_version          text NOT NULL,
  name                   text NOT NULL,             -- the user's label, shown in the UI only
  ram_mb                 int NOT NULL CHECK (ram_mb > 0),
  vcpus                  int NOT NULL CHECK (vcpus > 0),
  disk_gb                int NOT NULL CHECK (disk_gb > 0),
  linux_username         text NOT NULL,
  pending_password_hash  text,                      -- cleared once provisioning succeeds
  state                  text NOT NULL CHECK (state IN (
                           'provisioning', 'stopped', 'starting', 'running',
                           'suspending', 'suspended', 'stopping', 'rebooting',
                           'maintenance', 'deleting', 'deleted', 'error')),
  state_changed_at       timestamptz NOT NULL DEFAULT now(),
  state_detail           text,                      -- readable reason for error, or what maintenance is doing
  has_saved_state        boolean NOT NULL DEFAULT false,
  viewer_connected       boolean NOT NULL DEFAULT false,
  last_active_at         timestamptz,
  keep_alive_until       timestamptz,
  disk_used_bytes        bigint NOT NULL DEFAULT 0,
  created_at             timestamptz NOT NULL DEFAULT now(),
  updated_at             timestamptz NOT NULL DEFAULT now(),
  deleted_at             timestamptz
);
CREATE INDEX vms_user_idx ON vms (user_id) WHERE deleted_at IS NULL;
CREATE INDEX vms_host_state_idx ON vms (host_id, state) WHERE deleted_at IS NULL;

CREATE TABLE vm_events (
  id          bigserial PRIMARY KEY,
  vm_id       uuid NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
  type        text NOT NULL,   -- created, started, resumed, suspended, stopped, error, ...
  actor       text NOT NULL CHECK (actor IN ('user', 'admin', 'system')),
  message     text NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX vm_events_vm_idx ON vm_events (vm_id, created_at DESC);

CREATE TABLE snapshots (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),  -- also the snapshot's name on disk
  vm_id       uuid NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
  name        text NOT NULL,                               -- the user's label
  status      text NOT NULL DEFAULT 'creating' CHECK (status IN ('creating', 'ready', 'error')),
  size_bytes  bigint NOT NULL DEFAULT 0,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX snapshots_vm_idx ON snapshots (vm_id);

-- One row per period a VM was running. Billing will read this later.
CREATE TABLE usage_intervals (
  id          bigserial PRIMARY KEY,
  vm_id       uuid NOT NULL REFERENCES vms(id),
  user_id     uuid NOT NULL REFERENCES users(id),
  host_id     uuid NOT NULL REFERENCES hosts(id),
  ram_mb      int NOT NULL,
  vcpus       int NOT NULL,
  started_at  timestamptz NOT NULL,
  ended_at    timestamptz
);
CREATE UNIQUE INDEX usage_one_open_per_vm ON usage_intervals (vm_id) WHERE ended_at IS NULL;
CREATE INDEX usage_user_idx ON usage_intervals (user_id, started_at);
