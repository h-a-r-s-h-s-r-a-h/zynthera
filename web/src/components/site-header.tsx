"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import { useLogout, useMe } from "@/lib/api/auth";
import type { VM } from "@/lib/api/types";
import { vmsKey } from "@/lib/api/vms";
import { keepAliveActive } from "@/lib/vm-state";
import { useHydrated } from "@/lib/use-hydrated";
import { Button, ButtonLink } from "./ui";

export function SiteHeader() {
  const router = useRouter();
  const pathname = usePathname();
  const me = useMe();
  const logout = useLogout();
  const queryClient = useQueryClient();

  function onLogout() {
    // Logging out suspends running PCs (unless keep-alive is on). Say so
    // first, so nobody is surprised.
    const vms = queryClient.getQueryData<VM[]>(vmsKey) ?? [];
    const willSuspend = vms.filter((vm) => vm.state === "running" && !keepAliveActive(vm));
    if (willSuspend.length > 0) {
      const what =
        willSuspend.length === 1 ? `"${willSuspend[0].name}" is` : `${willSuspend.length} PCs are`;
      const ok = window.confirm(
        `${what} running and will be suspended when you log out. Everything stays as it is and resumes when you come back. Log out?`,
      );
      if (!ok) return;
    }
    logout.mutate(undefined, { onSuccess: () => router.replace("/login") });
  }
  const hydrated = useHydrated();
  // Until hydration is done, render the same neutral header the server sent.
  const user = hydrated ? me.data : undefined;

  // The desktop viewer uses the whole window; its own toolbar leads back.
  if (pathname.endsWith("/desktop")) return null;

  return (
    <header className="border-b border-border">
      <div className="mx-auto flex h-14 w-full max-w-5xl items-center justify-between gap-4 px-4">
        <Link href={user ? "/dashboard" : "/"} className="text-lg font-semibold tracking-tight">
          Zynthera
        </Link>

        {/* Nothing is shown until we know whether someone is logged in. */}
        {user ? (
          <nav aria-label="Account" className="flex items-center gap-3">
            {user.role === "admin" && (
              <ButtonLink href="/admin/hosts" variant="ghost">
                Admin
              </ButtonLink>
            )}
            <Link
              href="/account"
              className="hidden max-w-56 truncate text-sm text-text-muted hover:text-text sm:inline"
              title={`${user.email} (account settings)`}
            >
              {user.email}
            </Link>
            <Button variant="secondary" busy={logout.isPending} onClick={onLogout}>
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
