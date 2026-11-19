"use client";

import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { downloadURL, joinPath, listFiles, uploadFile, type UploadHandle } from "@/lib/api/files";
import { formatBytes } from "@/lib/vm-state";
import { Button, Notice } from "../ui";

export interface UploadItem {
  id: number;
  name: string;
  size: number;
  fraction: number;
  status: "uploading" | "done" | "error";
  message?: string;
  savedAs?: string;
}

/**
 * Keeps the list of uploads of one PC. It lives in the viewer (not in the
 * panel), so uploads continue and stay visible while the panel is closed.
 */
export function useUploads(vmId: string) {
  const [items, setItems] = useState<UploadItem[]>([]);
  const handles = useRef(new Map<number, UploadHandle>());
  const nextId = useRef(1);

  const patch = (id: number, change: Partial<UploadItem>) =>
    setItems((list) => list.map((item) => (item.id === id ? { ...item, ...change } : item)));

  const add = (files: Iterable<File>) => {
    for (const file of files) {
      const id = nextId.current++;
      setItems((list) => [
        ...list,
        { id, name: file.name, size: file.size, fraction: 0, status: "uploading" },
      ]);
      const handle = uploadFile(vmId, file, (fraction) => patch(id, { fraction }));
      handles.current.set(id, handle);
      handle.done
        .then((res) => patch(id, { status: "done", fraction: 1, savedAs: res.name }))
        .catch((err: Error) => patch(id, { status: "error", message: err.message }))
        .finally(() => handles.current.delete(id));
    }
  };

  const cancel = (id: number) => handles.current.get(id)?.abort();
  const clearFinished = () =>
    setItems((list) => list.filter((item) => item.status === "uploading"));
  const active = items.some((item) => item.status === "uploading");

  // Closing the tab would silently cut off a running upload: ask first.
  useEffect(() => {
    if (!active) return;
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault();
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [active]);

  return { items, add, cancel, clearFinished, active };
}

function UploadList({ uploads }: { uploads: ReturnType<typeof useUploads> }) {
  if (uploads.items.length === 0) return null;
  return (
    <ul className="flex flex-col gap-2" aria-label="Uploads">
      {uploads.items.map((item) => (
        <li key={item.id} className="rounded-control border border-border bg-bg p-2 text-sm">
          <div className="flex items-baseline justify-between gap-3">
            <span className="truncate" title={item.name}>
              {item.savedAs ?? item.name}
            </span>
            <span className="shrink-0 text-xs text-text-muted">
              {item.status === "uploading" && `${Math.round(item.fraction * 100)}%`}
              {item.status === "done" && "Uploaded"}
              {item.status === "error" && "Failed"}
            </span>
          </div>
          {item.status === "uploading" && (
            <div className="mt-1.5 flex items-center gap-2">
              <progress
                className="h-1.5 flex-1 overflow-hidden rounded-full [&::-moz-progress-bar]:bg-accent [&::-webkit-progress-bar]:bg-border [&::-webkit-progress-value]:bg-accent"
                value={item.fraction}
                max={1}
                aria-label={`Uploading ${item.name}`}
              />
              <button
                type="button"
                className="text-xs text-text-muted hover:text-text"
                onClick={() => uploads.cancel(item.id)}
              >
                Cancel
              </button>
            </div>
          )}
          {item.status === "error" && <p className="mt-1 text-xs text-danger">{item.message}</p>}
        </li>
      ))}
    </ul>
  );
}

export function FilesPanel({
  vmId,
  uploads,
  onClose,
}: {
  vmId: string;
  uploads: ReturnType<typeof useUploads>;
  onClose: () => void;
}) {
  const [folders, setFolders] = useState<string[]>([]);
  const [downloadError, setDownloadError] = useState("");
  const picker = useRef<HTMLInputElement>(null);
  const path = joinPath(folders);

  // The listing is refreshed whenever an upload finishes.
  const finished = uploads.items.filter((item) => item.status === "done").length;
  const listing = useQuery({
    queryKey: ["files", vmId, path, finished],
    queryFn: ({ signal }) => listFiles(vmId, path, signal),
    staleTime: 0,
    retry: false,
    placeholderData: (previous) => previous,
  });

  async function download(name: string) {
    setDownloadError("");
    try {
      // A normal navigation: the browser saves the file and stays here.
      window.location.assign(await downloadURL(vmId, joinPath([...folders, name])));
    } catch (err) {
      setDownloadError(err instanceof Error ? err.message : "The download could not start.");
    }
  }

  return (
    <div
      role="dialog"
      aria-label="Files"
      className="absolute right-4 top-12 z-10 flex max-h-[calc(100%-4rem)] w-96 max-w-[calc(100%-2rem)] flex-col gap-3 overflow-hidden rounded-card border border-border bg-surface p-3 shadow-lg"
    >
      <div className="flex items-center justify-between gap-2">
        <h2 className="text-sm font-semibold">Files</h2>
        <Button variant="ghost" onClick={onClose}>
          Close
        </Button>
      </div>

      <div className="flex flex-col gap-2">
        <input
          ref={picker}
          type="file"
          multiple
          className="sr-only"
          aria-label="Choose files to upload"
          onChange={(event) => {
            if (event.target.files) uploads.add(event.target.files);
            event.target.value = "";
          }}
        />
        <div className="flex items-center justify-between gap-2">
          <Button onClick={() => picker.current?.click()}>Upload files</Button>
          {uploads.items.some((item) => item.status !== "uploading") && (
            <button
              type="button"
              className="text-xs text-text-muted hover:text-text"
              onClick={uploads.clearFinished}
            >
              Clear finished
            </button>
          )}
        </div>
        <p className="text-xs text-text-muted">
          Uploads land in the <span className="font-mono">Uploads</span> folder of your PC. You can
          also drop files onto the desktop.
        </p>
        <UploadList uploads={uploads} />
      </div>

      <div className="flex min-h-0 flex-col gap-2 border-t border-border pt-3">
        <nav aria-label="Folder" className="flex flex-wrap items-center gap-1 text-sm">
          <button
            type="button"
            className="font-medium text-accent hover:text-accent-hover"
            onClick={() => setFolders([])}
          >
            Home
          </button>
          {folders.map((folder, i) => (
            <span key={`${i}-${folder}`} className="flex items-center gap-1">
              <span aria-hidden className="text-text-muted">
                /
              </span>
              <button
                type="button"
                className="max-w-32 truncate font-medium text-accent hover:text-accent-hover"
                onClick={() => setFolders(folders.slice(0, i + 1))}
              >
                {folder}
              </button>
            </span>
          ))}
        </nav>

        {downloadError && <Notice tone="danger">{downloadError}</Notice>}
        {listing.isError ? (
          <Notice tone="danger">{listing.error.message}</Notice>
        ) : !listing.data ? (
          <p className="text-sm text-text-muted">Loading…</p>
        ) : listing.data.length === 0 ? (
          <p className="text-sm text-text-muted">This folder is empty.</p>
        ) : (
          <ul className="min-h-0 overflow-y-auto text-sm" aria-label="Files in this folder">
            {listing.data.map((entry) => (
              <li key={entry.name}>
                <button
                  type="button"
                  className="flex w-full items-baseline justify-between gap-3 rounded-control px-2 py-1.5 text-left hover:bg-surface-raised"
                  onClick={() =>
                    entry.type === "dir"
                      ? setFolders([...folders, entry.name])
                      : void download(entry.name)
                  }
                >
                  <span className="truncate">
                    {entry.name}
                    {entry.type === "dir" && "/"}
                  </span>
                  <span className="shrink-0 text-xs text-text-muted">
                    {entry.type === "dir" ? "Folder" : `${formatBytes(entry.size)} · Download`}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
