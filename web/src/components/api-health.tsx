"use client";

import { useEffect, useState } from "react";
import { healthLabel, interpretHealth, type HealthState } from "@/lib/api/health";

const dotColor: Record<HealthState, string> = {
  checking: "bg-text-muted",
  healthy: "bg-success",
  degraded: "bg-warning",
  unreachable: "bg-danger",
};

async function checkHealth(signal: AbortSignal): Promise<HealthState> {
  try {
    const res = await fetch("/api/health", { cache: "no-store", signal });
    const body: unknown = await res.json().catch(() => null);
    return interpretHealth(res.status, body);
  } catch {
    return "unreachable";
  }
}

export function ApiHealth() {
  const [state, setState] = useState<HealthState>("checking");

  useEffect(() => {
    const controller = new AbortController();
    const run = () => {
      void checkHealth(controller.signal).then((next) => {
        if (!controller.signal.aborted) setState(next);
      });
    };
    run();
    const timer = setInterval(run, 10_000);
    return () => {
      controller.abort();
      clearInterval(timer);
    };
  }, []);

  return (
    <p
      role="status"
      data-state={state}
      className="inline-flex items-center gap-2 rounded-control border border-border bg-surface px-3 py-1.5 text-sm text-text-muted"
    >
      <span aria-hidden className={`size-2 rounded-full ${dotColor[state]}`} />
      {healthLabel[state]}
    </p>
  );
}
