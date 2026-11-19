"use client";

import { useState, type FormEvent } from "react";
import { RequireAuth } from "@/components/auth-guard";
import { Button, ButtonLink, Card, Field, Notice } from "@/components/ui";
import { useDeleteAccount, useMe } from "@/lib/api/auth";
import { ApiError } from "@/lib/api/client";

function DeleteAccount() {
  const del = useDeleteAccount();
  const [password, setPassword] = useState("");
  const [understood, setUnderstood] = useState(false);
  const [error, setError] = useState("");
  const [formError, setFormError] = useState("");

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    setError("");
    setFormError("");
    if (!password) {
      setError("Enter your password to confirm.");
      return;
    }
    del.mutate(password, {
      // A full page load on purpose, not a client-side navigation: it throws
      // away everything cached about the account that no longer exists, and
      // the login guard of this page cannot redirect to /login first.
      // eslint-disable-next-line @next/next/no-location-assign-relative-destination
      onSuccess: () => window.location.assign("/?account=deleted"),
      onError: (err) => {
        if (err instanceof ApiError && err.fields.password) setError(err.fields.password);
        else setFormError(err.message);
      },
    });
  }

  return (
    <Card className="flex flex-col gap-4">
      <h2 className="text-lg font-semibold">Delete your account</h2>
      <p className="text-sm text-text-muted">
        This deletes your account and every PC you have: their disks, saved sessions and snapshots.
        It cannot be undone, and we cannot recover anything afterwards.
      </p>
      {formError && <Notice tone="danger">{formError}</Notice>}
      <form onSubmit={onSubmit} noValidate className="flex flex-col gap-4">
        <Field
          id="delete-password"
          label="Your password"
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          error={error}
        />
        <label className="flex items-start gap-2 text-sm">
          <input
            type="checkbox"
            className="mt-0.5 size-4 accent-danger"
            checked={understood}
            onChange={(e) => setUnderstood(e.target.checked)}
          />
          <span>I understand that all my PCs and files will be deleted for good.</span>
        </label>
        <div>
          <Button
            type="submit"
            busy={del.isPending}
            disabled={!understood}
            className="bg-danger text-accent-contrast hover:bg-danger"
          >
            Delete my account
          </Button>
        </div>
      </form>
    </Card>
  );
}

function Account() {
  const me = useMe();
  if (!me.data) return null;
  return (
    <section className="mx-auto flex w-full max-w-2xl flex-1 flex-col gap-6 px-4 py-10">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-2xl font-semibold tracking-tight">Your account</h1>
        <ButtonLink href="/dashboard" variant="secondary">
          Your PCs
        </ButtonLink>
      </div>
      <Card className="flex flex-col gap-2">
        <h2 className="text-lg font-semibold">Login</h2>
        <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1 text-sm">
          <dt className="text-text-muted">Email</dt>
          <dd className="truncate font-medium">{me.data.email}</dd>
          <dt className="text-text-muted">Email confirmed</dt>
          <dd>{me.data.email_verified ? "Yes" : "Not yet"}</dd>
        </dl>
        <p className="text-sm text-text-muted">
          To change your password, log out and use “Forgot your password?” on the login page.
        </p>
      </Card>
      <DeleteAccount />
    </section>
  );
}

export default function AccountPage() {
  return (
    <RequireAuth>
      <Account />
    </RequireAuth>
  );
}
