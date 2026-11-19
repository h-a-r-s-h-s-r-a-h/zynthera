"use client";

import { useState, type FormEvent } from "react";
import { AuthShell } from "@/components/auth-shell";
import { GuestOnly } from "@/components/auth-guard";
import { Button, ButtonLink, Field, Notice, TextLink } from "@/components/ui";
import { useSignup } from "@/lib/api/auth";
import { ApiError } from "@/lib/api/client";
import { fieldErrors, signupSchema } from "@/lib/validation";

function SignupForm({ onDone }: { onDone: (email: string) => void }) {
  const signup = useSignup();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState("");

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    setFormError("");
    const parsed = signupSchema.safeParse({ email, password });
    if (!parsed.success) {
      setErrors(fieldErrors(parsed.error));
      return;
    }
    setErrors({});
    signup.mutate(parsed.data, {
      onSuccess: () => onDone(parsed.data.email),
      onError: (err) => {
        if (err instanceof ApiError && Object.keys(err.fields).length > 0) setErrors(err.fields);
        else setFormError(err.message);
      },
    });
  }

  return (
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
      <Field
        id="password"
        label="Password"
        type="password"
        autoComplete="new-password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        error={errors.password}
        hint="At least 10 characters."
      />
      <Button type="submit" busy={signup.isPending}>
        Create account
      </Button>
    </form>
  );
}

export default function SignupPage() {
  const [sentTo, setSentTo] = useState("");

  if (sentTo) {
    return (
      <AuthShell
        title="Check your email"
        intro={
          <>
            If <span className="font-medium text-text">{sentTo}</span> is not registered yet, we
            just sent it a link to confirm the address. The link works for 24 hours.
          </>
        }
      >
        <p className="text-sm text-text-muted">
          Already have an account with this address? Then no email was sent. Log in, or reset your
          password if you have forgotten it.
        </p>
        <ButtonLink href="/login">Continue to log in</ButtonLink>
      </AuthShell>
    );
  }

  return (
    <GuestOnly>
      <AuthShell
        title="Create your account"
        intro="One account, one Linux PC that is always how you left it."
        footer={
          <>
            Already have an account? <TextLink href="/login">Log in</TextLink>
          </>
        }
      >
        <SignupForm onDone={setSentTo} />
      </AuthShell>
    </GuestOnly>
  );
}
