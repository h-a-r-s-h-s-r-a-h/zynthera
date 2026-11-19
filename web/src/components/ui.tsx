import Link from "next/link";
import type { ComponentProps, ReactNode } from "react";

// Small shared building blocks. Every color and radius comes from the tokens
// in globals.css.

const buttonBase =
  "inline-flex h-10 items-center justify-center gap-2 rounded-control px-4 text-sm font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-60";

const buttonVariants = {
  primary: "bg-accent text-accent-contrast hover:bg-accent-hover",
  secondary: "border border-border bg-surface text-text hover:bg-surface-raised",
  ghost: "text-text-muted hover:bg-surface hover:text-text",
} as const;

type Variant = keyof typeof buttonVariants;

export function Button({
  variant = "primary",
  busy = false,
  className = "",
  children,
  disabled,
  ...props
}: ComponentProps<"button"> & { variant?: Variant; busy?: boolean }) {
  return (
    <button
      {...props}
      disabled={disabled || busy}
      aria-busy={busy}
      className={`${buttonBase} ${buttonVariants[variant]} ${className}`}
    >
      {busy && (
        <span
          aria-hidden
          className="size-4 animate-spin rounded-full border-2 border-current border-t-transparent"
        />
      )}
      {children}
    </button>
  );
}

export function ButtonLink({
  variant = "primary",
  className = "",
  ...props
}: ComponentProps<typeof Link> & { variant?: Variant }) {
  return <Link {...props} className={`${buttonBase} ${buttonVariants[variant]} ${className}`} />;
}

export function Field({
  label,
  error,
  hint,
  id,
  ...props
}: ComponentProps<"input"> & { label: string; id: string; error?: string; hint?: string }) {
  const describedBy = error ? `${id}-error` : hint ? `${id}-hint` : undefined;
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={id} className="text-sm font-medium">
        {label}
      </label>
      <input
        {...props}
        id={id}
        aria-invalid={error ? true : undefined}
        aria-describedby={describedBy}
        className={`h-10 rounded-control border bg-bg px-3 text-sm text-text placeholder:text-text-muted ${
          error ? "border-danger" : "border-border"
        }`}
      />
      {error ? (
        <p id={`${id}-error`} className="text-sm text-danger">
          {error}
        </p>
      ) : hint ? (
        <p id={`${id}-hint`} className="text-sm text-text-muted">
          {hint}
        </p>
      ) : null}
    </div>
  );
}

const noticeTone = {
  info: "border-border text-text-muted",
  success: "border-success/40 text-success",
  warning: "border-warning/40 text-warning",
  danger: "border-danger/40 text-danger",
} as const;

export function Notice({
  tone = "info",
  children,
}: {
  tone?: keyof typeof noticeTone;
  children: ReactNode;
}) {
  return (
    <div
      role={tone === "danger" ? "alert" : "status"}
      className={`rounded-control border bg-surface px-3 py-2 text-sm ${noticeTone[tone]}`}
    >
      {children}
    </div>
  );
}

export function Card({ children, className = "" }: { children: ReactNode; className?: string }) {
  return (
    <div className={`rounded-card border border-border bg-surface p-6 ${className}`}>
      {children}
    </div>
  );
}

export function TextLink(props: ComponentProps<typeof Link>) {
  return <Link {...props} className="font-medium text-accent hover:text-accent-hover" />;
}

/** A centered spinner for "we do not know yet" states. */
export function PageLoading({ label = "Loading…" }: { label?: string }) {
  return (
    <div
      role="status"
      className="flex flex-1 items-center justify-center gap-3 py-24 text-text-muted"
    >
      <span
        aria-hidden
        className="size-5 animate-spin rounded-full border-2 border-current border-t-transparent"
      />
      <span className="text-sm">{label}</span>
    </div>
  );
}
