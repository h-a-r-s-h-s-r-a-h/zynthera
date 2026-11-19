import { expect, test } from "@playwright/test";
import { emailedLink, resetRateLimits, uniqueEmail } from "./helpers";

test.beforeEach(() => resetRateLimits());

test("sign up, verify by email, log in, log out", async ({ page, request }) => {
  const email = uniqueEmail();
  const password = "a-good-password";

  await page.goto("/");
  await expect(page.getByRole("status").filter({ hasText: "API is healthy" })).toBeVisible();
  await page.getByRole("link", { name: "Create your PC" }).click();

  // Client-side validation shows before anything is sent.
  await page.getByLabel("Email").fill("not-an-email");
  await page.getByLabel("Password").fill("short");
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page.getByText("Enter a valid email address.")).toBeVisible();
  await expect(page.getByText("Use at least 10 characters.")).toBeVisible();

  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page.getByRole("heading", { name: "Check your email" })).toBeVisible();

  // Unverified users can log in and are asked to confirm their email.
  await page.getByRole("link", { name: "Continue to log in" }).click();
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page).toHaveURL(/\/dashboard$/);
  await expect(page.getByText("Confirm your email to create a PC")).toBeVisible();

  const link = await emailedLink(request, email, "/verify-email");
  await page.goto(link);
  await expect(page.getByRole("heading", { name: "Email confirmed" })).toBeVisible();
  await page.getByRole("link", { name: "Go to your dashboard" }).click();
  await expect(page.getByRole("heading", { name: "Your PCs" })).toBeVisible();
  await expect(page.getByText("Confirm your email to create a PC")).toHaveCount(0);

  // A logged-in user is kept away from the auth pages.
  await page.goto("/login");
  await expect(page).toHaveURL(/\/dashboard$/);

  await page.getByRole("button", { name: "Log out" }).click();
  await expect(page).toHaveURL(/\/login$/);

  // And a logged-out visitor cannot see the dashboard.
  await page.goto("/dashboard");
  await expect(page).toHaveURL(/\/login$/);
});

test("wrong password shows one generic message", async ({ page }) => {
  await page.goto("/login");
  await page.getByLabel("Email").fill("nobody@example.com");
  await page.getByLabel("Password").fill("definitely-wrong");
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page.getByRole("main").getByRole("alert")).toHaveText("Wrong email or password.");
});

test("password reset by email", async ({ page, request }) => {
  const email = uniqueEmail("reset");
  await request.post("/api/auth/signup", {
    data: { email, password: "the-old-password" },
    headers: { Origin: "http://localhost:3000" },
  });

  await page.goto("/forgot-password");
  await page.getByLabel("Email").fill(email);
  await page.getByRole("button", { name: "Send reset link" }).click();
  await expect(page.getByRole("heading", { name: "Check your email" })).toBeVisible();

  await page.goto(await emailedLink(request, email, "/reset-password"));
  await page.getByLabel("New password").fill("a-brand-new-password");
  await page.getByRole("button", { name: "Change password" }).click();
  await expect(page.getByRole("heading", { name: "Password changed" })).toBeVisible();

  await page.getByRole("main").getByRole("link", { name: "Log in" }).click();
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill("the-old-password");
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page.getByRole("main").getByRole("alert")).toHaveText("Wrong email or password.");

  await page.getByLabel("Password").fill("a-brand-new-password");
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page).toHaveURL(/\/dashboard$/);
});

test("the seeded admin can log in", async ({ page }) => {
  await page.goto("/login");
  await page.getByLabel("Email").fill("admin@zynthera.local");
  await page.getByLabel("Password").fill("dev-only-admin-password");
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page).toHaveURL(/\/dashboard$/);
  // Administrators get a link to the admin area; normal users do not.
  await page.getByRole("link", { name: "Admin" }).click();
  await expect(page).toHaveURL(/\/admin\/hosts$/);
  await expect(page.getByRole("heading", { name: "Admin" })).toBeVisible();
});
