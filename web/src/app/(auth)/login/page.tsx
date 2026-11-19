"use client";

import { useRouter } from "next/navigation";
import { useState, type FormEvent } from "react";
import { AuthShell } from "@/components/auth-shell";
import { GuestOnly } from "@/components/auth-guard";
import { Button, Field, Notice, TextLink } from "@/components/ui";
import { useLogin } from "@/lib/api/auth";
import { ApiError } from "@/lib/api/client";
import { fieldErrors, loginSchema } from "@/lib/validation";

function LoginForm() {
  const router = useRouter();
  const login = useLogin();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState("");

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    setFormError("");
    const parsed = loginSchema.safeParse({ email, password });
    if (!parsed.success) {
      setErrors(fieldErrors(parsed.error));
      return;
    }
    setErrors({});
    login.mutate(parsed.data, {
      onSuccess: () => router.replace("/dashboard"),
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
        autoComplete="current-password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        error={errors.password}
      />
      <Button type="submit" busy={login.isPending}>
        Log in
      </Button>
      <p className="text-sm">
        <TextLink href="/forgot-password">Forgot your password?</TextLink>
      </p>
    </form>
  );
}

export default function LoginPage() {
  return (
    <GuestOnly>
      <AuthShell
        title="Log in"
        footer={
          <>
            New to Zynthera? <TextLink href="/signup">Create an account</TextLink>
          </>
        }
      >
        <LoginForm />
      </AuthShell>
    </GuestOnly>
  );
}
