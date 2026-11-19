import type { ReactNode } from "react";
import { Card } from "./ui";

/** The centered card every account page (login, sign-up, reset…) sits in. */
export function AuthShell({
  title,
  intro,
  children,
  footer,
}: {
  title: string;
  intro?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
}) {
  return (
    <section className="mx-auto flex w-full max-w-md flex-1 flex-col justify-center gap-4 px-4 py-12">
      <Card className="flex flex-col gap-5">
        <div className="flex flex-col gap-1.5">
          <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
          {intro && <p className="text-sm text-text-muted">{intro}</p>}
        </div>
        {children}
      </Card>
      {footer && <p className="text-center text-sm text-text-muted">{footer}</p>}
    </section>
  );
}
