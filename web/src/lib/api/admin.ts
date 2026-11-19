"use client";

import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type { AdminUser, AdminVM, Host, UsageRow, UserLimitDefaults } from "./types";

export function useHosts() {
  return useQuery({
    queryKey: ["admin", "hosts"],
    queryFn: ({ signal }) =>
      api<{ hosts: Host[]; disk_headroom_percent: number }>("/admin/hosts", { signal }),
    // Heartbeats arrive every ten seconds; keep the page reasonably live.
    refetchInterval: 10_000,
  });
}

export function usePatchHost() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...body }: { id: string; admin_state?: Host["admin_state"] }) =>
      api<{ host: Host }>(`/admin/hosts/${id}`, { method: "PATCH", body }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["admin", "hosts"] }),
  });
}

export interface CreatedHost {
  host: Host;
  agent_token: string;
  stream_secret: string;
}

export function useCreateHost() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: { name: string; agent_url: string; public_url: string }) =>
      api<CreatedHost>("/admin/hosts", { method: "POST", body }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["admin", "hosts"] }),
  });
}

export interface VMFilters {
  state: string;
  host: string;
  email: string;
}

function query(params: Record<string, string>): string {
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) if (value) q.set(key, value);
  const s = q.toString();
  return s ? `?${s}` : "";
}

export function useAdminVMs(filters: VMFilters) {
  return useInfiniteQuery({
    queryKey: ["admin", "vms", filters],
    queryFn: ({ pageParam, signal }) =>
      api<{ vms: AdminVM[]; next_cursor: string }>(
        `/admin/vms${query({ ...filters, cursor: pageParam })}`,
        { signal },
      ),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor || undefined,
    refetchInterval: 10_000,
  });
}

export function useAdminVMAction() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, action }: { id: string; action: "suspend" | "force-stop" }) =>
      api<{ vm: AdminVM }>(`/admin/vms/${id}/${action}`, { method: "POST", body: {} }),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["admin", "vms"] }),
  });
}

export function useAdminUsers(email: string) {
  return useInfiniteQuery({
    queryKey: ["admin", "users", email],
    queryFn: ({ pageParam, signal }) =>
      api<{ users: AdminUser[]; next_cursor: string; defaults: UserLimitDefaults }>(
        `/admin/users${query({ email, cursor: pageParam })}`,
        { signal },
      ),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor || undefined,
  });
}

export type UserPatch = Partial<
  Record<"max_vms" | "max_ram_mb" | "max_vcpus" | "max_disk_gb", number | null>
> & { disabled?: boolean };

export function usePatchUser() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, patch }: { id: string; patch: UserPatch }) =>
      api<{ user: AdminUser }>(`/admin/users/${id}`, { method: "PATCH", body: patch }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["admin", "users"] }),
  });
}

export function useUsage(from: string, to: string) {
  return useQuery({
    queryKey: ["admin", "usage", from, to],
    queryFn: ({ signal }) =>
      api<{ from: string; to: string; rows: UsageRow[]; total_hours: number }>(
        `/admin/usage${query({ from, to })}`,
        { signal },
      ),
    enabled: Boolean(from && to),
  });
}

/** The address of the CSV export for the same range. */
export function usageCsvUrl(from: string, to: string): string {
  return `/api/admin/usage${query({ from, to, format: "csv" })}`;
}
