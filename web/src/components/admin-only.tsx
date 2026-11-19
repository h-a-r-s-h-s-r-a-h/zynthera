"use client";

import type { ReactNode } from "react";
import { useMe } from "@/lib/api/auth";
import { RequireAuth } from "./auth-guard";
import { ButtonLink } from "./ui";

/**
 * Shows its children only to administrators.
 *
 * The API enforces this on every admin route. This component only makes
 * sure that a normal user who types an admin address sees something
 * sensible instead of a page full of "forbidden" errors.
 */
export function AdminOnly({ children }: { children: ReactNode }) {
  return (
    <RequireAuth>
      <Gate>{children}</Gate>
    </RequireAuth>
  );
}

function Gate({ children }: { children: ReactNode }) {
  const me = useMe();
  if (me.data && me.data.role !== "admin") {
    return (
      <div className="flex flex-col items-start gap-4 py-8">
        <p className="text-lg font-medium">This area is for administrators</p>
        <ButtonLink href="/dashboard">Back to your PCs</ButtonLink>
      </div>
    );
  }
  return <>{children}</>;
}
