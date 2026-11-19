import { expect, test, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { deleteAllPCs, resetRateLimits, verifiedAccount } from "./helpers";

test.beforeEach(() => resetRateLimits());
// page.request shares the page's login cookie.
test.afterEach(async ({ page }) => deleteAllPCs(page.request));

const viewer = (page: Page) => page.getByTestId("desktop-viewer");

async function logIn(page: Page, email: string, password: string) {
  await page.goto("/login");
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page).toHaveURL(/\/dashboard$/);
}

async function createPC(page: Page, name: string) {
  await page.getByRole("link", { name: /Create your first PC|New PC/ }).click();
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
  await page.getByLabel("PC name").fill(name);
  await page.getByLabel("Linux username").fill("alex");
  await page.getByLabel("Linux password").fill("linux-pass-123");
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByRole("button", { name: "Create PC" }).click();
  await expect(viewer(page)).toHaveAttribute("data-link", "connected", { timeout: 60_000 });
}

test("upload a file into the PC and download it again", async ({ page, request }) => {
  test.setTimeout(120_000);
  const { email, password } = await verifiedAccount(request, "files");
  await logIn(page, email, password);
  await createPC(page, "Files PC");

  await page.getByRole("button", { name: "Show PC controls" }).click();
  await page.getByRole("button", { name: /^Files/ }).click();
  const panel = page.getByRole("dialog", { name: "Files" });
  await expect(panel.getByText("This folder is empty.")).toBeVisible();

  // ---- upload two files; the same name twice must not overwrite
  const content = `hello from the test ${Date.now()}\n`.repeat(200);
  const file = { name: "notes.txt", mimeType: "text/plain", buffer: Buffer.from(content) };
  await panel.getByLabel("Choose files to upload").setInputFiles([file]);
  await expect(panel.getByRole("list", { name: "Uploads" })).toContainText("Uploaded");
  await panel.getByLabel("Choose files to upload").setInputFiles([file]);
  await expect(panel.getByRole("list", { name: "Uploads" })).toContainText("notes (1).txt");

  // ---- browse the PC's home folder and download the file back
  await panel.getByRole("list", { name: "Files in this folder" }).getByText("Uploads/").click();
  const listing = panel.getByRole("list", { name: "Files in this folder" });
  await expect(listing).toContainText("notes.txt");
  await expect(listing).toContainText("notes (1).txt");

  const downloadPromise = page.waitForEvent("download");
  await listing.getByRole("button", { name: /^notes\.txt/ }).click();
  const download = await downloadPromise;
  expect(download.suggestedFilename()).toBe("notes.txt");
  const saved = await readFile(await download.path());
  expect(saved.toString()).toBe(content);

  // The desktop is still connected after all that.
  await expect(viewer(page)).toHaveAttribute("data-link", "connected");

  // Breadcrumb leads back to the home folder.
  await panel
    .getByRole("navigation", { name: "Folder" })
    .getByRole("button", { name: "Home" })
    .click();
  await expect(listing).toContainText("Uploads/");
});

test("take a snapshot, restore it, and change the size", async ({ page, request }) => {
  test.setTimeout(150_000);
  const { email, password } = await verifiedAccount(request, "snap");
  await logIn(page, email, password);
  await createPC(page, "Snap PC");

  await page.getByRole("button", { name: "Show PC controls" }).click();
  await page.getByRole("link", { name: "Your PCs" }).click();
  await page.getByRole("link", { name: "Settings" }).click();

  // ---- a snapshot of the running PC: it pauses and comes back
  await page.getByLabel("New snapshot").fill("Before the experiment");
  await page.getByRole("button", { name: "Take snapshot" }).click();
  const snapshots = page.getByRole("list", { name: "Snapshots" });
  await expect(snapshots).toContainText("Before the experiment");
  // (The dashboard stays in the page, hidden, so only visible badges count.)
  await expect(page.locator('[data-state="running"]:visible')).toBeVisible({ timeout: 30_000 });
  await expect(snapshots).not.toContainText("Being taken", { timeout: 30_000 });

  // While the PC runs, restoring and resizing are not offered.
  await expect(snapshots.getByRole("button", { name: "Restore" })).toBeDisabled();
  await expect(page.getByRole("button", { name: "Change size" })).toBeDisabled();
  await expect(page.getByText("Shut the PC down first.")).toBeVisible();

  // ---- shut down, then restore
  await page.goto("/dashboard");
  await page.getByRole("button", { name: "Shut down" }).click();
  await expect(page.getByTestId("vm-card")).toContainText("Shut down", { timeout: 30_000 });
  await page.getByRole("link", { name: "Settings" }).click();

  let confirmText = "";
  page.once("dialog", (dialog) => {
    confirmText = dialog.message();
    void dialog.accept();
  });
  await page
    .getByRole("list", { name: "Snapshots" })
    .getByRole("button", { name: "Restore" })
    .click();
  await expect(page.getByText("Snapshot restored")).toBeVisible({ timeout: 30_000 });
  expect(confirmText).toContain("everything changed since this snapshot is lost");

  // ---- resize: more memory and a bigger disk; a smaller disk is not offered
  const disk = page.getByRole("radiogroup", { name: "Disk" });
  await page
    .getByRole("radiogroup", { name: "Memory" })
    .getByRole("radio", { name: "4 GB" })
    .click();
  await disk.getByRole("radio", { name: "80 GB" }).click();
  await page.getByRole("button", { name: "Change size" }).click();
  await expect(page.getByText(/Size changed to 4096 MB RAM, 2 vCPUs, 80 GB disk/)).toBeVisible({
    timeout: 30_000,
  });
  await expect(disk.getByRole("radio", { name: "40 GB" })).toBeDisabled();

  await page.goto("/dashboard");
  const card = page.getByTestId("vm-card");
  await expect(card).toContainText("4 GB");
  await expect(card).toContainText("80 GB");
});
