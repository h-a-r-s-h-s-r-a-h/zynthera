import { describe, expect, it } from "vitest";
import { toApiError } from "./client";

describe("toApiError", () => {
  it("reads the error envelope", () => {
    const err = toApiError(422, {
      error: {
        code: "validation_failed",
        message: "Please check the highlighted fields.",
        details: { fields: { email: "Enter a valid email address." } },
      },
    });
    expect(err.code).toBe("validation_failed");
    expect(err.status).toBe(422);
    expect(err.message).toBe("Please check the highlighted fields.");
    expect(err.fields).toEqual({ email: "Enter a valid email address." });
  });

  it("reads the retry time of a rate limit", () => {
    const err = toApiError(429, {
      error: {
        code: "rate_limited",
        message: "Too many attempts.",
        details: { retry_after_seconds: 42 },
      },
    });
    expect(err.retryAfterSeconds).toBe(42);
    expect(err.fields).toEqual({});
  });

  it("falls back to a generic error for anything else", () => {
    for (const body of [null, "<html>", {}, { error: "nope" }, { error: { code: 1 } }]) {
      const err = toApiError(502, body);
      expect(err.code).toBe("internal");
      expect(err.status).toBe(502);
    }
  });
});
