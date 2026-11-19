"use client";

import type RFB from "@novnc/novnc";
import { useEffect, useImperativeHandle, useRef, type Ref } from "react";
import { fetchStreamToken } from "@/lib/api/vms";

/** Why a stream ended, as far as the viewer cares. */
export type StreamEnd =
  | "replaced" // 4409: the PC was opened in another tab
  | "not-running" // 4404: the PC stopped running
  | "bad-token" // 4401: the token was refused; a fresh one may work
  | "dropped" // the connection broke
  | "failed"; // it could not even be set up

/** What the toolbar can ask of an open session. */
export interface VncHandle {
  sendCtrlAltDel(): void;
  sendClipboard(text: string): void;
  focus(): void;
  disconnect(): void;
}

function endFromCloseCode(code: number): StreamEnd {
  if (code === 4409) return "replaced";
  if (code === 4404) return "not-running";
  if (code === 4401 || code === 4403) return "bad-token";
  return "dropped";
}

/**
 * One connection attempt to a PC's desktop. Mounting it fetches a fresh
 * single-use token and connects; unmounting disconnects. To reconnect, the
 * parent mounts a new one (by changing its `key`).
 */
export function VncSession({
  vmId,
  handle,
  onConnected,
  onEnded,
  onClipboard,
}: {
  vmId: string;
  handle: Ref<VncHandle>;
  onConnected: () => void;
  onEnded: (reason: StreamEnd) => void;
  onClipboard: (text: string) => void;
}) {
  const target = useRef<HTMLDivElement>(null);
  const rfbRef = useRef<RFB | null>(null);

  // The callbacks may change identity on every render of the parent; the
  // connection must not be torn down because of that.
  const callbacks = useRef({ onConnected, onEnded, onClipboard });
  useEffect(() => {
    callbacks.current = { onConnected, onEnded, onClipboard };
  });

  useImperativeHandle(
    handle,
    () => ({
      sendCtrlAltDel: () => rfbRef.current?.sendCtrlAltDel(),
      sendClipboard: (text) => rfbRef.current?.clipboardPasteFrom(text),
      focus: () => rfbRef.current?.focus(),
      disconnect: () => rfbRef.current?.disconnect(),
    }),
    [],
  );

  useEffect(() => {
    const abort = new AbortController();
    let socket: WebSocket | null = null;
    let rfb: RFB | null = null;
    let ended = false;
    const end = (reason: StreamEnd) => {
      if (ended || abort.signal.aborted) return;
      ended = true;
      callbacks.current.onEnded(reason);
    };

    void (async () => {
      try {
        // noVNC touches `window` when it loads, so it is only ever imported
        // in the browser, and only when a desktop is actually opened.
        const [{ default: RFBClass }, stream] = await Promise.all([
          import("@novnc/novnc"),
          fetchStreamToken(vmId, abort.signal),
        ]);
        if (abort.signal.aborted || !target.current) return;

        // We open the socket ourselves (instead of handing noVNC the URL)
        // to see the close code, which says why a stream ended.
        socket = new WebSocket(`${stream.url}?token=${encodeURIComponent(stream.token)}`, [
          "binary",
        ]);
        socket.addEventListener("close", (event) => end(endFromCloseCode(event.code)));

        rfb = new RFBClass(target.current, socket);
        rfbRef.current = rfb;
        // Fit the remote screen into the window, and ask the PC to change
        // its resolution to match the window where the display supports it.
        rfb.scaleViewport = true;
        rfb.resizeSession = true;
        rfb.focusOnClick = true;
        rfb.background = "transparent";
        rfb.addEventListener("connect", () => {
          if (abort.signal.aborted) return;
          callbacks.current.onConnected();
          rfb?.focus();
        });
        rfb.addEventListener("clipboard", (event) =>
          callbacks.current.onClipboard(event.detail.text),
        );
        rfb.addEventListener("securityfailure", () => end("failed"));
      } catch (err) {
        if (abort.signal.aborted) return;
        // A PC that stopped between the state update and the token request
        // answers 409; the state stream will show what happened.
        end(
          err instanceof Error && "code" in err && err.code === "vm_invalid_state"
            ? "not-running"
            : "failed",
        );
      }
    })();

    return () => {
      abort.abort();
      rfbRef.current = null;
      try {
        rfb?.disconnect();
      } catch {
        // Already disconnected.
      }
      socket?.close();
    };
  }, [vmId]);

  return <div ref={target} data-testid="vnc-screen" className="absolute inset-0" />;
}
