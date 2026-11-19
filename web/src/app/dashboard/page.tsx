"use client";

import { RequireAuth } from "@/components/auth-guard";
import { Button, Card, Notice } from "@/components/ui";
import { useMe, useResendVerification } from "@/lib/api/auth";

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
  const user = me.data;
  if (!user) return null;

  return (
    <section className="mx-auto flex w-full max-w-5xl flex-1 flex-col gap-6 px-4 py-10">
      <h1 className="text-2xl font-semibold tracking-tight">Your PCs</h1>
      {!user.email_verified && <VerifyBanner />}
      <Card className="flex flex-col items-start gap-2 py-10">
        <p className="text-lg font-medium">You have no PC yet</p>
        <p className="max-w-md text-sm text-text-muted">
          Creating a PC arrives in the next build step. Your account is ready for it.
        </p>
      </Card>
      {user.role === "admin" && <Notice>You are signed in as an administrator.</Notice>}
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
