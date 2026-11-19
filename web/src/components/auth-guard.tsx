"use client";

import { useRouter } from "next/navigation";
import { useEffect, type ReactNode } from "react";
import { useMe } from "@/lib/api/auth";
import { useHydrated } from "@/lib/use-hydrated";
import { Notice, PageLoading } from "./ui";

/** Shows its children only to a logged-in user; sends everyone else to /login. */
export function RequireAuth({ children }: { children: ReactNode }) {
  const router = useRouter();
  const me = useMe();
  const hydrated = useHydrated();
  const loggedOut = me.data === null;

  useEffect(() => {
    if (loggedOut) router.replace("/login");
  }, [loggedOut, router]);

  if (!hydrated) return <PageLoading />;
  if (me.isError) {
    return (
      <div className="mx-auto w-full max-w-md px-4 py-16">
        <Notice tone="danger">{me.error.message}</Notice>
      </div>
    );
  }
  if (!me.data) return <PageLoading />;
  return <>{children}</>;
}

/** Shows its children only to visitors; sends logged-in users to the dashboard. */
export function GuestOnly({ children }: { children: ReactNode }) {
  const router = useRouter();
  const me = useMe();
  const hydrated = useHydrated();
  const loggedIn = Boolean(me.data);

  useEffect(() => {
    if (loggedIn) router.replace("/dashboard");
  }, [loggedIn, router]);

  // If the check itself fails, still show the form: the person may simply
  // need to log in, and the form reports its own errors.
  if (!hydrated || me.isPending || loggedIn) return <PageLoading />;
  return <>{children}</>;
}
