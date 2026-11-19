"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useLogout, useMe } from "@/lib/api/auth";
import { useHydrated } from "@/lib/use-hydrated";
import { Button, ButtonLink } from "./ui";

export function SiteHeader() {
  const router = useRouter();
  const me = useMe();
  const logout = useLogout();
  const hydrated = useHydrated();
  // Until hydration is done, render the same neutral header the server sent.
  const user = hydrated ? me.data : undefined;

  return (
    <header className="border-b border-border">
      <div className="mx-auto flex h-14 w-full max-w-5xl items-center justify-between gap-4 px-4">
        <Link href={user ? "/dashboard" : "/"} className="text-lg font-semibold tracking-tight">
          Zynthera
        </Link>

        {/* Nothing is shown until we know whether someone is logged in. */}
        {user ? (
          <nav aria-label="Account" className="flex items-center gap-3">
            <span
              className="hidden max-w-56 truncate text-sm text-text-muted sm:inline"
              title={user.email}
            >
              {user.email}
            </span>
            <Button
              variant="secondary"
              busy={logout.isPending}
              onClick={() =>
                logout.mutate(undefined, { onSuccess: () => router.replace("/login") })
              }
            >
              Log out
            </Button>
          </nav>
        ) : user === null ? (
          <nav aria-label="Account" className="flex items-center gap-2">
            <ButtonLink href="/login" variant="ghost">
              Log in
            </ButtonLink>
            <ButtonLink href="/signup">Sign up</ButtonLink>
          </nav>
        ) : null}
      </div>
    </header>
  );
}
