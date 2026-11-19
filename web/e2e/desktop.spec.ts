import { expect, test, type Page } from "@playwright/test";
import { deleteAllPCs, emailedLink, resetRateLimits, uniqueEmail } from "./helpers";

test.beforeEach(() => resetRateLimits());
// page.request shares the page's login cookie.
test.afterEach(async ({ page }) => deleteAllPCs(page.request));

const viewer = (page: Page) => page.getByTestId("desktop-viewer");

/** Counts distinct colours on the remote screen: a blank canvas has one. */
async function screenColours(page: Page): Promise<number> {
  return page.evaluate(() => {
    const canvas = document.querySelector<HTMLCanvasElement>('[data-testid="vnc-screen"] canvas');
    const ctx = canvas?.getContext("2d");
    if (!canvas || !ctx || canvas.width === 0) return 0;
    const data = ctx.getImageData(0, 0, canvas.width, canvas.height).data;
    const seen = new Set<number>();
    for (let i = 0; i < data.length; i += 4 * 499) {
      seen.add((data[i] << 16) | (data[i + 1] << 8) | data[i + 2]);
    }
    return seen.size;
  });
}

test("sign up, create a PC, use its desktop, log out, come back and resume", async ({
  page,
  request,
}) => {
  test.setTimeout(150_000);
  const email = uniqueEmail("pc");
  const password = "a-good-password";

  // ---- account
  await page.goto("/signup");
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page.getByRole("heading", { name: "Check your email" })).toBeVisible();
  await page.goto(await emailedLink(request, email, "/verify-email"));
  await expect(page.getByRole("heading", { name: "Email confirmed" })).toBeVisible();
  await page.getByRole("main").getByRole("link", { name: "Log in" }).click();
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page.getByText("You have no PC yet")).toBeVisible();

  // ---- wizard
  await page.getByRole("link", { name: "Create your first PC" }).click();
  await expect(page.getByRole("radio", { name: /Ubuntu 24\.04/ })).toBeChecked();
  await page.getByRole("button", { name: "Continue" }).click();
  await page
    .getByRole("radiogroup", { name: "Memory" })
    .getByRole("radio", { name: "2 GB" })
    .click();
  await page
    .getByRole("radiogroup", { name: "Disk" })
    .getByRole("radio", { name: "40 GB" })
    .click();
  await page.getByRole("button", { name: "Continue" }).click();

  // Validation of the login step happens before anything is sent.
  await page.getByLabel("PC name").fill("Test PC");
  await page.getByLabel("Linux username").fill("root");
  await page.getByLabel("Linux password").fill("short");
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByText("This username is reserved by the system.")).toBeVisible();
  await expect(page.getByText("Use at least 8 characters.")).toBeVisible();
  await page.getByLabel("Linux username").fill("alex");
  await page.getByLabel("Linux password").fill("linux-pass-123");
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("heading", { name: "Review" })).toBeVisible();
  await page.getByRole("button", { name: "Create PC" }).click();

  // ---- the viewer shows progress, then the desktop
  await expect(page).toHaveURL(/\/vms\/[0-9a-f-]+\/desktop$/);
  await expect(viewer(page)).toContainText(/Setting up your PC|Starting your PC/);
  await expect(viewer(page)).toHaveAttribute("data-link", "connected", { timeout: 60_000 });
  // Frames arrive: the remote screen is not a single flat colour.
  await expect.poll(() => screenColours(page), { timeout: 20_000 }).toBeGreaterThan(5);

  // Input reaches the stream without breaking it.
  await page.getByTestId("vnc-screen").click({ position: { x: 300, y: 300 } });
  await page.mouse.move(420, 380);
  await page.keyboard.type("hello");
  await expect(viewer(page)).toHaveAttribute("data-link", "connected");

  // ---- back to the dashboard: the card shows the running PC
  await page.getByRole("button", { name: "Show PC controls" }).click();
  await page.getByRole("link", { name: "Your PCs" }).click();
  const card = page.getByTestId("vm-card");
  await expect(card).toContainText("Test PC");
  await expect(card).toContainText("Running");
  await expect(card).toContainText("2 GB");

  // ---- logging out says the PC will be suspended, and suspends it
  let confirmText = "";
  page.once("dialog", (dialog) => {
    confirmText = dialog.message();
    void dialog.accept();
  });
  await page.getByRole("button", { name: "Log out" }).click();
  await expect(page).toHaveURL(/\/login$/);
  expect(confirmText).toContain("will be suspended");

  // ---- come back: the PC is suspended, and resumes
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page.getByTestId("vm-card")).toContainText("Suspended", { timeout: 30_000 });
  await page.getByRole("link", { name: "Resume" }).click();
  await expect(viewer(page)).toContainText("Resuming your PC");
  await expect(viewer(page)).toHaveAttribute("data-link", "connected", { timeout: 60_000 });
  await expect.poll(() => screenColours(page), { timeout: 20_000 }).toBeGreaterThan(5);

  // ---- a second tab takes the desktop over
  const second = await page.context().newPage();
  await second.goto(page.url());
  await expect(viewer(second)).toHaveAttribute("data-link", "connected", { timeout: 60_000 });
  await expect(viewer(page)).toContainText("This PC was opened in another tab");
  await page.getByRole("button", { name: "Use here" }).click();
  await expect(viewer(page)).toHaveAttribute("data-link", "connected", { timeout: 30_000 });
  await expect(viewer(second)).toContainText("This PC was opened in another tab");
  await second.close();

  // ---- settings: rename, history, delete
  await page.getByRole("button", { name: "Show PC controls" }).click();
  await page.getByRole("link", { name: "Your PCs" }).click();
  await page.getByRole("link", { name: "Settings" }).click();
  await page.getByLabel("PC name").fill("Renamed PC");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByRole("heading", { name: "Renamed PC" })).toBeVisible();
  await expect(page.getByText("PC resumed where it was left")).toBeVisible();
  await expect(page.getByText("PC suspended because you logged out")).toBeVisible();

  const deleteButton = page.getByRole("button", { name: "Delete PC" });
  await expect(deleteButton).toBeDisabled();
  await page.getByLabel(/Type the PC's name to confirm/).fill("Renamed PC");
  await deleteButton.click();
  await expect(page).toHaveURL(/\/dashboard$/);
  await expect(page.getByText("You have no PC yet")).toBeVisible({ timeout: 30_000 });
});
