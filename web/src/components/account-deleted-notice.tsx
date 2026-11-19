"use client";

import { useSearchParams } from "next/navigation";
import { Notice } from "./ui";

/** Shown on the landing page right after someone deleted their account. */
export function AccountDeletedNotice() {
  const params = useSearchParams();
  if (params.get("account") !== "deleted") return null;
  return (
    <Notice tone="success">
      Your account was deleted. Your PCs and their data are being removed now.
    </Notice>
  );
}
