"use client";

import { api, ApiError, toApiError } from "./client";
import type { FileEntry, FilesToken } from "./types";

// File transfer talks to the PC's host directly (not through /api), with a
// short-lived token from the API. The token can be reused until it expires.

const cache = new Map<string, FilesToken>();

/** A valid files token for the PC, fetched or renewed as needed. */
export async function filesAccess(vmId: string): Promise<FilesToken> {
  const cached = cache.get(vmId);
  // Renew a little early, so a token never expires in the middle of a request.
  if (cached && new Date(cached.expires_at).getTime() - Date.now() > 30_000) return cached;
  const fresh = await api<FilesToken>(`/vms/${vmId}/files-token`, { method: "POST", body: {} });
  cache.set(vmId, fresh);
  return fresh;
}

export function forgetFilesAccess(vmId: string): void {
  cache.delete(vmId);
}

/** Joins folder names into the path the file bridge expects. */
export function joinPath(parts: readonly string[]): string {
  return parts.join("/");
}

export async function listFiles(vmId: string, path: string, signal?: AbortSignal) {
  const access = await filesAccess(vmId);
  let res: Response;
  try {
    res = await fetch(`${access.base_url}/list?path=${encodeURIComponent(path)}`, {
      headers: { Authorization: `Bearer ${access.token}` },
      cache: "no-store",
      signal,
    });
  } catch (cause) {
    if (cause instanceof DOMException && cause.name === "AbortError") throw cause;
    throw new ApiError(0, "network", "Could not reach your PC's files. Check your connection.");
  }
  const body: unknown = await res.json().catch(() => null);
  if (!res.ok) {
    if (res.status === 401) forgetFilesAccess(vmId);
    throw toApiError(res.status, body);
  }
  return (body as { entries: FileEntry[] }).entries;
}

/** The address a browser can open to download a file from the PC. */
export async function downloadURL(vmId: string, path: string): Promise<string> {
  const access = await filesAccess(vmId);
  return `${access.base_url}/download?path=${encodeURIComponent(path)}&token=${encodeURIComponent(access.token)}`;
}

export interface UploadHandle {
  done: Promise<{ name: string; size: number }>;
  abort(): void;
}

/**
 * Uploads one file into the PC's ~/Uploads folder. XMLHttpRequest is used
 * because it reports upload progress, which fetch does not.
 */
export function uploadFile(
  vmId: string,
  file: File,
  onProgress: (fraction: number) => void,
): UploadHandle {
  const xhr = new XMLHttpRequest();
  let aborted = false;

  const done = (async () => {
    const access = await filesAccess(vmId);
    if (file.size > access.max_upload_mb * 1024 * 1024) {
      throw new ApiError(413, "too_large", `Files can be at most ${access.max_upload_mb} MB.`);
    }
    if (aborted) throw new ApiError(0, "aborted", "Upload cancelled.");

    return new Promise<{ name: string; size: number }>((resolve, reject) => {
      xhr.open("PUT", `${access.base_url}/upload?name=${encodeURIComponent(file.name)}`);
      xhr.setRequestHeader("Authorization", `Bearer ${access.token}`);
      xhr.responseType = "json";
      xhr.upload.onprogress = (event) => {
        if (event.lengthComputable) onProgress(event.loaded / event.total);
      };
      xhr.onload = () => {
        if (xhr.status >= 200 && xhr.status < 300) {
          onProgress(1);
          resolve(xhr.response as { name: string; size: number });
          return;
        }
        if (xhr.status === 401) forgetFilesAccess(vmId);
        reject(toApiError(xhr.status, xhr.response));
      };
      xhr.onerror = () =>
        reject(new ApiError(0, "network", "The upload was interrupted. Check your connection."));
      xhr.onabort = () => reject(new ApiError(0, "aborted", "Upload cancelled."));
      xhr.send(file);
    });
  })();

  return {
    done,
    abort: () => {
      aborted = true;
      xhr.abort();
    },
  };
}
