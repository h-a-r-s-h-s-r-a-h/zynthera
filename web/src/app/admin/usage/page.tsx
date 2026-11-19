"use client";

import { AdminOnly } from "@/components/admin-only";
import { useState } from "react";
import { Notice, PageLoading } from "@/components/ui";
import { usageCsvUrl, useUsage } from "@/lib/api/admin";
import { formatRAM } from "@/lib/vm-state";

const control = "h-9 rounded-control border border-border bg-bg px-2 text-sm text-text";

/** A date as yyyy-mm-dd in UTC, the format the API and date inputs use. */
function day(date: Date): string {
  return date.toISOString().slice(0, 10);
}

function firstOfMonth(offset: number): string {
  const now = new Date();
  return day(new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() + offset, 1)));
}

function AdminUsagePageContent() {
  // This month so far, by default. "to" is exclusive.
  const [from, setFrom] = useState(() => firstOfMonth(0));
  const [to, setTo] = useState(() => firstOfMonth(1));
  const usage = useUsage(from, to);

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-end gap-3">
        <label className="flex flex-col gap-1 text-sm">
          <span className="text-text-muted">From</span>
          <input
            type="date"
            className={control}
            value={from}
            onChange={(e) => setFrom(e.target.value)}
          />
        </label>
        <label className="flex flex-col gap-1 text-sm">
          <span className="text-text-muted">Up to (not including)</span>
          <input
            type="date"
            className={control}
            value={to}
            onChange={(e) => setTo(e.target.value)}
          />
        </label>
        <a
          href={usageCsvUrl(from, to)}
          className="inline-flex h-9 items-center rounded-control border border-border bg-surface px-3 text-sm font-medium hover:bg-surface-raised"
        >
          Download CSV
        </a>
      </div>
      <p className="text-sm text-text-muted">
        Hours each user&apos;s PCs were running, by PC size. Dates are in UTC. A PC that is still
        running counts up to now.
      </p>

      {usage.isError ? (
        <Notice tone="danger">{usage.error.message}</Notice>
      ) : !usage.data ? (
        <PageLoading />
      ) : usage.data.rows.length === 0 ? (
        <Notice>No PC ran in this period.</Notice>
      ) : (
        <div className="overflow-x-auto rounded-card border border-border">
          <table className="w-full min-w-[32rem] text-left text-sm">
            <thead className="border-b border-border bg-surface text-text-muted">
              <tr>
                <th className="px-3 py-2 font-medium">User</th>
                <th className="px-3 py-2 font-medium">Size</th>
                <th className="px-3 py-2 text-right font-medium">Hours</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {usage.data.rows.map((row) => (
                <tr key={`${row.user_id}-${row.ram_mb}-${row.vcpus}`}>
                  <td className="max-w-72 truncate px-3 py-2" title={row.user_email}>
                    {row.user_email}
                  </td>
                  <td className="px-3 py-2 whitespace-nowrap">
                    {formatRAM(row.ram_mb)} · {row.vcpus} vCPU
                  </td>
                  <td className="px-3 py-2 text-right tabular-nums">{row.hours.toFixed(2)}</td>
                </tr>
              ))}
            </tbody>
            <tfoot className="border-t border-border bg-surface font-medium">
              <tr>
                <td className="px-3 py-2" colSpan={2}>
                  Total
                </td>
                <td className="px-3 py-2 text-right tabular-nums">
                  {usage.data.total_hours.toFixed(2)}
                </td>
              </tr>
            </tfoot>
          </table>
        </div>
      )}
    </div>
  );
}

export default function AdminUsagePage() {
  return (
    <AdminOnly>
      <AdminUsagePageContent />
    </AdminOnly>
  );
}
