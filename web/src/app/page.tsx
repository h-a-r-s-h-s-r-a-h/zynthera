import { ApiHealth } from "@/components/api-health";

export default function LandingPage() {
  return (
    <section className="mx-auto flex w-full max-w-5xl flex-1 flex-col items-start justify-center gap-6 px-4 py-16">
      <h1 className="max-w-2xl text-4xl font-semibold tracking-tight sm:text-5xl">
        Your own Linux computer, in a browser tab.
      </h1>
      <p className="max-w-xl text-lg text-text-muted">
        Pick the size, install anything, and close the tab whenever you like. Your PC is saved
        exactly as you left it and resumes when you come back.
      </p>
      <ApiHealth />
    </section>
  );
}
