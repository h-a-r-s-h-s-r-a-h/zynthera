import { describe, expect, it } from "vitest";
import { interpretHealth } from "./health";

describe("interpretHealth", () => {
  it("reports healthy for a 200 with status ok", () => {
    expect(interpretHealth(200, { status: "ok" })).toBe("healthy");
  });

  it("reports degraded when a dependency is down", () => {
    expect(interpretHealth(503, { status: "degraded" })).toBe("degraded");
  });

  it("reports unreachable when the answer is not the API's JSON", () => {
    expect(interpretHealth(500, null)).toBe("unreachable");
    expect(interpretHealth(200, "<html>")).toBe("unreachable");
    expect(interpretHealth(200, {})).toBe("unreachable");
  });

  it("does not trust a body that says ok with a failing status", () => {
    expect(interpretHealth(502, { status: "ok" })).toBe("unreachable");
  });
});
