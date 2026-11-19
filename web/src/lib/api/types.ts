// API types. Keep these in step with the Go response structs
// (internal/api/httpapi). JSON keys are snake_case; timestamps are RFC 3339 UTC.

export type Role = "user" | "admin";

/** internal/api/httpapi/auth_handlers.go: userJSON */
export interface User {
  id: string;
  email: string;
  role: Role;
  email_verified: boolean;
  created_at: string;
}

/** The single error shape of every endpoint (internal/shared/httpx). */
export interface ApiErrorBody {
  error: {
    code: string;
    message: string;
    details: {
      fields?: Record<string, string>;
      retry_after_seconds?: number;
    };
  };
}

export type VMState =
  | "provisioning"
  | "stopped"
  | "starting"
  | "running"
  | "suspending"
  | "suspended"
  | "stopping"
  | "rebooting"
  | "maintenance"
  | "deleting"
  | "deleted"
  | "error";

/** internal/api/vms/statemachine.go: Action */
export type VMAction =
  | "connect"
  | "start"
  | "suspend"
  | "shutdown"
  | "force_stop"
  | "reboot"
  | "discard_saved_state"
  | "resize"
  | "snapshot"
  | "restore_snapshot"
  | "delete_snapshot"
  | "delete";

/** internal/api/vmsvc/vmsvc.go: VMJSON */
export interface VM {
  id: string;
  name: string;
  image: { slug: string; display_name: string };
  ram_mb: number;
  vcpus: number;
  disk_gb: number;
  disk_used_bytes: number;
  linux_username: string;
  state: VMState;
  state_detail: string | null;
  has_saved_state: boolean;
  viewer_connected: boolean;
  keep_alive_until: string | null;
  last_active_at: string | null;
  created_at: string;
  allowed_actions: VMAction[];
}

/** internal/api/httpapi/vm_handlers.go: imageJSON */
export interface OSImage {
  slug: string;
  display_name: string;
  description: string;
  min_ram_mb: number;
  min_disk_gb: number;
}

/** internal/api/httpapi/vm_handlers.go: handleOptions */
export interface Options {
  allowed_ram_mb: number[];
  allowed_vcpus: number[];
  allowed_disk_gb: number[];
  max_keep_alive_hours: number;
  max_snapshots_per_vm: number;
  idle_input_minutes: number;
  limits: { max_vms: number; max_ram_mb: number; max_vcpus: number; max_disk_gb: number };
  usage: { vms: number; ram_mb: number; vcpus: number; disk_gb: number };
}

/** internal/api/httpapi/vm_handlers.go: eventJSON */
export interface VMEvent {
  type: string;
  actor: "user" | "admin" | "system";
  message: string;
  created_at: string;
}

/** internal/api/httpapi/vm_handlers.go: snapshotJSON */
export interface Snapshot {
  id: string;
  name: string;
  status: "creating" | "ready" | "error";
  size_bytes: number;
  created_at: string;
}

/** internal/api/httpapi/hosts.go: handleStreamToken */
export interface StreamToken {
  url: string;
  token: string;
  expires_at: string;
}

/** POST /api/vms */
export interface CreateVMInput {
  name: string;
  image: string;
  ram_mb: number;
  vcpus: number;
  disk_gb: number;
  linux_username: string;
  linux_password: string;
}

/** internal/api/httpapi/hosts.go: handleFilesToken */
export interface FilesToken {
  base_url: string;
  token: string;
  expires_at: string;
  max_upload_mb: number;
}

/** internal/bridge/bridge.go: Entry */
export interface FileEntry {
  name: string;
  type: "file" | "dir";
  size: number;
  modified: string;
}

/** internal/api/httpapi/hosts.go: hostJSON */
export interface Host {
  id: string;
  name: string;
  agent_url: string;
  public_url: string;
  admin_state: "active" | "draining" | "disabled";
  online: boolean;
  last_heartbeat_at: string | null;
  agent_version: string | null;
  total_ram_mb: number;
  total_vcpus: number;
  total_disk_gb: number;
  free_disk_gb: number;
  reserved_ram_mb: number;
  cpu_overcommit: number;
  images: { slug: string; version: string }[];
  created_at: string;
  used_ram_mb: number;
  used_vcpus: number;
  committed_disk_gb: number;
  vm_count: number;
}

/** internal/api/httpapi/admin_handlers.go: adminVMJSON */
export interface AdminVM extends VM {
  user_id: string;
  user_email: string;
  host_id: string;
  host_name: string;
}

/** internal/api/httpapi/admin_handlers.go: adminUserJSON */
export interface AdminUser extends User {
  disabled: boolean;
  max_vms: number | null;
  max_ram_mb: number | null;
  max_vcpus: number | null;
  max_disk_gb: number | null;
  vm_count: number;
  used_ram_mb: number;
  used_disk_gb: number;
}

export interface UserLimitDefaults {
  max_vms: number;
  max_ram_mb: number;
  max_vcpus: number;
  max_disk_gb: number;
}

/** internal/api/httpapi/admin_handlers.go: usageRowJSON */
export interface UsageRow {
  user_id: string;
  user_email: string;
  ram_mb: number;
  vcpus: number;
  hours: number;
}
