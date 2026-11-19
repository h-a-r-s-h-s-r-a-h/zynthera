"use client";

import { useSearchParams } from "next/navigation";
import { Suspense, useEffect, useRef } from "react";
import { AuthShell } from "@/components/auth-shell";
import { Button, ButtonLink, Notice, PageLoading } from "@/components/ui";
import { useMe, useResendVerification, useVerifyEmail } from "@/lib/api/auth";
import { useHydrated } from "@/lib/use-hydrated";

function ResendButton() {
  const resend = useResendVerification();
  if (resend.isSuccess)
    return <Notice tone="success">A new link is on its way to your inbox.</Notice>;
  return (
    <div className="flex flex-col gap-3">
      {resend.isError && <Notice tone="danger">{resend.error.message}</Notice>}
      <Button variant="secondary" busy={resend.isPending} onClick={() => resend.mutate()}>
        Send a new link
      </Button>
    </div>
  );
}

function VerifyEmail() {
  const token = useSearchParams().get("token") ?? "";
  const verify = useVerifyEmail();
  const me = useMe();
  const started = useRef(false);
  const { mutate } = verify;

  // Use the emailed link exactly once, even if the effect runs twice.
  useEffect(() => {
    if (!token || started.current) return;
    started.current = true;
    mutate(token);
  }, [token, mutate]);

  const hydrated = useHydrated();
  const loggedIn = Boolean(me.data);

  if (!hydrated) return <PageLoading />;
  if (me.data?.email_verified || verify.isSuccess) {
    return (
      <AuthShell title="Email confirmed" intro="Thanks. You can now create your PC.">
        <ButtonLink href={loggedIn ? "/dashboard" : "/login"}>
          {loggedIn ? "Go to your dashboard" : "Log in"}
        </ButtonLink>
      </AuthShell>
    );
  }

  if (token && !verify.isError) return <PageLoading label="Confirming your email…" />;

  return (
    <AuthShell
      title={token ? "This link did not work" : "Confirm your email"}
      intro={
        token
          ? "It may have expired or been used already."
          : "Open the link in the email we sent you. If you cannot find it, we can send a new one."
      }
    >
      {verify.isError && verify.error.message && (
        <Notice tone="danger">{verify.error.message}</Notice>
      )}
      {loggedIn ? (
        <ResendButton />
      ) : (
        <ButtonLink href="/login" variant="secondary">
          Log in to get a new link
        </ButtonLink>
      )}
    </AuthShell>
  );
}

export default function VerifyEmailPage() {
  return (
    <Suspense fallback={<PageLoading />}>
      <VerifyEmail />
    </Suspense>
  );
}
