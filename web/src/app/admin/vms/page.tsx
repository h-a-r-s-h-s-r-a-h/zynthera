"use client";

import { AdminOnly } from "@/components/admin-only";
import { useState } from "react";
import { Button, Notice, PageLoading } from "@/components/ui";
import { StateBadge } from "@/components/vm/state-badge";
import { useAdminVMAction, useAdminVMs, useHosts, type VMFilters } from "@/lib/api/admin";
import { formatRAM } from "@/lib/vm-state";

const states = [
  "running",
  "suspended",
  "stopped",
  "error",
  "provisioning",
  "starting",
  "suspending",
  "stopping",
  "rebooting",
  "maintenance",
  "deleting",
];

const control = "h-9 rounded-control border border-border bg-bg px-2 text-sm text-text";

function AdminVMsPageContent() {
  const [filters, setFilters] = useState<VMFilters>({ state: "", host: "", email: "" });
  const [emailDraft, setEmailDraft] = useState("");
  const hosts = useHosts();
  const vms = useAdminVMs(filters);
  const act = useAdminVMAction();

  const rows = vms.data?.pages.flatMap((page) => page.vms) ?? [];

  return (
    <div className="flex flex-col gap-4">
      <form
        className="flex flex-wrap items-end gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          setFilters((f) => ({ ...f, email: emailDraft.trim() }));
        }}
      >
        <label className="flex flex-col gap-1 text-sm">
          <span className="text-text-muted">State</span>
          <select
            className={control}
            value={filters.state}
            onChange={(e) => setFilters((f) => ({ ...f, state: e.target.value }))}
          >
            <option value="">Any</option>
            {states.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </label>
        <label className="flex flex-col gap-1 text-sm">
          <span className="text-text-muted">Host</span>
          <select
            className={control}
            value={filters.host}
            onChange={(e) => setFilters((f) => ({ ...f, host: e.target.value }))}
          >
            <option value="">Any</option>
            {hosts.data?.hosts.map((h) => (
              <option key={h.id} value={h.id}>
                {h.name}
              </option>
            ))}
          </select>
        </label>
        <label className="flex flex-col gap-1 text-sm">
          <span className="text-text-muted">Owner email contains</span>
          <input
            className={control}
            value={emailDraft}
            onChange={(e) => setEmailDraft(e.target.value)}
            placeholder="alex@"
          />
        </label>
        <Button type="submit" variant="secondary">
          Filter
        </Button>
      </form>

      {act.isError && <Notice tone="danger">{act.error.message}</Notice>}
      {vms.isError ? (
        <Notice tone="danger">{vms.error.message}</Notice>
      ) : !vms.data ? (
        <PageLoading />
      ) : rows.length === 0 ? (
        <Notice>No PCs match.</Notice>
      ) : (
        <div className="overflow-x-auto rounded-card border border-border">
          <table className="w-full min-w-[46rem] text-left text-sm">
            <thead className="border-b border-border bg-surface text-text-muted">
              <tr>
                <th className="px-3 py-2 font-medium">PC</th>
                <th className="px-3 py-2 font-medium">Owner</th>
                <th className="px-3 py-2 font-medium">Host</th>
                <th className="px-3 py-2 font-medium">Size</th>
                <th className="px-3 py-2 font-medium">State</th>
                <th className="px-3 py-2 font-medium">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {rows.map((vm) => (
                <tr key={vm.id} data-testid="admin-vm-row">
                  <td className="max-w-48 px-3 py-2">
                    <div className="truncate font-medium" title={vm.name}>
                      {vm.name}
                    </div>
                    <div className="text-xs text-text-muted">{vm.image.display_name}</div>
                  </td>
                  <td className="max-w-56 truncate px-3 py-2" title={vm.user_email}>
                    {vm.user_email}
                  </td>
                  <td className="px-3 py-2">{vm.host_name}</td>
                  <td className="px-3 py-2 whitespace-nowrap">
                    {formatRAM(vm.ram_mb)} · {vm.vcpus} vCPU · {vm.disk_gb} GB
                  </td>
                  <td className="px-3 py-2">
                    <StateBadge vm={vm} />
                  </td>
                  <td className="px-3 py-2">
                    <div className="flex justify-end gap-2">
                      {vm.allowed_actions.includes("suspend") && (
                        <Button
                          variant="secondary"
                          disabled={act.isPending}
                          onClick={() => act.mutate({ id: vm.id, action: "suspend" })}
                        >
                          Suspend
                        </Button>
                      )}
                      {vm.allowed_actions.includes("force_stop") && (
                        <Button
                          variant="ghost"
                          disabled={act.isPending}
                          onClick={() => {
                            const ok = window.confirm(
                              `Force stop "${vm.name}" of ${vm.user_email}? Unsaved work in the PC is lost.`,
                            );
                            if (ok) act.mutate({ id: vm.id, action: "force-stop" });
                          }}
                        >
                          Force stop
                        </Button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {vms.hasNextPage && (
        <div>
          <Button
            variant="secondary"
            busy={vms.isFetchingNextPage}
            onClick={() => void vms.fetchNextPage()}
          >
            Load more
          </Button>
        </div>
      )}
    </div>
  );
}

export default function AdminVMsPage() {
  return (
    <AdminOnly>
      <AdminVMsPageContent />
    </AdminOnly>
  );
}
