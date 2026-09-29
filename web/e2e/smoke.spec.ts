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

  if (selectedUser === "admin") {
    await page.goto("/t/acme/projects");
    await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();
    const projectSearch = page.getByRole("textbox", { name: "Filter this page by project name or slug" });
    await projectSearch.fill("Genomics");
    await expect(page).toHaveURL(/q=Genomics/);
    await expect(page.getByRole("link", { name: "Project One" })).toHaveCount(0);
    await projectSearch.fill("");
    await expect(page).not.toHaveURL(/q=/);
    await page.getByRole("link", { name: "Project One" }).click();
    await expect(page.getByRole("tab", { name: "Members" })).toBeVisible();
    await page.getByRole("tab", { name: "Cluster bindings" }).click();
    await expect(page.getByText("E2E Cluster").first()).toBeVisible();
    await page.getByRole("tab", { name: "Allocations" }).click();
    await expect(page.getByText("CPU budget")).toBeVisible();
    await expect(page.getByRole("meter")).toHaveCount(2);

    await page.goto("/t/acme/clusters");
    await expect(page.getByRole("heading", { name: "Clusters" })).toBeVisible();
    const clusterSearch = page.getByRole("textbox", { name: "Filter this page by cluster name or slug" });
    await clusterSearch.fill("hopper");
    await expect(page).toHaveURL(/q=hopper/);
    await expect(page.getByRole("link", { name: "E2E Cluster" })).toHaveCount(0);
    await clusterSearch.fill("");
    await page.getByRole("link", { name: "E2E Cluster" }).click();
    await expect(page.getByRole("tab", { name: "Partitions" })).toBeVisible();
    await expect(page.getByText("compute").first()).toBeVisible();

    await page.goto("/t/acme/secrets");
    await expect(page.getByRole("tab", { name: "Connectors" })).toBeVisible();
    await expect(page.getByText("research-vault")).toBeVisible();
    await expect(page.getByText("Platform default · OpenBao")).toBeVisible();
    await expect(page.getByRole("cell", { name: "Present" })).toBeVisible();
    await page.getByRole("tab", { name: "References" }).click();
    await expect(page.getByText("research-dataset")).toBeVisible();
    await expect(page.getByText("projects/p1/datasets · read-token")).toBeVisible();

    await page.goto("/t/acme/usage");
    await expect(page.getByRole("heading", { name: "Usage by day" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Top consumers · by user" })).toBeVisible();
    await page.getByRole("combobox", { name: "Group by" }).click();
    await page.getByRole("option", { name: "Project" }).click();
    await expect(page).toHaveURL(/group_by=project/);
    await page.getByRole("combobox", { name: "Top metric" }).click();
    await page.getByRole("option", { name: "GPU hours" }).click();
    await expect(page).toHaveURL(/metric=gpu_seconds/);
    await page.getByRole("combobox", { name: "Top by" }).click();
    await page.getByRole("option", { name: "Project" }).click();
    await expect(page).toHaveURL(/by=project/);
    await page.getByRole("button", { name: "30d" }).click();
    await expect(page).toHaveURL(/range=30d/);
    await expect(page).toHaveURL(/from=.*to=/);
    await page.goto("/t/acme/usage");
    await expect(page.getByRole("columnheader", { name: "CPU hours" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Top consumers · by user" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Tenant allocations" })).toBeVisible();
    await expect(page.getByText("CPU budget")).toBeVisible();
  }

  if (selectedUser === "alice") {
    await page.goto("/t/acme/secrets?tab=connectors");
    await expect(page.getByRole("heading", { name: "You don't have access to this view" })).toBeVisible();
  }

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
