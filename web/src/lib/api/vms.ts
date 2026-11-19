"use client";

import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { applyVMUpdate } from "../vm-state";
import { api } from "./client";
import type { CreateVMInput, Options, OSImage, StreamToken, VM, VMEvent } from "./types";

export const vmsKey = ["vms"] as const;

/** Puts one changed PC into the cached list. Used by mutations and the event stream. */
export function putVM(queryClient: QueryClient, vm: VM): void {
  queryClient.setQueryData<VM[]>(vmsKey, (list) => applyVMUpdate(list ?? [], vm));
}

/** The user's PCs. Kept current by the event stream, so it never refetches on a timer. */
export function useVMs() {
  return useQuery({
    queryKey: vmsKey,
    queryFn: async ({ signal }) => (await api<{ vms: VM[] }>("/vms", { signal })).vms,
    staleTime: Infinity,
  });
}

/**
 * One PC, taken from the list. `vm` is undefined while loading and null when
 * the PC does not exist (or belongs to someone else).
 */
export function useVM(id: string): { vm: VM | null | undefined; error: Error | null } {
  const list = useVMs();
  if (list.isError) return { vm: undefined, error: list.error };
  if (!list.data) return { vm: undefined, error: null };
  return { vm: list.data.find((v) => v.id === id) ?? null, error: null };
}

/**
 * Keeps the cached list in step with the server through Server-Sent Events.
 * Mount it once while someone is logged in. The browser reconnects by itself
 * after a network break; on each (re)connect the server first sends the whole
 * current list, then "ready".
 */
export function useVMEventStream(enabled: boolean): void {
  const queryClient = useQueryClient();
  useEffect(() => {
    if (!enabled) return;
    const source = new EventSource("/api/events");
    // PCs received since the last (re)connect, until "ready" arrives.
    let initial: VM[] | null = [];

    const onOpen = () => {
      initial = [];
    };
    const onVM = (event: MessageEvent<string>) => {
      const vm = JSON.parse(event.data) as VM;
      if (initial) initial = applyVMUpdate(initial, vm);
      else putVM(queryClient, vm);
    };
    const onReady = () => {
      // The initial list is complete: it replaces whatever was cached, which
      // also drops PCs deleted while we were disconnected.
      if (initial) queryClient.setQueryData<VM[]>(vmsKey, initial);
      initial = null;
    };

    source.addEventListener("open", onOpen);
    source.addEventListener("vm", onVM);
    source.addEventListener("ready", onReady);
    return () => source.close();
  }, [enabled, queryClient]);
}

export function useImages() {
  return useQuery({
    queryKey: ["images"],
    queryFn: async ({ signal }) => (await api<{ images: OSImage[] }>("/images", { signal })).images,
    staleTime: 60_000,
  });
}

export function useOptions() {
  return useQuery({
    queryKey: ["options"],
    queryFn: ({ signal }) => api<Options>("/options", { signal }),
    staleTime: 0,
  });
}

export function useCreateVM() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateVMInput) => api<{ vm: VM }>("/vms", { method: "POST", body }),
    onSuccess: (res) => {
      putVM(queryClient, res.vm);
      void queryClient.invalidateQueries({ queryKey: ["options"] });
    },
  });
}

/** The action endpoints that take no body. */
export type SimpleAction =
  "start" | "suspend" | "shutdown" | "force-stop" | "reboot" | "discard-saved-state";

export function useVMAction() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, action }: { id: string; action: SimpleAction }) =>
      api<{ vm: VM }>(`/vms/${id}/${action}`, { method: "POST", body: {} }),
    onSuccess: (res) => putVM(queryClient, res.vm),
  });
}

export function useDeleteVM() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api<{ vm: VM }>(`/vms/${id}`, { method: "DELETE" }),
    onSuccess: (res) => {
      putVM(queryClient, res.vm);
      void queryClient.invalidateQueries({ queryKey: ["options"] });
    },
  });
}

export interface PatchVMInput {
  name?: string;
  keep_alive_hours?: number;
  ram_mb?: number;
  vcpus?: number;
  disk_gb?: number;
}

export function usePatchVM(id: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: PatchVMInput) => api<{ vm: VM }>(`/vms/${id}`, { method: "PATCH", body }),
    onSuccess: (res) => putVM(queryClient, res.vm),
  });
}

export function useVMHistory(id: string, refreshKey: string) {
  return useQuery({
    // The history changes whenever the PC's state does.
    queryKey: ["vm-events", id, refreshKey],
    queryFn: async ({ signal }) =>
      (await api<{ events: VMEvent[] }>(`/vms/${id}/events`, { signal })).events,
    placeholderData: (previous) => previous,
  });
}

/** A fresh single-use token for the desktop stream. Never cached. */
export function fetchStreamToken(id: string, signal?: AbortSignal): Promise<StreamToken> {
  return api<StreamToken>(`/vms/${id}/stream-token`, { method: "POST", body: {}, signal });
}
