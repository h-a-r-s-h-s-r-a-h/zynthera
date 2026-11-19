import { describe, expect, it } from "vitest";
import type { VM, VMState } from "./api/types";
import {
  applyVMUpdate,
  diskSummary,
  formatBytes,
  formatRAM,
  isBusy,
  keepAliveActive,
  reconnectDelay,
  stateLabel,
  stateTone,
  viewerPhase,
} from "./vm-state";

function vm(overrides: Partial<VM> = {}): VM {
  return {
    id: "a",
    name: "My PC",
    image: { slug: "ubuntu-24.04-xfce", display_name: "Ubuntu 24.04" },
    ram_mb: 4096,
    vcpus: 2,
    disk_gb: 80,
    disk_used_bytes: 0,
    linux_username: "alex",
    state: "running",
    state_detail: null,
    has_saved_state: false,
    viewer_connected: false,
    keep_alive_until: null,
    last_active_at: null,
    created_at: "2026-11-19T10:00:00Z",
    allowed_actions: [],
    ...overrides,
  };
}

const allStates: VMState[] = [
  "provisioning",
  "stopped",
  "starting",
  "running",
  "suspending",
  "suspended",
  "stopping",
  "rebooting",
  "maintenance",
  "deleting",
  "deleted",
  "error",
];

describe("state labels", () => {
  it("has a label for every state", () => {
    for (const state of allStates) {
      expect(stateLabel(vm({ state }))).not.toBe("");
    }
  });

  it("says Resuming when a saved session exists", () => {
    expect(stateLabel(vm({ state: "starting", has_saved_state: true }))).toBe("Resuming…");
    expect(stateLabel(vm({ state: "starting", has_saved_state: false }))).toBe("Starting…");
  });

  it("names what maintenance is doing", () => {
    expect(stateLabel(vm({ state: "maintenance", state_detail: "Taking a snapshot" }))).toBe(
      "Taking a snapshot…",
    );
  });

  it("marks exactly the transitional states as busy", () => {
    const busy = allStates.filter((state) => isBusy({ state }));
    expect(busy).toEqual([
      "provisioning",
      "starting",
      "suspending",
      "stopping",
      "rebooting",
      "maintenance",
      "deleting",
    ]);
    for (const state of busy) expect(stateTone(state)).toBe("busy");
    expect(stateTone("running")).toBe("success");
    expect(stateTone("error")).toBe("danger");
  });
});

describe("viewerPhase (spec 15.4)", () => {
  it("connects when running", () => {
    expect(viewerPhase(vm({ state: "running" }))).toEqual({ kind: "connect" });
  });

  it("starts a stopped or suspended PC", () => {
    expect(viewerPhase(vm({ state: "stopped" }))).toEqual({ kind: "start", resuming: false });
    expect(viewerPhase(vm({ state: "suspended", has_saved_state: true }))).toEqual({
      kind: "start",
      resuming: true,
    });
  });

  it("waits and names what is happening during a transition", () => {
    expect(viewerPhase(vm({ state: "starting", has_saved_state: true }))).toEqual({
      kind: "wait",
      label: "Resuming your PC…",
    });
    expect(viewerPhase(vm({ state: "provisioning" }))).toEqual({
      kind: "wait",
      label: "Setting up your PC…",
    });
  });

  it("shows the message for an error", () => {
    expect(viewerPhase(vm({ state: "error", state_detail: "PC not found on its host." }))).toEqual({
      kind: "error",
      detail: "PC not found on its host.",
    });
  });

  it("treats a deleted PC as gone", () => {
    expect(viewerPhase(vm({ state: "deleted" })).kind).toBe("gone");
    expect(viewerPhase(vm({ state: "deleting" })).kind).toBe("gone");
  });
});

describe("reconnectDelay", () => {
  it("waits 1 s, 2 s, 5 s, then 10 s, for 5 tries", () => {
    expect([0, 1, 2, 3, 4].map(reconnectDelay)).toEqual([1000, 2000, 5000, 10000, 10000]);
  });

  it("gives up after the fifth try", () => {
    expect(reconnectDelay(5)).toBeNull();
    expect(reconnectDelay(-1)).toBeNull();
  });
});

describe("formatting", () => {
  it("formats memory", () => {
    expect(formatRAM(2048)).toBe("2 GB");
    expect(formatRAM(16384)).toBe("16 GB");
    expect(formatRAM(1536)).toBe("1536 MB");
  });

  it("formats bytes", () => {
    expect(formatBytes(9 * 1024 ** 3)).toBe("9 GB");
    expect(formatBytes(9.44 * 1024 ** 3)).toBe("9.4 GB");
    expect(formatBytes(45.6 * 1024 ** 3)).toBe("46 GB");
    expect(formatBytes(512 * 1024 ** 2)).toBe("512 MB");
    expect(formatBytes(2048)).toBe("2 KB");
    expect(formatBytes(300)).toBe("300 B");
  });

  it("summarises disk use", () => {
    expect(diskSummary({ disk_used_bytes: 9 * 1024 ** 3, disk_gb: 80 })).toBe("9 GB of 80 GB");
    expect(diskSummary({ disk_used_bytes: 0, disk_gb: 80 })).toBe("80 GB disk");
  });
});

describe("keepAliveActive", () => {
  const now = new Date("2026-11-19T12:00:00Z");
  it("is active only while the end time is in the future", () => {
    expect(keepAliveActive({ keep_alive_until: null }, now)).toBe(false);
    expect(keepAliveActive({ keep_alive_until: "2026-11-19T13:00:00Z" }, now)).toBe(true);
    expect(keepAliveActive({ keep_alive_until: "2026-11-19T11:59:59Z" }, now)).toBe(false);
  });
});

describe("applyVMUpdate", () => {
  const a = vm({ id: "a" });
  const b = vm({ id: "b" });

  it("replaces a PC in place", () => {
    const next = applyVMUpdate([a, b], vm({ id: "a", state: "suspended" }));
    expect(next.map((v) => `${v.id}:${v.state}`)).toEqual(["a:suspended", "b:running"]);
  });

  it("adds a new PC at the end", () => {
    expect(applyVMUpdate([a], b).map((v) => v.id)).toEqual(["a", "b"]);
  });

  it("removes a deleted PC", () => {
    expect(applyVMUpdate([a, b], vm({ id: "a", state: "deleted" })).map((v) => v.id)).toEqual([
      "b",
    ]);
    expect(applyVMUpdate([b], vm({ id: "a", state: "deleted" })).map((v) => v.id)).toEqual(["b"]);
  });

  it("does not change the list it was given", () => {
    const list = [a];
    applyVMUpdate(list, vm({ id: "a", state: "stopped" }));
    expect(list[0].state).toBe("running");
  });
});
