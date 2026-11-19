"use client";

import { useParams, useRouter } from "next/navigation";
import { Suspense, useState, type FormEvent } from "react";
import { RequireAuth } from "@/components/auth-guard";
import { Button, ButtonLink, Card, Choice, Field, Notice, PageLoading } from "@/components/ui";
import { StateBadge } from "@/components/vm/state-badge";
import type { VM } from "@/lib/api/types";
import {
  useDeleteVM,
  useOptions,
  usePatchVM,
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

function History({ vm }: { vm: VM }) {
  const history = useVMHistory(vm.id, vm.state);
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
