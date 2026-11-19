// API types. Keep these in step with the Go response structs
// (internal/api/httpapi). JSON keys are snake_case; timestamps are RFC 3339 UTC.

export type Role = "user" | "admin";

/** internal/api/httpapi/auth_handlers.go: userJSON */
export interface User {
  id: string;
  email: string;
  role: Role;
  email_verified: boolean;
  created_at: string;
}

/** The single error shape of every endpoint (internal/shared/httpx). */
export interface ApiErrorBody {
  error: {
    code: string;
    message: string;
    details: {
      fields?: Record<string, string>;
      retry_after_seconds?: number;
    };
  };
}
