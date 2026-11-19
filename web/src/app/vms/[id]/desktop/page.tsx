"use client";

import { useParams } from "next/navigation";
import { Suspense } from "react";
import { RequireAuth } from "@/components/auth-guard";
import { ButtonLink, Notice, PageLoading } from "@/components/ui";
import { DesktopViewer } from "@/components/vm/desktop-viewer";
import { useVM } from "@/lib/api/vms";

function Desktop() {
  const { id } = useParams<{ id: string }>();
  const { vm, error } = useVM(id);

  if (error) {
    return (
      <div className="mx-auto w-full max-w-md px-4 py-16">
        <Notice tone="danger">{error.message}</Notice>
      </div>
    );
  }
  if (vm === undefined) return <PageLoading />;
  if (vm === null) {
    return (
      <div className="mx-auto flex w-full max-w-md flex-col items-start gap-4 px-4 py-16">
        <p className="text-lg font-medium">This PC does not exist</p>
        <ButtonLink href="/dashboard">Back to your PCs</ButtonLink>
      </div>
    );
  }
  return <DesktopViewer vm={vm} />;
}

export default function DesktopPage() {
  return (
    <RequireAuth>
      <Suspense fallback={<PageLoading />}>
        <Desktop />
      </Suspense>
    </RequireAuth>
  );
}
