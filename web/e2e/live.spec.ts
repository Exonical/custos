import { expect, test } from "@playwright/test";

test("sign in through e2e Keycloak and show Acme jobs", async ({ page }) => {
  test.skip(process.env.CUSTOS_E2E !== "1", "requires the live e2e stack");

  await page.goto("/");
  if (new URL(page.url()).pathname === "/auth/login") {
    await page.getByRole("button", { name: "Continue to sign in" }).click();
  }
  if (new URL(page.url()).hostname === "keycloak.e2e") {
    await page.locator("#username").fill("alice");
    await page.locator("#password").fill("alice-e2e-password");
    await page.locator("#kc-login").click();
  }
  await page.goto("/t/acme/jobs");
  await expect(page.getByRole("heading", { name: "Jobs" })).toBeVisible();
  const visibleJobs = page.locator("tbody tr a");
  await expect(visibleJobs.first()).toBeVisible({ timeout: 30_000 });
  expect(await visibleJobs.count()).toBeGreaterThan(0);
});
