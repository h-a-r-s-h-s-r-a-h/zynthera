"use client";

import { RequireAuth } from "@/components/auth-guard";
import { Button, ButtonLink, Card, Notice, PageLoading } from "@/components/ui";
import { VMCard } from "@/components/vm/vm-card";
import { useMe, useResendVerification } from "@/lib/api/auth";
import { useVMs } from "@/lib/api/vms";

function VerifyBanner() {
  const resend = useResendVerification();
  return (
    <div className="flex flex-col gap-3 rounded-card border border-warning/40 bg-surface p-4 sm:flex-row sm:items-center sm:justify-between">
      <div>
        <p className="font-medium">Confirm your email to create a PC</p>
        <p className="text-sm text-text-muted">
          {resend.isSuccess
            ? "A new link is on its way to your inbox."
            : resend.isError
              ? resend.error.message
              : "We sent you a link when you signed up."}
        </p>
      </div>
      {!resend.isSuccess && (
        <Button variant="secondary" busy={resend.isPending} onClick={() => resend.mutate()}>
          Send a new link
        </Button>
      )}
    </div>
  );
}

function Dashboard() {
  const me = useMe();
  const vms = useVMs();
  const user = me.data;
  if (!user) return null;

  return (
    <section className="mx-auto flex w-full max-w-5xl flex-1 flex-col gap-6 px-4 py-10">
      <div className="flex items-center justify-between gap-4">
        <h1 className="text-2xl font-semibold tracking-tight">Your PCs</h1>
        {user.email_verified && vms.data && vms.data.length > 0 && (
          <ButtonLink href="/vms/new" variant="secondary">
            New PC
          </ButtonLink>
        )}
      </div>

      {!user.email_verified && <VerifyBanner />}

      {vms.isError ? (
        <Notice tone="danger">{vms.error.message}</Notice>
      ) : !vms.data ? (
        <PageLoading />
      ) : vms.data.length === 0 ? (
        <Card className="flex flex-col items-start gap-3 py-10">
          <p className="text-lg font-medium">You have no PC yet</p>
          <p className="max-w-md text-sm text-text-muted">
            Create one in about a minute: pick an operating system and a size, and it opens right
            here in your browser.
          </p>
          {user.email_verified ? (
            <ButtonLink href="/vms/new">Create your first PC</ButtonLink>
          ) : (
            <p className="text-sm text-text-muted">Confirm your email first, then come back.</p>
          )}
        </Card>
      ) : (
        <div className="grid gap-4 md:grid-cols-2">
          {vms.data.map((vm) => (
            <VMCard key={vm.id} vm={vm} />
          ))}
        </div>
      )}
    </section>
  );
}

export default function DashboardPage() {
  return (
    <RequireAuth>
      <Dashboard />
    </RequireAuth>
  );
}
