"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "./client";
import type { User } from "./types";

export const meKey = ["auth", "me"] as const;

/**
 * The logged-in user, or null when nobody is logged in.
 * `data === undefined` means "not known yet".
 */
export function useMe() {
  return useQuery({
    queryKey: meKey,
    queryFn: async ({ signal }): Promise<User | null> => {
      try {
        const res = await api<{ user: User }>("/auth/me", { signal });
        return res.user;
      } catch (err) {
        if (err instanceof ApiError && err.code === "unauthenticated") return null;
        throw err;
      }
    },
    staleTime: 60_000,
    retry: false,
  });
}

interface Credentials {
  email: string;
  password: string;
}

export function useSignup() {
  return useMutation({
    mutationFn: (body: Credentials) =>
      api<{ status: string }>("/auth/signup", { method: "POST", body }),
  });
}

export function useLogin() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: Credentials) => api<{ user: User }>("/auth/login", { method: "POST", body }),
    onSuccess: (res) => {
      // A different person may have logged in: drop everything cached.
      queryClient.clear();
      queryClient.setQueryData(meKey, res.user);
    },
  });
}

export function useLogout() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => api<{ status: string }>("/auth/logout", { method: "POST", body: {} }),
    onSuccess: () => {
      queryClient.clear();
      queryClient.setQueryData(meKey, null);
    },
  });
}

export function useVerifyEmail() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (token: string) =>
      api<{ status: string }>("/auth/verify-email", { method: "POST", body: { token } }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: meKey }),
  });
}

export function useResendVerification() {
  return useMutation({
    mutationFn: () =>
      api<{ status: string }>("/auth/resend-verification", { method: "POST", body: {} }),
  });
}

export function useForgotPassword() {
  return useMutation({
    mutationFn: (email: string) =>
      api<{ status: string }>("/auth/forgot-password", { method: "POST", body: { email } }),
  });
}

export function useResetPassword() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: { token: string; password: string }) =>
      api<{ status: string }>("/auth/reset-password", { method: "POST", body }),
    onSuccess: () => {
      queryClient.clear();
      queryClient.setQueryData(meKey, null);
    },
  });
}
