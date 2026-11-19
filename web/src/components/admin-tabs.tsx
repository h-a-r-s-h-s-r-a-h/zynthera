"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

const tabs = [
  { href: "/admin/hosts", label: "Hosts" },
  { href: "/admin/vms", label: "PCs" },
  { href: "/admin/users", label: "Users" },
  { href: "/admin/usage", label: "Usage" },
];

export function AdminTabs() {
  const pathname = usePathname();
  return (
    <nav aria-label="Admin sections" className="flex flex-wrap gap-1">
      {tabs.map((tab) => {
        const active = pathname.startsWith(tab.href);
        return (
          <Link
            key={tab.href}
            href={tab.href}
            aria-current={active ? "page" : undefined}
            className={`rounded-control px-3 py-1.5 text-sm font-medium ${
              active ? "bg-surface-raised text-text" : "text-text-muted hover:text-text"
            }`}
          >
            {tab.label}
          </Link>
        );
      })}
    </nav>
  );
}
