import { expect, test } from "@playwright/test";

const jobId = "55555555-5555-4555-8555-555555555555";
const requestedUser = process.env.MOCK_USER;
const selectedUser = requestedUser === "admin" || requestedUser === "bob" ? requestedUser : "alice";
const pickerLabels = { alice: "Alice Researcher", admin: "Platform Admin", bob: "Bob Newcomer" };

test("login, jobs, CSRF-protected cancellation, and logout", async ({ page, baseURL }) => {
  let sawLogin = false;
  page.on("request", (request) => {
    if (new URL(request.url()).pathname === "/auth/login") sawLogin = true;
  });

  await page.goto("/");
  if (new URL(page.url()).pathname.endsWith("/protocol/openid-connect/auth")) {
    await page.getByRole("button", { name: pickerLabels[selectedUser] }).click();
  }
  expect(sawLogin).toBe(true);

  if (selectedUser === "bob") {
    await expect(page).toHaveURL(/\/select-tenant$/);
    await expect(page.getByText("You do not have a tenant membership yet.")).toBeVisible();
    return;
  }
  if (selectedUser === "admin") {
    await expect(page).toHaveURL(/\/select-tenant$/);
    const tenantButtons = page.getByRole("button", { name: "Open tenant" });
    await expect(tenantButtons).toHaveCount(2);
    await tenantButtons.first().click();
  }
  await expect(page).toHaveURL(/\/t\/acme$/);
  await expect(page.getByRole("heading", { name: "Acme Research" })).toBeVisible();

  await page.goto("/t/acme/jobs");
  await expect(page.getByRole("heading", { name: "Jobs" })).toBeVisible();
  const nextPage = page.getByRole("button", { name: "Next page" });
  await expect(nextPage).toBeEnabled();
  await nextPage.click();
  await expect(page).toHaveURL(/cursor=/);
  await expect(page.getByRole("link", { name: "smoke-job" })).toHaveCount(0);
  const previousPage = page.getByRole("button", { name: "Previous page" });
  await expect(previousPage).toBeEnabled();
  await previousPage.click();
  await expect(page).not.toHaveURL(/cursor=/);
  await page.goto("/t/acme/jobs");
  const jobLink = page.getByRole("link", { name: "smoke-job" });
  await expect(jobLink).toBeVisible();

  const csrfCookie = (await page.context().cookies()).find((cookie) => cookie.name === "custos_csrf");
  expect(csrfCookie?.value).toBeTruthy();
  const origin = baseURL ?? "http://localhost:3005";
  const crossOrigin = await page.request.post(`${origin}/api/bff/tenants/acme/projects/p1/jobs/${jobId}/cancel`, {
    headers: {
      Origin: "https://attacker.invalid",
      "Sec-Fetch-Site": "cross-site",
      "X-CSRF-Token": csrfCookie?.value ?? "",
      Accept: "application/json",
    },
    failOnStatusCode: false,
  });
  expect(crossOrigin.status()).toBe(403);

  await jobLink.click();
  await expect(page.getByRole("heading", { name: "smoke-job" })).toBeVisible();
  await page.getByRole("button", { name: "Cancel job" }).click();
  await page.getByRole("button", { name: "Confirm cancel" }).click();
  await expect(page.getByText("CANCELED").first()).toBeVisible();

  await page.getByRole("button", { name: "User menu" }).click();
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page.getByRole("heading", { name: "You are signed out" })).toBeVisible();
  const signInAgain = page.getByRole("button", { name: "Sign in again" });
  await expect(signInAgain).toHaveAttribute("href", "/auth/login");
  const loginNavigation = page.waitForRequest((request) => new URL(request.url()).pathname === "/auth/login");
  await signInAgain.click();
  expect((await loginNavigation).isNavigationRequest()).toBe(true);
});
