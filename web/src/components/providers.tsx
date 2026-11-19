"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { useMe } from "@/lib/api/auth";
import { useVMEventStream } from "@/lib/api/vms";

/** Keeps the list of PCs live for as long as someone is logged in. */
function LiveUpdates() {
  const me = useMe();
  useVMEventStream(Boolean(me.data));
  return null;
}

export function Providers({ children }: { children: ReactNode }) {
  // One client per browser tab, created once.
  const [client] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: { refetchOnWindowFocus: false, retry: 1 },
        },
      }),
  );
  return (
    <QueryClientProvider client={client}>
      <LiveUpdates />
      {children}
    </QueryClientProvider>
  );
}
