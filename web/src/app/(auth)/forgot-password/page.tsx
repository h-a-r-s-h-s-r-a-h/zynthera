"use client";

import { useState, type FormEvent } from "react";
import { AuthShell } from "@/components/auth-shell";
import { Button, ButtonLink, Field, Notice, TextLink } from "@/components/ui";
import { useForgotPassword } from "@/lib/api/auth";
import { ApiError } from "@/lib/api/client";
import { fieldErrors, forgotPasswordSchema } from "@/lib/validation";

export default function ForgotPasswordPage() {
  const forgot = useForgotPassword();
  const [email, setEmail] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState("");
  const [sentTo, setSentTo] = useState("");

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    setFormError("");
    const parsed = forgotPasswordSchema.safeParse({ email });
    if (!parsed.success) {
      setErrors(fieldErrors(parsed.error));
      return;
    }
    setErrors({});
    forgot.mutate(parsed.data.email, {
      onSuccess: () => setSentTo(parsed.data.email),
      onError: (err) => {
        if (err instanceof ApiError && Object.keys(err.fields).length > 0) setErrors(err.fields);
        else setFormError(err.message);
      },
    });
  }

  if (sentTo) {
    return (
      <AuthShell
        title="Check your email"
        intro={
          <>
            If <span className="font-medium text-text">{sentTo}</span> has a Zynthera account, we
            just sent it a link to choose a new password. The link works for 1 hour.
          </>
        }
      >
        <ButtonLink href="/login" variant="secondary">
          Back to log in
        </ButtonLink>
      </AuthShell>
    );
  }

  return (
    <AuthShell
      title="Reset your password"
      intro="Enter your account's email and we will send you a link to choose a new password."
      footer={
        <>
          Remembered it? <TextLink href="/login">Log in</TextLink>
        </>
      }
    >
      <form onSubmit={onSubmit} noValidate className="flex flex-col gap-4">
        {formError && <Notice tone="danger">{formError}</Notice>}
        <Field
          id="email"
          label="Email"
          type="email"
          autoComplete="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          error={errors.email}
        />
        <Button type="submit" busy={forgot.isPending}>
          Send reset link
        </Button>
      </form>
    </AuthShell>
  );
}
