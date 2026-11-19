import type { VM } from "@/lib/api/types";
import { stateLabel, stateTone, type Tone } from "@/lib/vm-state";

const toneClass: Record<Tone, string> = {
  neutral: "border-border text-text-muted",
  success: "border-success/40 text-success",
  warning: "border-warning/40 text-warning",
  danger: "border-danger/40 text-danger",
  busy: "border-accent/40 text-accent",
};

export function StateBadge({ vm }: { vm: VM }) {
  const tone = stateTone(vm.state);
  return (
    <span
      data-state={vm.state}
      className={`inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs font-medium ${toneClass[tone]}`}
    >
      {tone === "busy" ? (
        <span
          aria-hidden
          className="size-3 animate-spin rounded-full border-2 border-current border-t-transparent"
        />
      ) : (
        <span aria-hidden className="size-1.5 rounded-full bg-current" />
      )}
      {stateLabel(vm)}
    </span>
  );
}
