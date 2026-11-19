"use client";

import { useSearchParams } from "next/navigation";
import { Suspense, useState, type FormEvent } from "react";
import { AuthShell } from "@/components/auth-shell";
import { Button, ButtonLink, Field, Notice, PageLoading } from "@/components/ui";
import { useResetPassword } from "@/lib/api/auth";
import { ApiError } from "@/lib/api/client";
import { fieldErrors, resetPasswordSchema } from "@/lib/validation";

function ResetPassword() {
  const token = useSearchParams().get("token") ?? "";
  const reset = useResetPassword();
  const [password, setPassword] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState("");

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    setFormError("");
    const parsed = resetPasswordSchema.safeParse({ password });
    if (!parsed.success) {
      setErrors(fieldErrors(parsed.error));
      return;
    }
    setErrors({});
    reset.mutate(
      { token, password: parsed.data.password },
      {
        onError: (err) => {
          if (err instanceof ApiError && Object.keys(err.fields).length > 0) setErrors(err.fields);
          else setFormError(err.message);
        },
      },
    );
  }

  if (reset.isSuccess) {
    return (
      <AuthShell
        title="Password changed"
        intro="You were logged out everywhere. Log in with your new password."
      >
        <ButtonLink href="/login">Log in</ButtonLink>
      </AuthShell>
    );
  }

  if (!token) {
    return (
      <AuthShell
        title="This link is not complete"
        intro="Open the link from your email again, or ask for a new one."
      >
        <ButtonLink href="/forgot-password" variant="secondary">
          Send a new link
        </ButtonLink>
      </AuthShell>
    );
  }

  return (
    <AuthShell title="Choose a new password">
      <form onSubmit={onSubmit} noValidate className="flex flex-col gap-4">
        {formError && (
          <Notice tone="danger">
            {formError}{" "}
            <a href="/forgot-password" className="underline">
              Send a new link
            </a>
          </Notice>
        )}
        <Field
          id="password"
          label="New password"
          type="password"
          autoComplete="new-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          error={errors.password}
          hint="At least 10 characters."
        />
        <Button type="submit" busy={reset.isPending}>
          Change password
        </Button>
      </form>
    </AuthShell>
  );
}

export default function ResetPasswordPage() {
  return (
    <Suspense fallback={<PageLoading />}>
      <ResetPassword />
    </Suspense>
  );
}
