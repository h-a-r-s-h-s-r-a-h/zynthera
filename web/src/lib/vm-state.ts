import type { VM, VMState } from "./api/types";

// Everything the UI derives from a VM object lives here as pure functions, so
// it can be tested without a browser. Which actions are allowed is NOT
// decided here: the API sends `allowed_actions` and the UI obeys it.

export type Tone = "neutral" | "success" | "warning" | "danger" | "busy";

const transitional: ReadonlySet<VMState> = new Set([
  "provisioning",
  "starting",
  "suspending",
  "stopping",
  "rebooting",
  "maintenance",
  "deleting",
]);

/** True while a job is working on the PC and every button must be disabled. */
export function isBusy(vm: Pick<VM, "state">): boolean {
  return transitional.has(vm.state);
}

/** The short text of the state badge. */
export function stateLabel(vm: Pick<VM, "state" | "has_saved_state" | "state_detail">): string {
  switch (vm.state) {
    case "provisioning":
      return "Setting up…";
    case "starting":
      return vm.has_saved_state ? "Resuming…" : "Starting…";
    case "running":
      return "Running";
    case "suspending":
      return "Suspending…";
    case "suspended":
      return "Suspended";
    case "stopping":
      return "Shutting down…";
    case "stopped":
      return "Shut down";
    case "rebooting":
      return "Restarting…";
    case "maintenance":
      return vm.state_detail ? `${vm.state_detail}…` : "Working…";
    case "deleting":
      return "Deleting…";
    case "deleted":
      return "Deleted";
    case "error":
      return "Needs attention";
  }
}

export function stateTone(state: VMState): Tone {
  if (transitional.has(state)) return "busy";
  switch (state) {
    case "running":
      return "success";
    case "suspended":
      return "warning";
    case "error":
      return "danger";
    default:
      return "neutral";
  }
}

/** What the desktop viewer should be doing for a PC in this state. */
export type ViewerPhase =
  | { kind: "connect" }
  | { kind: "start"; resuming: boolean }
  | { kind: "wait"; label: string }
  | { kind: "error"; detail: string }
  | { kind: "gone" };

export function viewerPhase(vm: VM): ViewerPhase {
  switch (vm.state) {
    case "running":
      return { kind: "connect" };
    case "stopped":
    case "suspended":
      return { kind: "start", resuming: vm.has_saved_state };
    case "error":
      return {
        kind: "error",
        detail: vm.state_detail ?? "Something went wrong with this PC.",
      };
    case "deleting":
    case "deleted":
      return { kind: "gone" };
    case "provisioning":
      return { kind: "wait", label: "Setting up your PC…" };
    case "starting":
      return {
        kind: "wait",
        label: vm.has_saved_state ? "Resuming your PC…" : "Starting your PC…",
      };
    case "suspending":
      return { kind: "wait", label: "Suspending your PC…" };
    case "stopping":
      return { kind: "wait", label: "Shutting down your PC…" };
    case "rebooting":
      return { kind: "wait", label: "Restarting your PC…" };
    case "maintenance":
      return { kind: "wait", label: `${vm.state_detail ?? "Working on your PC"}…` };
  }
}

/** Delays between reconnect attempts after the stream drops (spec 15.4). */
export const reconnectDelaysMs = [1000, 2000, 5000, 10000, 10000] as const;

/** The delay before attempt number `attempt` (0-based), or null to give up. */
export function reconnectDelay(attempt: number): number | null {
  return attempt >= 0 && attempt < reconnectDelaysMs.length ? reconnectDelaysMs[attempt] : null;
}

export function formatRAM(mb: number): string {
  return mb % 1024 === 0 ? `${mb / 1024} GB` : `${mb} MB`;
}

export function formatBytes(bytes: number): string {
  if (bytes < 1024 ** 3) return `${Math.round(bytes / 1024 ** 2)} MB`;
  const gb = bytes / 1024 ** 3;
  return `${gb >= 10 ? Math.round(gb) : Math.round(gb * 10) / 10} GB`;
}

/** "9 GB of 80 GB" */
export function diskSummary(vm: Pick<VM, "disk_used_bytes" | "disk_gb">): string {
  if (vm.disk_used_bytes <= 0) return `${vm.disk_gb} GB disk`;
  return `${formatBytes(vm.disk_used_bytes)} of ${vm.disk_gb} GB`;
}

export function keepAliveActive(vm: Pick<VM, "keep_alive_until">, now: Date = new Date()): boolean {
  return vm.keep_alive_until !== null && new Date(vm.keep_alive_until).getTime() > now.getTime();
}

/** Puts a changed PC into a list: replaces, adds, or removes it when deleted. */
export function applyVMUpdate(list: readonly VM[], vm: VM): VM[] {
  const without = list.filter((v) => v.id !== vm.id);
  if (vm.state === "deleted") return without;
  const index = list.findIndex((v) => v.id === vm.id);
  if (index === -1) return [...list, vm];
  const next = [...list];
  next[index] = vm;
  return next;
}
