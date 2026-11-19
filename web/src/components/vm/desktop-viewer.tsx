"use client";

import { useRouter } from "next/navigation";
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import type { VM } from "@/lib/api/types";
import { useOptions, useVMAction, type SimpleAction } from "@/lib/api/vms";
import { keepAliveActive, reconnectDelay, viewerPhase } from "@/lib/vm-state";
import { Button, ButtonLink, Notice } from "../ui";
import { FilesPanel, useUploads } from "./files-panel";
import { VncSession, type StreamEnd, type VncHandle } from "./vnc-session";

/** State of the stream to a running PC. */
type Link =
  | { kind: "connecting" }
  | { kind: "connected" }
  | { kind: "reconnecting"; attempt: number }
  | { kind: "gave-up" }
  | { kind: "replaced" }
  | { kind: "idle" };

const idleCountdownSeconds = 60;

function Screen({ children }: { children: ReactNode }) {
  return (
    <div className="absolute inset-0 flex flex-col items-center justify-center gap-4 p-6 text-center">
      {children}
    </div>
  );
}

function Spinner({ label }: { label: string }) {
  return (
    <div role="status" className="flex items-center gap-3 text-text-muted">
      <span
        aria-hidden
        className="size-5 animate-spin rounded-full border-2 border-current border-t-transparent"
      />
      <span>{label}</span>
    </div>
  );
}

export function DesktopViewer({ vm }: { vm: VM }) {
  const router = useRouter();
  const act = useVMAction();
  const options = useOptions();
  const phase = viewerPhase(vm);

  const frame = useRef<HTMLDivElement>(null);
  const vnc = useRef<VncHandle>(null);
  const lastInput = useRef(0);

  const [link, setLink] = useState<Link>({ kind: "connecting" });
  // Changing this number mounts a fresh VncSession: one new connection attempt.
  const [session, setSession] = useState(0);
  const [fullscreen, setFullscreen] = useState(false);
  // The toolbar hides itself; the tab below it pins it open (needed on
  // touch screens, where nothing can hover).
  const [toolbarPinned, setToolbarPinned] = useState(false);
  const [clipboardOpen, setClipboardOpen] = useState(false);
  const [filesOpen, setFilesOpen] = useState(false);
  const [dropping, setDropping] = useState(false);
  const uploads = useUploads(vm.id);
  const [clipboard, setClipboard] = useState("");
  const [idleLeft, setIdleLeft] = useState<number | null>(null);

  const reconnectNow = useCallback(() => {
    setLink({ kind: "connecting" });
    setSession((n) => n + 1);
  }, []);

  // ---- start a PC that is off, once, when the page opens
  const autoStarted = useRef(false);
  const { mutate: runAction } = act;
  useEffect(() => {
    if (phase.kind === "start" && !autoStarted.current) {
      autoStarted.current = true;
      runAction({ id: vm.id, action: "start" });
    }
  }, [phase.kind, vm.id, runAction]);

  // ---- a new connection whenever the PC (re)enters "running"
  const wasRunning = useRef(phase.kind === "connect");
  useEffect(() => {
    const running = phase.kind === "connect";
    if (running && !wasRunning.current) reconnectNow();
    wasRunning.current = running;
  }, [phase.kind, reconnectNow]);

  // ---- reconnect with back-off after the stream dropped
  useEffect(() => {
    if (link.kind !== "reconnecting") return;
    const delay = reconnectDelay(link.attempt);
    // onEnded never produces an attempt beyond the last allowed one.
    if (delay === null) return;
    const timer = setTimeout(() => setSession((n) => n + 1), delay);
    return () => clearTimeout(timer);
  }, [link]);

  const onConnected = useCallback(() => {
    lastInput.current = Date.now();
    setLink({ kind: "connected" });
  }, []);

  const onEnded = useCallback((reason: StreamEnd) => {
    setLink((current) => {
      // We ended it ourselves; keep showing why.
      if (current.kind === "idle" || current.kind === "replaced") return current;
      if (reason === "replaced") return { kind: "replaced" };
      // "not-running": the state stream will move us to another screen. If
      // it does not (the PC is in fact running), treat it like a drop.
      const attempt = current.kind === "reconnecting" ? current.attempt + 1 : 0;
      // After the last allowed try, stop and offer a button instead.
      if (reconnectDelay(attempt) === null) return { kind: "gave-up" };
      return { kind: "reconnecting", attempt };
    });
  }, []);

  // ---- fullscreen, with keyboard lock so Esc, Alt+Tab etc. reach the PC
  useEffect(() => {
    const onChange = () => {
      const active = document.fullscreenElement === frame.current;
      setFullscreen(active);
      const keyboard = (
        navigator as Navigator & { keyboard?: { lock?: () => Promise<void>; unlock?: () => void } }
      ).keyboard;
      if (active) void keyboard?.lock?.().catch(() => undefined);
      else keyboard?.unlock?.();
      vnc.current?.focus();
    };
    document.addEventListener("fullscreenchange", onChange);
    return () => document.removeEventListener("fullscreenchange", onChange);
  }, []);

  const toggleFullscreen = () => {
    if (document.fullscreenElement) void document.exitFullscreen();
    else void frame.current?.requestFullscreen().catch(() => undefined);
  };

  // ---- "Still there?" after a long time without input
  const idleMinutes = options.data?.idle_input_minutes ?? 60;
  const connected = phase.kind === "connect" && link.kind === "connected";
  const keepAlive = keepAliveActive(vm);
  useEffect(() => {
    if (!connected || keepAlive) return;
    const timer = setInterval(() => {
      const idleFor = Date.now() - lastInput.current;
      const limit = idleMinutes * 60_000;
      if (idleFor < limit) {
        setIdleLeft(null);
        return;
      }
      const left = idleCountdownSeconds - Math.floor((idleFor - limit) / 1000);
      if (left > 0) {
        setIdleLeft(left);
        return;
      }
      // Nobody answered: disconnect. The PC is then suspended by the
      // normal idle rule and resumes when its owner comes back.
      setIdleLeft(null);
      setLink({ kind: "idle" });
      vnc.current?.disconnect();
    }, 1000);
    return () => clearInterval(timer);
  }, [connected, keepAlive, idleMinutes]);

  const noteInput = () => {
    lastInput.current = Date.now();
  };

  // ---- actions from the toolbar
  const leaveAfter = (action: SimpleAction) => {
    act.mutate({ id: vm.id, action }, { onSuccess: () => router.push("/dashboard") });
  };

  let body: ReactNode;
  switch (phase.kind) {
    case "gone":
      body = (
        <Screen>
          <p className="text-lg font-medium">This PC was deleted</p>
          <ButtonLink href="/dashboard">Back to your PCs</ButtonLink>
        </Screen>
      );
      break;
    case "error":
      body = (
        <Screen>
          <p className="text-lg font-medium">This PC needs attention</p>
          <p className="max-w-md text-sm text-text-muted">{phase.detail}</p>
          {act.isError && <Notice tone="danger">{act.error.message}</Notice>}
          <div className="flex flex-wrap justify-center gap-2">
            {vm.allowed_actions.includes("start") && (
              <Button
                busy={act.isPending}
                onClick={() => act.mutate({ id: vm.id, action: "start" })}
              >
                Try to start it
              </Button>
            )}
            {vm.allowed_actions.includes("discard_saved_state") && (
              <Button
                variant="secondary"
                disabled={act.isPending}
                onClick={() => {
                  if (
                    window.confirm(
                      "Discard the saved session? Open windows and unsaved work are lost. Your files are kept.",
                    )
                  )
                    act.mutate({ id: vm.id, action: "discard-saved-state" });
                }}
              >
                Discard saved session
              </Button>
            )}
            {vm.allowed_actions.includes("force_stop") && (
              <Button
                variant="secondary"
                disabled={act.isPending}
                onClick={() => act.mutate({ id: vm.id, action: "force-stop" })}
              >
                Force stop
              </Button>
            )}
            <ButtonLink href={`/vms/${vm.id}`} variant="ghost">
              Settings
            </ButtonLink>
          </div>
        </Screen>
      );
      break;
    case "wait":
      body = (
        <Screen>
          <Spinner label={phase.label} />
        </Screen>
      );
      break;
    case "start":
      body = act.isPending ? (
        <Screen>
          <Spinner label={phase.resuming ? "Resuming your PC…" : "Starting your PC…"} />
        </Screen>
      ) : (
        <Screen>
          <p className="text-lg font-medium">
            {phase.resuming ? "Your PC is suspended" : "Your PC is shut down"}
          </p>
          {act.isError && <Notice tone="danger">{act.error.message}</Notice>}
          {vm.state_detail && !act.isError && (
            <p className="max-w-md text-sm text-text-muted">{vm.state_detail}</p>
          )}
          <div className="flex gap-2">
            <Button onClick={() => act.mutate({ id: vm.id, action: "start" })}>
              {phase.resuming ? "Resume" : "Start"}
            </Button>
            <ButtonLink href="/dashboard" variant="secondary">
              Back to your PCs
            </ButtonLink>
          </div>
        </Screen>
      );
      break;
    case "connect":
      body = (
        <>
          {link.kind !== "replaced" && link.kind !== "idle" && link.kind !== "gave-up" && (
            <VncSession
              key={session}
              vmId={vm.id}
              handle={vnc}
              onConnected={onConnected}
              onEnded={onEnded}
              onClipboard={setClipboard}
            />
          )}
          {link.kind === "connecting" && (
            <Screen>
              <Spinner label="Connecting to your PC…" />
            </Screen>
          )}
          {link.kind === "reconnecting" && (
            <Screen>
              <Spinner label="Connection lost. Reconnecting…" />
            </Screen>
          )}
          {link.kind === "gave-up" && (
            <Screen>
              <p className="text-lg font-medium">Could not reconnect</p>
              <p className="max-w-md text-sm text-text-muted">
                Your PC is still running. Check your internet connection and try again.
              </p>
              <Button onClick={reconnectNow}>Reconnect</Button>
            </Screen>
          )}
          {link.kind === "replaced" && (
            <Screen>
              <p className="text-lg font-medium">This PC was opened in another tab</p>
              <p className="max-w-md text-sm text-text-muted">
                A PC can be shown in one place at a time.
              </p>
              <Button onClick={reconnectNow}>Use here</Button>
            </Screen>
          )}
          {link.kind === "idle" && (
            <Screen>
              <p className="text-lg font-medium">Disconnected because nobody was here</p>
              <p className="max-w-md text-sm text-text-muted">
                Your PC is suspended after a while and resumes exactly where you left it.
              </p>
              <Button onClick={reconnectNow}>Reconnect</Button>
            </Screen>
          )}
        </>
      );
      break;
  }

  const showToolbar = phase.kind === "connect" && link.kind === "connected";

  return (
    <div
      ref={frame}
      data-testid="desktop-viewer"
      data-link={phase.kind === "connect" ? link.kind : phase.kind}
      className="relative flex-1 overflow-hidden bg-bg"
      onPointerMoveCapture={noteInput}
      onPointerDownCapture={noteInput}
      onKeyDownCapture={noteInput}
      onWheelCapture={noteInput}
      onDragEnter={(event) => {
        if (showToolbar && event.dataTransfer.types.includes("Files")) setDropping(true);
      }}
      onDragOver={(event) => {
        // Without this the browser would open the dropped file instead.
        if (event.dataTransfer.types.includes("Files")) event.preventDefault();
      }}
      onDragLeave={(event) => {
        if (event.currentTarget === event.target) setDropping(false);
      }}
      onDrop={(event) => {
        event.preventDefault();
        setDropping(false);
        if (!showToolbar || event.dataTransfer.files.length === 0) return;
        uploads.add(event.dataTransfer.files);
        setFilesOpen(true);
      }}
    >
      {body}

      {showToolbar && (
        // The toolbar hides itself: only a small tab shows until the pointer
        // or the keyboard focus reaches it.
        <div className="group absolute left-1/2 top-0 z-10 -translate-x-1/2">
          <div
            className={`flex flex-col items-center transition-transform duration-150 focus-within:translate-y-0 group-hover:translate-y-0 ${
              toolbarPinned ? "translate-y-0" : "-translate-y-[calc(100%-0.75rem)]"
            }`}
          >
            <div
              role="toolbar"
              aria-label="PC controls"
              className="flex flex-wrap items-center gap-1 rounded-b-card border border-t-0 border-border bg-surface px-2 py-1.5 shadow-lg"
            >
              <span className="flex items-center gap-1.5 px-2 text-xs text-success">
                <span aria-hidden className="size-1.5 rounded-full bg-current" />
                Connected
              </span>
              <Button variant="ghost" onClick={toggleFullscreen}>
                {fullscreen ? "Exit fullscreen" : "Fullscreen"}
              </Button>
              <Button variant="ghost" onClick={() => vnc.current?.sendCtrlAltDel()}>
                Ctrl+Alt+Del
              </Button>
              <Button
                variant="ghost"
                aria-expanded={clipboardOpen}
                onClick={() => setClipboardOpen((v) => !v)}
              >
                Clipboard
              </Button>
              <Button
                variant="ghost"
                aria-expanded={filesOpen}
                onClick={() => {
                  setClipboardOpen(false);
                  setFilesOpen((v) => !v);
                }}
              >
                Files{uploads.active ? " (uploading…)" : ""}
              </Button>
              <Button
                variant="ghost"
                disabled={act.isPending}
                onClick={() => leaveAfter("suspend")}
              >
                Suspend
              </Button>
              <Button
                variant="ghost"
                disabled={act.isPending}
                onClick={() => leaveAfter("shutdown")}
              >
                Shut down
              </Button>
              <ButtonLink href="/dashboard" variant="ghost">
                Your PCs
              </ButtonLink>
            </div>
            <button
              type="button"
              aria-label={toolbarPinned ? "Hide PC controls" : "Show PC controls"}
              aria-expanded={toolbarPinned}
              onClick={() => setToolbarPinned((v) => !v)}
              className="h-3 w-20 rounded-b-control bg-border hover:bg-text-muted"
            />
          </div>
        </div>
      )}

      {showToolbar && clipboardOpen && (
        <div className="absolute right-4 top-12 z-10 flex w-80 max-w-[calc(100%-2rem)] flex-col gap-2 rounded-card border border-border bg-surface p-3 shadow-lg">
          <label htmlFor="clipboard" className="text-sm font-medium">
            Clipboard
          </label>
          <textarea
            id="clipboard"
            rows={4}
            value={clipboard}
            onChange={(e) => setClipboard(e.target.value)}
            className="rounded-control border border-border bg-bg p-2 text-sm text-text"
          />
          <p className="text-xs text-text-muted">
            Text copied inside the PC appears here. To paste into the PC, put text here, send it,
            then paste inside the PC.
          </p>
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => setClipboardOpen(false)}>
              Close
            </Button>
            <Button
              onClick={() => {
                vnc.current?.sendClipboard(clipboard);
                vnc.current?.focus();
              }}
            >
              Send to PC
            </Button>
          </div>
        </div>
      )}

      {showToolbar && filesOpen && (
        <FilesPanel vmId={vm.id} uploads={uploads} onClose={() => setFilesOpen(false)} />
      )}

      {showToolbar && dropping && (
        <div className="pointer-events-none absolute inset-0 z-20 flex items-center justify-center bg-bg/80">
          <p className="rounded-card border border-accent bg-surface px-6 py-4 text-lg font-medium">
            Drop to upload to your PC
          </p>
        </div>
      )}

      {connected && idleLeft !== null && (
        <div
          role="alertdialog"
          aria-labelledby="idle-title"
          className="absolute inset-0 z-20 flex items-center justify-center bg-bg/80 p-6"
        >
          <div className="flex max-w-sm flex-col items-center gap-3 rounded-card border border-border bg-surface p-6 text-center">
            <p id="idle-title" className="text-lg font-medium">
              Still there?
            </p>
            <p className="text-sm text-text-muted">
              Disconnecting in {idleLeft} {idleLeft === 1 ? "second" : "seconds"} to save resources.
              Your PC will be suspended and resumes where you left it.
            </p>
            <Button
              autoFocus
              onClick={() => {
                noteInput();
                setIdleLeft(null);
                vnc.current?.focus();
              }}
            >
              I am still here
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
