import { expect, test, type Page } from "@playwright/test";
import { deleteAllPCs, resetRateLimits, verifiedAccount } from "./helpers";

test.beforeEach(() => resetRateLimits());

const admin = { email: "admin@zynthera.local", password: "dev-only-admin-password" };

async function logIn(page: Page, email: string, password: string) {
  await page.goto("/login");
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page).toHaveURL(/\/dashboard$/);
}

test("a normal user cannot open the admin area", async ({ page, request }) => {
  const user = await verifiedAccount(request, "notadmin");
  await logIn(page, user.email, user.password);
  await expect(page.getByRole("link", { name: "Admin", exact: true })).toHaveCount(0);
  await page.goto("/admin/vms");
  await expect(page.getByText("This area is for administrators")).toBeVisible();
  // And the API refuses, whatever the page shows.
  const res = await page.request.get("/api/admin/vms");
  expect(res.status()).toBe(403);
});

test("admin: hosts, a user's PC, limits and usage", async ({ browser, request }) => {
  test.setTimeout(150_000);
  const user = await verifiedAccount(request, "managed");

  // ---- a user creates a PC
  const userContext = await browser.newContext();
  const userPage = await userContext.newPage();
  await logIn(userPage, user.email, user.password);
  const created = await userPage.request.post("/api/vms", {
    headers: { Origin: "http://localhost:3000" },
    data: {
      name: "Managed PC",
      image: "ubuntu-24.04-xfce",
      ram_mb: 2048,
      vcpus: 1,
      disk_gb: 40,
      linux_username: "alex",
      linux_password: "linux-pass-123",
    },
  });
  expect(created.status()).toBe(202);
  await userPage.reload();
  await expect(userPage.getByTestId("vm-card")).toContainText("Running", { timeout: 60_000 });

  // ---- the admin looks at the host
  const adminContext = await browser.newContext();
  const page = await adminContext.newPage();
  await logIn(page, admin.email, admin.password);
  await page.getByRole("link", { name: "Admin", exact: true }).click();
  const host = page.getByTestId("host-card").first();
  await expect(host).toContainText("local");
  await expect(host).toContainText("Online");
  await expect(host.getByRole("meter", { name: "Memory" })).toBeVisible();
  await expect(host).toContainText("ubuntu-24.04-xfce");

  // ---- finds the user's PC and suspends it
  await page.getByRole("link", { name: "PCs", exact: true }).click();
  await page.getByLabel("Owner email contains").fill(user.email);
  await page.getByRole("button", { name: "Filter" }).click();
  const row = page.getByTestId("admin-vm-row");
  await expect(row).toHaveCount(1);
  await expect(row).toContainText("Managed PC");
  await expect(row).toContainText(user.email);
  await row.getByRole("button", { name: "Suspend" }).click();
  await expect(row).toContainText("Suspended", { timeout: 30_000 });

  // The owner sees it happen, and their history says who did it.
  await expect(userPage.getByTestId("vm-card")).toContainText("Suspended", { timeout: 30_000 });
  await userPage.getByRole("link", { name: "Settings" }).click();
  await expect(userPage.getByText("PC suspended by an administrator")).toBeVisible();

  // ---- sets a limit for the user
  await page.getByRole("link", { name: "Users" }).click();
  await page.getByLabel("Email contains", { exact: true }).fill(user.email);
  await page.getByRole("button", { name: "Search" }).click();
  const userRow = page.getByTestId("admin-user-row");
  await expect(userRow).toHaveCount(1);
  await expect(userRow).toContainText("Defaults");
  await userRow.getByRole("button", { name: "Limits" }).click();
  await page.getByLabel("RAM (MB)").fill("lots");
  await page.getByRole("button", { name: "Save limits" }).click();
  await expect(page.getByText("Whole number, or empty for the default.")).toBeVisible();
  await page.getByLabel("RAM (MB)").fill("8192");
  await page.getByRole("button", { name: "Save limits" }).click();
  await expect(userRow).toContainText("RAM (MB): 8192");

  // ---- usage shows the hours the PC ran
  await page.getByRole("link", { name: "Usage" }).click();
  await expect(page.getByRole("row").filter({ hasText: user.email })).toContainText("2 GB");
  const downloadPromise = page.waitForEvent("download");
  await page.getByRole("link", { name: "Download CSV" }).click();
  const download = await downloadPromise;
  expect(download.suggestedFilename()).toMatch(/^zynthera-usage-.*\.csv$/);

  // ---- disabling the user logs them out at once
  await page.getByRole("link", { name: "Users" }).click();
  await page.getByLabel("Email contains", { exact: true }).fill(user.email);
  await page.getByRole("button", { name: "Search" }).click();
  page.once("dialog", (dialog) => void dialog.accept());
  await page.getByTestId("admin-user-row").getByRole("button", { name: "Disable" }).click();
  await expect(page.getByTestId("admin-user-row")).toContainText("Disabled");
  expect((await userPage.request.get("/api/auth/me")).status()).toBe(401);

  // ---- clean up: enable again so the PC can be deleted by its owner
  await page.getByTestId("admin-user-row").getByRole("button", { name: "Enable" }).click();
  await expect(page.getByTestId("admin-user-row")).toContainText("Active");
  await logIn(userPage, user.email, user.password);
  await deleteAllPCs(userPage.request);
  await userContext.close();
  await adminContext.close();
});
