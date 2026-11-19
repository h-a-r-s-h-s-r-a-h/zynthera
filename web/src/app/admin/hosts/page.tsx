"use client";

import { AdminOnly } from "@/components/admin-only";
import { useState, type FormEvent } from "react";
import { Button, Card, Field, Notice, PageLoading } from "@/components/ui";
import { useCreateHost, useHosts, usePatchHost, type CreatedHost } from "@/lib/api/admin";
import { ApiError } from "@/lib/api/client";
import type { Host } from "@/lib/api/types";
import { formatRAM } from "@/lib/vm-state";

function Bar({
  label,
  used,
  total,
  unit,
}: {
  label: string;
  used: number;
  total: number;
  unit: string;
}) {
  const fraction = total > 0 ? Math.min(used / total, 1) : 0;
  const tone = fraction >= 0.9 ? "bg-danger" : fraction >= 0.75 ? "bg-warning" : "bg-accent";
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-baseline justify-between text-sm">
        <span className="text-text-muted">{label}</span>
        <span className="font-medium">
          {used} of {total} {unit}
        </span>
      </div>
      <div
        role="meter"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={total}
        aria-valuenow={Math.min(used, total)}
        className="h-2 overflow-hidden rounded-full bg-border"
      >
        <div className={`h-full ${tone}`} style={{ width: `${fraction * 100}%` }} />
      </div>
    </div>
  );
}

const dateTime = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "medium" });

function HostCard({ host, headroom }: { host: Host; headroom: number }) {
  const patch = usePatchHost();
  // The same arithmetic the scheduler uses (spec section 10.6).
  const ramForPCs = Math.max(host.total_ram_mb - host.reserved_ram_mb, 0);
  const vcpusForPCs = Math.floor(host.total_vcpus * host.cpu_overcommit);
  const diskForPCs = Math.floor((host.total_disk_gb * (100 - headroom)) / 100);

  return (
    <Card className="flex flex-col gap-4" data-testid="host-card">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="truncate text-lg font-semibold">{host.name}</h2>
          <p className="truncate text-sm text-text-muted">{host.public_url}</p>
        </div>
        <span
          className={`inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs font-medium ${
            host.online ? "border-success/40 text-success" : "border-danger/40 text-danger"
          }`}
        >
          <span aria-hidden className="size-1.5 rounded-full bg-current" />
          {host.online ? "Online" : "Offline"}
        </span>
      </div>

      <div className="grid gap-3 sm:grid-cols-3">
        <Bar
          label="Memory"
          used={Math.round(host.used_ram_mb / 1024)}
          total={Math.round(ramForPCs / 1024)}
          unit="GB"
        />
        <Bar label="vCPUs" used={host.used_vcpus} total={vcpusForPCs} unit="" />
        <Bar label="Disk promised" used={host.committed_disk_gb} total={diskForPCs} unit="GB" />
      </div>

      <dl className="grid grid-cols-2 gap-x-6 gap-y-1 text-sm sm:grid-cols-4">
        <dt className="text-text-muted">PCs</dt>
        <dd>{host.vm_count}</dd>
        <dt className="text-text-muted">Reserved for the server</dt>
        <dd>{formatRAM(host.reserved_ram_mb)}</dd>
        <dt className="text-text-muted">Last heartbeat</dt>
        <dd>
          {host.last_heartbeat_at ? dateTime.format(new Date(host.last_heartbeat_at)) : "never"}
        </dd>
        <dt className="text-text-muted">Agent</dt>
        <dd>{host.agent_version ?? "unknown"}</dd>
        <dt className="text-text-muted">Images</dt>
        <dd className="col-span-3">
          {host.images.length > 0
            ? host.images.map((i) => `${i.slug} (${i.version})`).join(", ")
            : "none"}
        </dd>
      </dl>

      {patch.isError && <Notice tone="danger">{patch.error.message}</Notice>}
      <div className="flex flex-wrap items-center gap-2">
        <label htmlFor={`state-${host.id}`} className="text-sm text-text-muted">
          New PCs
        </label>
        <select
          id={`state-${host.id}`}
          value={host.admin_state}
          disabled={patch.isPending}
          onChange={(e) =>
            patch.mutate({ id: host.id, admin_state: e.target.value as Host["admin_state"] })
          }
          className="h-9 rounded-control border border-border bg-bg px-2 text-sm text-text"
        >
          <option value="active">Active: accepts new PCs</option>
          <option value="draining">Draining: no new PCs, existing ones still start</option>
          <option value="disabled">Disabled: nothing starts here</option>
        </select>
      </div>
    </Card>
  );
}

function CopyRow({ label, value }: { label: string; value: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex flex-col gap-1">
      <span className="text-sm font-medium">{label}</span>
      <div className="flex gap-2">
        <code className="min-w-0 flex-1 overflow-x-auto rounded-control border border-border bg-bg px-3 py-2 font-mono text-xs whitespace-pre">
          {value}
        </code>
        <Button
          variant="secondary"
          onClick={() => {
            void navigator.clipboard.writeText(value).then(() => setCopied(true));
          }}
        >
          {copied ? "Copied" : "Copy"}
        </Button>
      </div>
    </div>
  );
}

function NewHostSecrets({ created, onDone }: { created: CreatedHost; onDone: () => void }) {
  const env = [
    `ZYN_AGENT_HOST_ID=${created.host.id}`,
    `ZYN_AGENT_TOKEN=${created.agent_token}`,
    `ZYN_AGENT_STREAM_SECRET=${created.stream_secret}`,
  ].join("\n");
  return (
    <Card className="flex flex-col gap-4 border-warning/40">
      <h2 className="text-lg font-semibold">Host “{created.host.name}” created</h2>
      <Notice tone="warning">
        These secrets are shown only now. Copy them into <code>/etc/zynthera/agent.env</code> on the
        host before leaving this page.
      </Notice>
      <CopyRow label="Lines for agent.env" value={env} />
      <div>
        <Button onClick={onDone}>I have saved them</Button>
      </div>
    </Card>
  );
}

function AddHost() {
  const create = useCreateHost();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [agentUrl, setAgentUrl] = useState("");
  const [publicUrl, setPublicUrl] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState("");
  const [created, setCreated] = useState<CreatedHost | null>(null);

  if (created) {
    return (
      <NewHostSecrets
        created={created}
        onDone={() => {
          setCreated(null);
          setOpen(false);
          setName("");
          setAgentUrl("");
          setPublicUrl("");
        }}
      />
    );
  }
  if (!open) {
    return (
      <div>
        <Button variant="secondary" onClick={() => setOpen(true)}>
          Add a host
        </Button>
      </div>
    );
  }

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    setErrors({});
    setFormError("");
    create.mutate(
      { name: name.trim(), agent_url: agentUrl.trim(), public_url: publicUrl.trim() },
      {
        onSuccess: setCreated,
        onError: (err) => {
          if (err instanceof ApiError && Object.keys(err.fields).length > 0) setErrors(err.fields);
          else setFormError(err.message);
        },
      },
    );
  }

  return (
    <Card>
      <form onSubmit={onSubmit} noValidate className="flex flex-col gap-4">
        <h2 className="text-lg font-semibold">Add a host</h2>
        {formError && <Notice tone="danger">{formError}</Notice>}
        <Field
          id="host-name"
          label="Name"
          placeholder="host-2"
          value={name}
          onChange={(e) => setName(e.target.value)}
          error={errors.name}
        />
        <Field
          id="host-agent-url"
          label="Agent address (used by the control plane)"
          placeholder="https://host-2.example.com"
          value={agentUrl}
          onChange={(e) => setAgentUrl(e.target.value)}
          error={errors.agent_url}
        />
        <Field
          id="host-public-url"
          label="Public address (used by browsers)"
          placeholder="https://host-2.example.com"
          value={publicUrl}
          onChange={(e) => setPublicUrl(e.target.value)}
          error={errors.public_url}
        />
        <div className="flex gap-2">
          <Button type="submit" busy={create.isPending}>
            Create host
          </Button>
          <Button type="button" variant="ghost" onClick={() => setOpen(false)}>
            Cancel
          </Button>
        </div>
      </form>
    </Card>
  );
}

function AdminHostsPageContent() {
  const hosts = useHosts();
  if (hosts.isError) return <Notice tone="danger">{hosts.error.message}</Notice>;
  if (!hosts.data) return <PageLoading />;
  return (
    <div className="flex flex-col gap-4">
      {hosts.data.hosts.length === 0 && <Notice>No hosts yet. Add one to run PCs.</Notice>}
      {hosts.data.hosts.map((host) => (
        <HostCard key={host.id} host={host} headroom={hosts.data.disk_headroom_percent} />
      ))}
      <AddHost />
    </div>
  );
}

export default function AdminHostsPage() {
  return (
    <AdminOnly>
      <AdminHostsPageContent />
    </AdminOnly>
  );
}
