"use client";

import Link from "next/link";
import type { VM, VMAction } from "@/lib/api/types";
import { useVMAction, type SimpleAction } from "@/lib/api/vms";
import { diskSummary, formatRAM, isBusy } from "@/lib/vm-state";
import { Button, ButtonLink, Card, Notice } from "../ui";
import { StateBadge } from "./state-badge";

/** The buttons a card can show, in order. Which ones appear is decided by `allowed_actions`. */
const cardActions: { allowed: VMAction; action: SimpleAction; label: string; confirm?: string }[] =
  [
    { allowed: "suspend", action: "suspend", label: "Suspend" },
    { allowed: "shutdown", action: "shutdown", label: "Shut down" },
    { allowed: "reboot", action: "reboot", label: "Restart" },
    {
      allowed: "force_stop",
      action: "force-stop",
      label: "Force stop",
      confirm:
        "Force stop is like pulling the power plug: unsaved work in the PC is lost. Force stop it?",
    },
  ];

function openLabel(vm: VM): string | null {
  if (vm.allowed_actions.includes("connect")) return "Open";
  if (vm.allowed_actions.includes("start")) return vm.has_saved_state ? "Resume" : "Start";
  return null;
}

export function VMCard({ vm }: { vm: VM }) {
  const act = useVMAction();
  const busy = isBusy(vm);
  const open = openLabel(vm);

  return (
    <Card className="flex flex-col gap-4" data-testid="vm-card">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="truncate text-lg font-semibold" title={vm.name}>
            {vm.name}
          </h2>
          <p className="text-sm text-text-muted">{vm.image.display_name}</p>
        </div>
        <StateBadge vm={vm} />
      </div>

      <dl className="grid grid-cols-3 gap-3 text-sm">
        <div>
          <dt className="text-text-muted">Memory</dt>
          <dd className="font-medium">{formatRAM(vm.ram_mb)}</dd>
        </div>
        <div>
          <dt className="text-text-muted">CPU</dt>
          <dd className="font-medium">
            {vm.vcpus} {vm.vcpus === 1 ? "core" : "cores"}
          </dd>
        </div>
        <div>
          <dt className="text-text-muted">Disk</dt>
          <dd className="font-medium">{diskSummary(vm)}</dd>
        </div>
      </dl>

      {vm.state === "error" && (
        <Notice tone="danger">{vm.state_detail ?? "Something went wrong with this PC."}</Notice>
      )}
      {vm.state !== "error" && vm.state_detail && !busy && <Notice>{vm.state_detail}</Notice>}
      {act.isError && <Notice tone="danger">{act.error.message}</Notice>}

      <div className="flex flex-wrap items-center gap-2">
        {open && <ButtonLink href={`/vms/${vm.id}/desktop`}>{open}</ButtonLink>}
        {cardActions
          .filter((a) => vm.allowed_actions.includes(a.allowed))
          .map((a) => (
            <Button
              key={a.action}
              variant="secondary"
              disabled={act.isPending}
              onClick={() => {
                if (a.confirm && !window.confirm(a.confirm)) return;
                act.mutate({ id: vm.id, action: a.action });
              }}
            >
              {a.label}
            </Button>
          ))}
        <Link
          href={`/vms/${vm.id}`}
          className="ml-auto text-sm font-medium text-text-muted hover:text-text"
        >
          Settings
        </Link>
      </div>
    </Card>
  );
}
