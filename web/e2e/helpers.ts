import { execFileSync } from "node:child_process";
import path from "node:path";
import { expect, type APIRequestContext } from "@playwright/test";

const repoRoot = path.resolve(__dirname, "..", "..");
const mailpit = "http://127.0.0.1:8025";

/** A fresh address for each test run, so runs never collide. */
export function uniqueEmail(prefix = "e2e"): string {
  return `${prefix}-${Date.now()}-${Math.floor(Math.random() * 1e6)}@example.com`;
}

/**
 * Clears the development rate-limit counters. Without this, running the
 * tests a few times in a row would hit the "5 sign-ups per hour" limit.
 */
export function resetRateLimits(): void {
  execFileSync(
    "docker",
    [
      "compose",
      "--env-file",
      ".env",
      "-f",
      "infra/docker-compose.yml",
      "exec",
      "-T",
      "redis",
      "redis-cli",
      "EVAL",
      "local n = 0 for _, k in ipairs(redis.call('KEYS', 'zyn:rl:*')) do redis.call('DEL', k) n = n + 1 end return n",
      "0",
    ],
    { cwd: repoRoot, stdio: "pipe" },
  );
}

interface MailpitSummary {
  ID: string;
}

/** Waits for the newest email to an address and returns the link inside it. */
export async function emailedLink(
  request: APIRequestContext,
  to: string,
  pathPrefix: string,
): Promise<string> {
  let link = "";
  await expect
    .poll(
      async () => {
        const search = await request.get(`${mailpit}/api/v1/search`, {
          params: { query: `to:"${to}"` },
        });
        const { messages } = (await search.json()) as { messages: MailpitSummary[] };
        for (const summary of messages) {
          const res = await request.get(`${mailpit}/api/v1/message/${summary.ID}`);
          const { Text } = (await res.json()) as { Text: string };
          const match = Text.match(new RegExp(`https?://\\S+${pathPrefix}\\?token=\\S+`));
          if (match) {
            link = match[0];
            return true;
          }
        }
        return false;
      },
      { timeout: 15_000, message: `no ${pathPrefix} email arrived for ${to}` },
    )
    .toBe(true);
  return link;
}
