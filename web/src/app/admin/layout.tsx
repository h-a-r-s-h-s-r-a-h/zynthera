import type { ReactNode } from "react";
import { Suspense } from "react";
import { AdminTabs } from "@/components/admin-tabs";

// The frame of the admin area. Who may see what is decided inside each page
// (AdminOnly) and, authoritatively, by the API.
export default function AdminLayout({ children }: { children: ReactNode }) {
  return (
    <section className="mx-auto flex w-full max-w-6xl flex-1 flex-col gap-6 px-4 py-10">
      <div className="flex flex-wrap items-center justify-between gap-4">
        <h1 className="text-2xl font-semibold tracking-tight">Admin</h1>
        {/* The tabs read the current path, which is only known in the browser. */}
        <Suspense fallback={<div className="h-9" />}>
          <AdminTabs />
        </Suspense>
      </div>
      {children}
    </section>
  );
}
