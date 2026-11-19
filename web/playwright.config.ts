import { defineConfig, devices } from "@playwright/test";

// End-to-end tests run against the real stack in mock mode.
// Start `make up` first; this config starts `make dev` if it is not running.
export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [["list"]],
  timeout: 60_000,
  use: {
    baseURL: "http://localhost:3000",
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: {
    command: "make -C .. dev",
    // 127.0.0.1, not localhost: the dev server listens on IPv4 only and Node may
    // try IPv6 first, which would make an already running server look absent.
    url: "http://127.0.0.1:3000/api/health",
    reuseExistingServer: true,
    timeout: 120_000,
  },
});
