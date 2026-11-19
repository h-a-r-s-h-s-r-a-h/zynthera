"use client";

import { useParams, useRouter } from "next/navigation";
import { Suspense, useState, type FormEvent } from "react";
import { RequireAuth } from "@/components/auth-guard";
import { Button, ButtonLink, Card, Choice, Field, Notice, PageLoading } from "@/components/ui";
import { StateBadge } from "@/components/vm/state-badge";
import type { VM } from "@/lib/api/types";
import {
  useCreateSnapshot,
  useDeleteVM,
  useOptions,
  usePatchVM,
  useSnapshotAction,
  useSnapshots,
  useVM,
  useVMAction,
  useVMHistory,
} from "@/lib/api/vms";
import { vmNameSchema } from "@/lib/validation";
import { diskSummary, formatRAM, isBusy, keepAliveActive } from "@/lib/vm-state";

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <Card className="flex flex-col gap-4">
      <h2 className="text-lg font-semibold">{title}</h2>
      {children}
    </Card>
  );
}

const dateTime = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

function Rename({ vm }: { vm: VM }) {
  const patch = usePatchVM(vm.id);
  const [name, setName] = useState(vm.name);
  const [error, setError] = useState("");

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    const parsed = vmNameSchema.safeParse(name);
    if (!parsed.success) {
      setError(parsed.error.issues[0].message);
      return;
    }
    setError("");
    patch.mutate({ name: parsed.data }, { onError: (err) => setError(err.message) });
  }

  return (
    <Section title="Name">
      <form onSubmit={onSubmit} noValidate className="flex flex-col gap-3 sm:flex-row sm:items-end">
        <div className="flex-1">
          <Field
            id="name"
            label="PC name"
            value={name}
            maxLength={40}
            onChange={(e) => setName(e.target.value)}
            error={error}
          />
        </div>
        <Button
          type="submit"
          variant="secondary"
          busy={patch.isPending}
          disabled={name.trim() === vm.name}
        >
          Save
        </Button>
      </form>
    </Section>
  );
}

function KeepAlive({ vm }: { vm: VM }) {
  const patch = usePatchVM(vm.id);
  const options = useOptions();
  const max = options.data?.max_keep_alive_hours ?? 12;
  const active = keepAliveActive(vm);

  return (
    <Section title="Keep running when I disconnect">
      <p className="text-sm text-text-muted">
        Normally your PC is suspended when you log out or leave it idle. Keep it running for a while
        when it has work to finish, such as a long download.
      </p>
      {active && vm.keep_alive_until && (
        <Notice tone="success">
          Stays running until {dateTime.format(new Date(vm.keep_alive_until))}.
        </Notice>
      )}
      {patch.isError && <Notice tone="danger">{patch.error.message}</Notice>}
      <div role="radiogroup" aria-label="Keep running for" className="flex flex-wrap gap-2">
        {[1, 4, 8, 12]
          .filter((hours) => hours <= max)
          .map((hours) => (
            <Choice
              key={hours}
              selected={false}
              disabled={patch.isPending}
              title={`${hours} ${hours === 1 ? "hour" : "hours"}`}
              onSelect={() => patch.mutate({ keep_alive_hours: hours })}
            />
          ))}
        {active && (
          <Button
            variant="ghost"
            disabled={patch.isPending}
            onClick={() => patch.mutate({ keep_alive_hours: 0 })}
          >
            Turn off
          </Button>
        )}
      </div>
    </Section>
  );
}

function SavedSession({ vm }: { vm: VM }) {
  const act = useVMAction();
  if (!vm.allowed_actions.includes("discard_saved_state")) return null;
  return (
    <Section title="Saved session">
      <p className="text-sm text-text-muted">
        This PC has a saved session: its open windows and unsaved work, frozen as you left them.
        Discarding it means the PC starts fresh next time. Your files and installed software are not
        affected.
      </p>
      {act.isError && <Notice tone="danger">{act.error.message}</Notice>}
      <div>
        <Button
          variant="secondary"
          busy={act.isPending}
          onClick={() => {
            if (
              window.confirm("Discard the saved session? Open windows and unsaved work are lost.")
            )
              act.mutate({ id: vm.id, action: "discard-saved-state" });
          }}
        >
          Discard saved session
        </Button>
      </div>
    </Section>
  );
}

function Snapshots({ vm }: { vm: VM }) {
  const options = useOptions();
  const snapshots = useSnapshots(vm.id);
  const create = useCreateSnapshot(vm.id);
  const act = useSnapshotAction(vm.id);
  const [name, setName] = useState("");
  const [error, setError] = useState("");

  const max = options.data?.max_snapshots_per_vm ?? 5;
  const list = snapshots.data ?? [];
  const canCreate = vm.allowed_actions.includes("snapshot");
  const canRestore = vm.allowed_actions.includes("restore_snapshot");
  const canDelete = vm.allowed_actions.includes("delete_snapshot");

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    const trimmed = name.trim();
    if (trimmed.length < 1 || trimmed.length > 60) {
      setError("Give the snapshot a name of 1 to 60 characters.");
      return;
    }
    setError("");
    create.mutate(trimmed, {
      onSuccess: () => setName(""),
      onError: (err) => setError(err.message),
    });
  }

  return (
    <Section title="Snapshots">
      <p className="text-sm text-text-muted">
        A snapshot remembers the whole disk at one moment, so you can go back to it later. You can
        keep up to {max}. Taking one while the PC is running pauses it for a moment. Shutting down
        first gives the cleanest snapshot.
      </p>
      {snapshots.isError && <Notice tone="danger">{snapshots.error.message}</Notice>}
      {act.isError && <Notice tone="danger">{act.error.message}</Notice>}

      {list.length > 0 && (
        <ul className="flex flex-col divide-y divide-border text-sm" aria-label="Snapshots">
          {list.map((snap) => (
            <li key={snap.id} className="flex flex-wrap items-center justify-between gap-3 py-2">
              <div className="min-w-0">
                <p className="truncate font-medium">{snap.name}</p>
                <p className="text-xs text-text-muted">
                  {snap.status === "creating"
                    ? "Being taken…"
                    : snap.status === "error"
                      ? "Failed"
                      : dateTime.format(new Date(snap.created_at))}
                </p>
              </div>
              <div className="flex gap-2">
                <Button
                  variant="secondary"
                  disabled={!canRestore || snap.status !== "ready" || act.isPending}
                  onClick={() => {
                    const ok = window.confirm(
                      `Restore "${snap.name}"? The current session ends, and everything changed since this snapshot is lost.`,
                    );
                    if (ok) act.mutate({ snapshotId: snap.id, action: "restore" });
                  }}
                >
                  Restore
                </Button>
                <Button
                  variant="ghost"
                  disabled={!canDelete || snap.status === "creating" || act.isPending}
                  onClick={() => {
                    if (window.confirm(`Delete the snapshot "${snap.name}"?`))
                      act.mutate({ snapshotId: snap.id, action: "delete" });
                  }}
                >
                  Delete
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}
      {list.length > 0 && !canRestore && vm.state === "running" && (
        <p className="text-sm text-text-muted">
          Suspend or shut down the PC to restore or delete a snapshot.
        </p>
      )}

      <form onSubmit={onSubmit} noValidate className="flex flex-col gap-3 sm:flex-row sm:items-end">
        <div className="flex-1">
          <Field
            id="snapshot-name"
            label="New snapshot"
            placeholder="Before the big update"
            value={name}
            maxLength={60}
            onChange={(e) => setName(e.target.value)}
            error={error}
          />
        </div>
        <Button
          type="submit"
          variant="secondary"
          busy={create.isPending}
          disabled={!canCreate || list.length >= max}
        >
          Take snapshot
        </Button>
      </form>
      {list.length >= max && (
        <p className="text-sm text-text-muted">Delete a snapshot to make room for a new one.</p>
      )}
    </Section>
  );
}

function Resize({ vm }: { vm: VM }) {
  const options = useOptions();
  const patch = usePatchVM(vm.id);
  const [ram, setRam] = useState(vm.ram_mb);
  const [vcpus, setVcpus] = useState(vm.vcpus);
  const [disk, setDisk] = useState(vm.disk_gb);

  const allowed = vm.allowed_actions.includes("resize");
  const changed = ram !== vm.ram_mb || vcpus !== vm.vcpus || disk !== vm.disk_gb;
  const why =
    vm.state === "suspended" || vm.has_saved_state
      ? "This PC has a saved session, and memory or CPU cannot change underneath it. Shut the PC down (or discard the saved session) first."
      : vm.state === "running"
        ? "Shut the PC down first. Its size can only change while it is off."
        : "The size can only change while the PC is shut down.";

  if (!options.data) return null;
  const o = options.data;
  // What this PC may grow to: the account's limit minus what other PCs use.
  const room = {
    ram: o.limits.max_ram_mb - o.usage.ram_mb + vm.ram_mb,
    vcpus: o.limits.max_vcpus - o.usage.vcpus + vm.vcpus,
    disk: o.limits.max_disk_gb - o.usage.disk_gb + vm.disk_gb,
  };

  return (
    <Section title="Size">
      {!allowed && <Notice>{why}</Notice>}
      {patch.isError && <Notice tone="danger">{patch.error.message}</Notice>}
      <div className="flex flex-col gap-4">
        <div className="flex flex-col gap-2">
          <span className="text-sm font-medium">Memory</span>
          <div role="radiogroup" aria-label="Memory" className="flex flex-wrap gap-2">
            {o.allowed_ram_mb.map((v) => (
              <Choice
                key={v}
                selected={ram === v}
                disabled={!allowed || v > room.ram}
                title={formatRAM(v)}
                onSelect={() => setRam(v)}
              />
            ))}
          </div>
        </div>
        <div className="flex flex-col gap-2">
          <span className="text-sm font-medium">CPU cores</span>
          <div role="radiogroup" aria-label="CPU cores" className="flex flex-wrap gap-2">
            {o.allowed_vcpus.map((v) => (
              <Choice
                key={v}
                selected={vcpus === v}
                disabled={!allowed || v > room.vcpus}
                title={String(v)}
                onSelect={() => setVcpus(v)}
              />
            ))}
          </div>
        </div>
        <div className="flex flex-col gap-2">
          <span className="text-sm font-medium">Disk</span>
          <div role="radiogroup" aria-label="Disk" className="flex flex-wrap gap-2">
            {o.allowed_disk_gb.map((v) => (
              <Choice
                key={v}
                selected={disk === v}
                // A disk can grow but never shrink.
                disabled={!allowed || v > room.disk || v < vm.disk_gb}
                title={`${v} GB`}
                onSelect={() => setDisk(v)}
              />
            ))}
          </div>
          <p className="text-xs text-text-muted">A disk can grow but not shrink.</p>
        </div>
      </div>
      <div>
        <Button
          variant="secondary"
          busy={patch.isPending}
          disabled={!allowed || !changed}
          onClick={() => patch.mutate({ ram_mb: ram, vcpus, disk_gb: disk })}
        >
          Change size
        </Button>
      </div>
    </Section>
  );
}

function History({ vm }: { vm: VM }) {
  const history = useVMHistory(vm.id);
  return (
    <Section title="History">
      {history.isError ? (
        <Notice tone="danger">{history.error.message}</Notice>
      ) : !history.data ? (
        <p className="text-sm text-text-muted">Loading…</p>
      ) : history.data.length === 0 ? (
        <p className="text-sm text-text-muted">Nothing has happened yet.</p>
      ) : (
        <ol className="flex flex-col divide-y divide-border text-sm">
          {history.data.map((event, i) => (
            <li
              key={`${event.created_at}-${i}`}
              className="flex items-baseline justify-between gap-4 py-2"
            >
              <span>{event.message || event.type}</span>
              <time dateTime={event.created_at} className="shrink-0 text-xs text-text-muted">
                {dateTime.format(new Date(event.created_at))}
              </time>
            </li>
          ))}
        </ol>
      )}
    </Section>
  );
}

function DeleteVM({ vm }: { vm: VM }) {
  const router = useRouter();
  const del = useDeleteVM();
  const [typed, setTyped] = useState("");
  const allowed = vm.allowed_actions.includes("delete");

  return (
    <Section title="Delete this PC">
      <p className="text-sm text-text-muted">
        Deleting removes the PC, its disk, its saved session and its snapshots for good. This cannot
        be undone.
      </p>
      {del.isError && <Notice tone="danger">{del.error.message}</Notice>}
      <form
        className="flex flex-col gap-3 sm:flex-row sm:items-end"
        onSubmit={(event) => {
          event.preventDefault();
          del.mutate(vm.id, { onSuccess: () => router.replace("/dashboard") });
        }}
      >
        <div className="flex-1">
          <Field
            id="confirm-delete"
            label={`Type the PC's name to confirm: ${vm.name}`}
            autoComplete="off"
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
          />
        </div>
        <Button
          type="submit"
          busy={del.isPending}
          disabled={!allowed || typed !== vm.name}
          className="bg-danger text-accent-contrast hover:bg-danger"
        >
          Delete PC
        </Button>
      </form>
      {!allowed && (
        <p className="text-sm text-text-muted">Wait until the current operation has finished.</p>
      )}
    </Section>
  );
}

function Settings() {
  const { id } = useParams<{ id: string }>();
  const { vm, error } = useVM(id);

  if (error) return <Notice tone="danger">{error.message}</Notice>;
  if (vm === undefined) return <PageLoading />;
  if (vm === null) {
    return (
      <div className="flex flex-col items-start gap-4">
        <p className="text-lg font-medium">This PC does not exist</p>
        <ButtonLink href="/dashboard">Back to your PCs</ButtonLink>
      </div>
    );
  }

  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <h1 className="truncate text-2xl font-semibold tracking-tight">{vm.name}</h1>
          <p className="text-sm text-text-muted">
            {vm.image.display_name} · {formatRAM(vm.ram_mb)} · {vm.vcpus}{" "}
            {vm.vcpus === 1 ? "core" : "cores"} · {diskSummary(vm)} · user {vm.linux_username}
          </p>
        </div>
        <div className="flex items-center gap-3">
          <StateBadge vm={vm} />
          <ButtonLink href="/dashboard" variant="secondary">
            Your PCs
          </ButtonLink>
        </div>
      </div>
      {vm.state === "error" && (
        <Notice tone="danger">{vm.state_detail ?? "Something went wrong with this PC."}</Notice>
      )}
      {isBusy(vm) && <Notice>An operation is running on this PC. Some settings are paused.</Notice>}
      {/* key: start each form fresh when another PC is opened */}
      <Rename key={`rename-${vm.id}`} vm={vm} />
      <KeepAlive vm={vm} />
      <SavedSession vm={vm} />
      <Snapshots vm={vm} />
      {/* key: forget unsaved choices once the size really changed */}
      <Resize key={`resize-${vm.id}-${vm.ram_mb}-${vm.vcpus}-${vm.disk_gb}`} vm={vm} />
      <History vm={vm} />
      <DeleteVM key={`delete-${vm.id}`} vm={vm} />
    </>
  );
}

export default function VMSettingsPage() {
  return (
    <RequireAuth>
      <section className="mx-auto flex w-full max-w-2xl flex-1 flex-col gap-6 px-4 py-10">
        <Suspense fallback={<PageLoading />}>
          <Settings />
        </Suspense>
      </section>
    </RequireAuth>
  );
}
