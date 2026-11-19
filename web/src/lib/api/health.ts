export type HealthState = "checking" | "healthy" | "degraded" | "unreachable";

/**
 * Turns the API's /api/health answer into one of our display states.
 * `status` is the HTTP status, `body` the parsed JSON (or null when the
 * response was not JSON, which happens when the API is not running and the
 * web server answers instead).
 */
export function interpretHealth(status: number, body: unknown): HealthState {
  if (typeof body !== "object" || body === null || !("status" in body)) {
    return "unreachable";
  }
  if (status === 200 && body.status === "ok") {
    return "healthy";
  }
  if (body.status === "degraded") {
    return "degraded";
  }
  return "unreachable";
}

export const healthLabel: Record<HealthState, string> = {
  checking: "Checking the API…",
  healthy: "API is healthy",
  degraded: "API is running, but its database or queue is down",
  unreachable: "API is not reachable",
};
