import type { ApiErrorBody } from "./types";

/** An error answer from the API, or a failure to reach it (code "network"). */
export class ApiError extends Error {
  readonly code: string;
  readonly status: number;
  /** Messages for individual form fields, when code is "validation_failed". */
  readonly fields: Record<string, string>;
  readonly retryAfterSeconds: number | undefined;

  constructor(
    status: number,
    code: string,
    message: string,
    fields: Record<string, string> = {},
    retryAfterSeconds?: number,
  ) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.fields = fields;
    this.retryAfterSeconds = retryAfterSeconds;
  }
}

function isErrorBody(value: unknown): value is ApiErrorBody {
  if (typeof value !== "object" || value === null || !("error" in value)) return false;
  const err = (value as { error: unknown }).error;
  return (
    typeof err === "object" &&
    err !== null &&
    typeof (err as { code?: unknown }).code === "string" &&
    typeof (err as { message?: unknown }).message === "string"
  );
}

/** Turns a non-2xx response into an ApiError. Exported for tests. */
export function toApiError(status: number, body: unknown): ApiError {
  if (isErrorBody(body)) {
    const details = body.error.details ?? {};
    return new ApiError(
      status,
      body.error.code,
      body.error.message,
      details.fields ?? {},
      details.retry_after_seconds,
    );
  }
  return new ApiError(status, "internal", "Something went wrong. Please try again.");
}

interface RequestOptions {
  method?: "GET" | "POST" | "PATCH" | "DELETE";
  body?: unknown;
  signal?: AbortSignal;
}

/**
 * Calls the API under /api and returns the parsed JSON.
 * Throws ApiError for every failure, so callers handle one error type.
 */
export async function api<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = "GET", body, signal } = options;
  let res: Response;
  try {
    res = await fetch(`/api${path}`, {
      method,
      signal,
      cache: "no-store",
      credentials: "same-origin",
      headers: body === undefined ? undefined : { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (cause) {
    if (cause instanceof DOMException && cause.name === "AbortError") throw cause;
    throw new ApiError(0, "network", "Could not reach Zynthera. Check your connection.");
  }

  const parsed: unknown = await res.json().catch(() => null);
  if (!res.ok) throw toApiError(res.status, parsed);
  return parsed as T;
}
