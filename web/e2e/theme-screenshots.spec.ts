import { mkdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { expect, test, type Locator, type Page } from "@playwright/test";

const outputDirectory = join(process.env.TEMP ?? tmpdir(), "custos-theme-shots");

function expectInsideViewport(bounds: { x: number; y: number; width: number; height: number } | null, width: number, height: number) {
  expect(bounds).not.toBeNull();
  if (bounds) {
    expect(bounds.x).toBeGreaterThanOrEqual(0);
    expect(bounds.y).toBeGreaterThanOrEqual(0);
    expect(bounds.x + bounds.width).toBeLessThanOrEqual(width);
    expect(bounds.y + bounds.height).toBeLessThanOrEqual(height);
  }
}

async function expectCompactMonoControl(locator: Locator) {
  const fontVariable = await locator.page().locator("html").evaluate((element) => getComputedStyle(element).getPropertyValue("--font-plex-mono").trim());
  const monoFamily = fontVariable.split(",")[0].trim().replace(/[\"']/g, "");
  await expect.poll(() => locator.evaluate((element) => getComputedStyle(element).fontFamily), { timeout: 5_000 }).toContain(monoFamily);
  await expect(locator).toHaveCSS("font-size", "11px");
  await expect(locator).toHaveCSS("text-transform", "uppercase");
  await expect(locator).toHaveCSS("height", "32px");
}

async function expectSelectedTab(locator: Locator, inactiveLocator?: Locator) {
  await expect(locator).toHaveAttribute("aria-selected", "true");
  if (inactiveLocator) {
    await expect(inactiveLocator).toHaveAttribute("aria-selected", "false");
    const activeStyle = await locator.page().evaluate(() => {
      const probe = document.createElement("button");
      probe.setAttribute("aria-selected", "true");
      probe.className = "font-mono text-[11px] uppercase text-muted-foreground border-b-2 border-transparent aria-selected:border-primary aria-selected:text-primary";
      document.body.append(probe);
      const style = getComputedStyle(probe);
      const result = { color: style.color, borderColor: style.borderBottomColor };
      probe.remove();
      return result;
    });
    await expect.poll(() => locator.evaluate((element) => {
      const style = getComputedStyle(element);
      return { color: style.color, borderColor: style.borderBottomColor };
    }), { timeout: 5_000 }).toEqual(activeStyle);
    await expect.poll(async () => {
      const [selectedColor, inactiveColor] = await Promise.all([
        locator.evaluate((element) => getComputedStyle(element).color),
        inactiveLocator.evaluate((element) => getComputedStyle(element).color),
      ]);
      return selectedColor !== inactiveColor;
    }, { timeout: 5_000 }).toBe(true);
    await expect(locator).toHaveCSS("border-bottom-width", "2px");
  }
}

async function expectNoHorizontalOverflow(page: Page, width: number) {
  const documentWidth = await page.evaluate(() => document.documentElement.scrollWidth);
  expect(documentWidth).toBeLessThanOrEqual(width);
}

async function expectTableScrollContainer(page: Page) {
  const overflowX = await page.locator('[data-slot="table-container"]').first().evaluate((element) => getComputedStyle(element).overflowX);
  expect(overflowX).toBe("auto");
}

async function capture(page: Page, name: string, consoleIssues: string[]) {
  await page.screenshot({ path: join(outputDirectory, name), fullPage: false });
  await expect(page.locator("nextjs-portal").getByText(/^\d+$/)).toHaveCount(0);
  expect(consoleIssues, `browser console errors or warnings on ${new URL(page.url()).pathname}`).toEqual([]);
}

test("capture dark industrial console screenshots", async ({ page }) => {
  await rm(outputDirectory, { recursive: true, force: true });
  await mkdir(outputDirectory, { recursive: true });
  await page.setViewportSize({ width: 1440, height: 900 });

  const cspViolations: string[] = [];
  const consoleIssues: string[] = [];
  const externalRequests: string[] = [];
  const externalFontRequests: string[] = [];
  const localFontRequests: string[] = [];
  const workerUrls: string[] = [];
  const documentCspHeaders: string[] = [];
  const localHosts = new Set(["localhost", "127.0.0.1", "::1"]);
  page.on("console", (message) => {
    const type = message.type();
    if (type === "error" || type === "warning") {
      consoleIssues.push(`${new URL(page.url()).pathname} [${type}]: ${message.text()}`);
    }
    if ((type === "error" || type === "warning") && /content security policy|violates the following directive/i.test(message.text())) {
      cspViolations.push(message.text());
    }
  });
  page.on("pageerror", (error) => consoleIssues.push(`${new URL(page.url()).pathname} [pageerror]: ${error.message}`));
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (["http:", "https:", "ws:", "wss:"].includes(url.protocol) && !localHosts.has(url.hostname)) externalRequests.push(request.url());
    if (url.protocol === "blob:" || url.protocol === "data:") externalRequests.push(request.url());
    if (url.hostname === "fonts.googleapis.com" || url.hostname.endsWith(".gstatic.com")) externalFontRequests.push(request.url());
    if (url.pathname.endsWith(".woff2")) localFontRequests.push(request.url());
  });
  page.on("worker", (worker) => workerUrls.push(worker.url()));
  page.on("response", (response) => {
    if (response.request().resourceType() === "document") {
      void response.headerValue("content-security-policy").then((value) => { if (value) documentCspHeaders.push(value); });
    }
  });

  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Choose a mock user" })).toBeVisible();
  await capture(page, "01-mock-picker.png", consoleIssues);

  await page.getByRole("button", { name: "Platform Admin" }).click();
  await expect(page).toHaveURL(/\/select-tenant$/);
  await page.evaluate(async () => { await document.fonts.ready; });
  const tenantButtons = page.getByRole("button", { name: "Open tenant" });
  await expect(tenantButtons).toHaveCount(2);
  await capture(page, "02-select-tenant-admin.png", consoleIssues);
  await tenantButtons.first().click();
  await expect(page).toHaveURL(/\/t\/acme$/);
  await expect(page.getByRole("heading", { name: "Acme Research" })).toBeVisible();
  const dashboardBreadcrumb = page.getByRole("navigation", { name: "breadcrumb" });
  await expect(dashboardBreadcrumb).toContainText("Dashboard");
  await expect(dashboardBreadcrumb).not.toContainText("acme");
  await capture(page, "03-dashboard-acme.png", consoleIssues);

  await page.getByRole("button", { name: "Switch tenant" }).click();
  const tenantOption = page.getByRole("menuitemradio", { name: /Globex HPC/ });
  await expect(tenantOption).toBeVisible();
  expectInsideViewport(await page.getByRole("menu").boundingBox(), 1440, 900);
  await capture(page, "08-tenant-switcher-open.png", consoleIssues);
  await page.keyboard.press("Escape");

  await page.goto("/t/acme/jobs");
  await expect(page.getByRole("heading", { name: "Jobs" })).toBeVisible();
  const jobsBreadcrumb = page.getByRole("navigation", { name: "breadcrumb" });
  await expect(jobsBreadcrumb).toContainText("Jobs");
  await expect(jobsBreadcrumb).not.toContainText("acme");
  await expectCompactMonoControl(page.getByRole("combobox", { name: "Project" }));
  await expectCompactMonoControl(page.getByRole("combobox", { name: "State" }));
  await expectCompactMonoControl(page.getByRole("textbox", { name: "Filter this page by job name" }));
  await expectCompactMonoControl(page.getByRole("button", { name: "Refresh" }));
  await page.getByRole("combobox", { name: "Project" }).click();
  await expect(page.getByRole("option", { name: "Genomics" })).toBeVisible();
  expectInsideViewport(await page.getByRole("listbox").boundingBox(), 1440, 900);
  await page.keyboard.press("Escape");
  await capture(page, "04-jobs.png", consoleIssues);

  await page.getByRole("textbox", { name: "Filter this page by job name" }).fill("no-such-job");
  await expect(page.getByText("No data available.")).toBeVisible();
  await capture(page, "05-jobs-empty.png", consoleIssues);

  await page.goto("/t/acme/jobs");
  const smokeJob = page.getByRole("link", { name: "smoke-job" });
  await expect(smokeJob).toBeVisible();
  await smokeJob.click();
  await expect(page.getByRole("heading", { name: "smoke-job" })).toBeVisible();
  await expect(page.getByRole("navigation", { name: "breadcrumb" })).toContainText("smoke-job");
  const detailsTab = page.getByRole("tab", { name: "Details" });
  await expect(detailsTab).toBeVisible();
  await expectCompactMonoControl(detailsTab);
  await capture(page, "06-job-detail.png", consoleIssues);

  const cancelButton = page.getByRole("button", { name: "Cancel job" });
  await expectCompactMonoControl(cancelButton);
  await cancelButton.click();
  await expect(page.getByRole("alertdialog")).toBeVisible();
  const dialogBounds = await page.getByRole("alertdialog").boundingBox();
  expect(dialogBounds).not.toBeNull();
  if (dialogBounds) expect(Math.abs(dialogBounds.y + dialogBounds.height / 2 - 450)).toBeLessThan(32);
  const keepJobButton = page.getByRole("button", { name: "Keep job" });
  const confirmCancelButton = page.getByRole("button", { name: "Confirm cancel" });
  await expect(keepJobButton).toBeVisible();
  await expectCompactMonoControl(keepJobButton);
  await expectCompactMonoControl(confirmCancelButton);
  await capture(page, "07-cancel-dialog-open.png", consoleIssues);
  await keepJobButton.click();

  await page.getByRole("button", { name: "User menu" }).click();
  await expect(page.getByRole("menu")).toBeVisible();
  expectInsideViewport(await page.getByRole("menu").boundingBox(), 1440, 900);
  await capture(page, "09-user-menu-open.png", consoleIssues);
  await page.keyboard.press("Escape");

  await page.goto("/t/acme/jobs?mock_fail=500");
  await expect(page.getByRole("alert")).toBeVisible();
  await capture(page, "10-api-error.png", consoleIssues);

  await page.goto("/t/acme/projects");
  await expect(page.getByRole("columnheader", { name: "Slug" })).toBeVisible();
  await expectCompactMonoControl(page.getByRole("textbox", { name: "Filter this page by project name or slug" }));
  await expectCompactMonoControl(page.getByRole("button", { name: "Refresh" }));
  await capture(page, "14-projects.png", consoleIssues);
  await page.getByRole("link", { name: "Project One" }).click();
  await expect(page.getByRole("heading", { name: "Project One" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Jobs" })).toHaveAttribute("href", /\/t\/acme\/jobs\?project=p1/);
  await expectCompactMonoControl(page.getByRole("button", { name: "Jobs" }));
  await expectCompactMonoControl(page.getByRole("button", { name: "Refresh" }));
  const membersTab = page.getByRole("tab", { name: "Members" });
  const bindingTab = page.getByRole("tab", { name: "Cluster bindings" });
  await membersTab.click();
  await page.mouse.move(1435, 890);
  await expectSelectedTab(membersTab, bindingTab);
  await expectCompactMonoControl(membersTab);
  await capture(page, "15-project-detail-members.png", consoleIssues);
  await bindingTab.click();
  await page.mouse.move(1435, 890);
  await expectSelectedTab(bindingTab, membersTab);
  await expectCompactMonoControl(bindingTab);
  await expect(page.getByText("E2E Cluster")).toBeVisible();
  await capture(page, "16-project-detail-cluster-bindings.png", consoleIssues);
  const projectAllocationsTab = page.getByRole("tab", { name: "Allocations" });
  await projectAllocationsTab.click();
  await page.mouse.move(1435, 890);
  await expectSelectedTab(projectAllocationsTab, bindingTab);
  await expect(membersTab).toHaveAttribute("aria-selected", "false");
  await expectCompactMonoControl(projectAllocationsTab);
  await expect(page.getByText("CPU budget")).toBeVisible();
  await expect(page.getByRole("meter").nth(0)).toHaveAttribute("aria-valuenow", "85.1");
  await expect(page.getByRole("meter").nth(0).locator("div")).toHaveClass(/bg-status-queued/);
  await expect(page.getByRole("meter").nth(1)).toHaveAttribute("aria-valuenow", "100");
  await expect(page.getByRole("meter").nth(1).locator("div")).toHaveClass(/bg-destructive/);
  await capture(page, "17-project-detail-allocations.png", consoleIssues);

  await page.goto("/t/acme/clusters");
  await expect(page.getByRole("columnheader", { name: "Slurm version" })).toBeVisible();
  await expectCompactMonoControl(page.getByRole("textbox", { name: "Filter this page by cluster name or slug" }));
  await expectCompactMonoControl(page.getByRole("button", { name: "Refresh" }));
  await capture(page, "18-clusters.png", consoleIssues);
  await page.getByRole("link", { name: "E2E Cluster" }).click();
  await expect(page.getByRole("heading", { name: "E2E Cluster" })).toBeVisible();
  await expectCompactMonoControl(page.getByRole("tab", { name: "Partitions" }));
  await capture(page, "19-cluster-detail-partitions.png", consoleIssues);

  await page.goto("/t/acme/secrets");
  const connectorsTab = page.getByRole("tab", { name: "Connectors" });
  const referencesTab = page.getByRole("tab", { name: "References" });
  await expect(connectorsTab).toBeVisible();
  await expectSelectedTab(connectorsTab, referencesTab);
  await expectCompactMonoControl(connectorsTab);
  await expect(page.getByText("research-vault")).toBeVisible();
  await expect(page.getByText("Platform default · OpenBao")).toBeVisible();
  await expect(page.getByRole("cell", { name: "Present" })).toBeVisible();
  await expect(page.getByText(/credential_ref|secret_id/i)).toHaveCount(0);
  await capture(page, "20-secrets-connectors.png", consoleIssues);
  await referencesTab.click();
  await page.mouse.move(1435, 890);
  await expectSelectedTab(referencesTab, connectorsTab);
  await expectCompactMonoControl(referencesTab);
  await expect(page.getByText("research-dataset")).toBeVisible();
  await expect(page.getByText("projects/p1/datasets · read-token")).toBeVisible();
  await expect(page.getByText(/credential_ref|secret_id/i)).toHaveCount(0);
  await capture(page, "21-secrets-references.png", consoleIssues);

  await page.goto("/t/acme/usage");
  await expect(page.getByRole("heading", { name: "Usage by day" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Top consumers · by user" })).toBeVisible();
  await expectCompactMonoControl(page.getByRole("combobox", { name: "Group by" }));
  await expectCompactMonoControl(page.getByRole("combobox", { name: "Top metric" }));
  await expectCompactMonoControl(page.getByRole("combobox", { name: "Top by" }));
  await expectCompactMonoControl(page.getByRole("button", { name: "7d" }));
  await expect(page.getByRole("columnheader", { name: "CPU hours" })).toBeVisible();
  await expect(page.getByText("No allocations")).toHaveCount(0);
  await page.mouse.move(1435, 890);
  await capture(page, "22-usage.png", consoleIssues);
  await page.getByRole("combobox", { name: "Top by" }).click();
  await page.getByRole("option", { name: "Project" }).click();
  await expect(page).toHaveURL(/by=project/);
  await expect(page.getByRole("heading", { name: "Top consumers · by project" })).toBeVisible();
  await page.mouse.move(1435, 890);
  await capture(page, "26-usage-top-by-project.png", consoleIssues);

  await page.goto("/t/acme/workflows");
  await expect(page.getByRole("heading", { name: "Workflows" })).toBeVisible();
  await expectCompactMonoControl(page.getByRole("combobox", { name: "Project" }));
  await expectCompactMonoControl(page.getByRole("button", { name: "Show archived" }));
  await expect(page.getByRole("link", { name: "Gaussian simulation" })).toBeVisible();
  await capture(page, "27-workflows.png", consoleIssues);
  const workflowLink = page.getByRole("link", { name: "Gaussian simulation" });
  const workflowHref = await workflowLink.getAttribute("href");
  expect(workflowHref).not.toBeNull();
  await workflowLink.click();
  await expect(page.getByRole("heading", { name: "Gaussian simulation" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Run" })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Versions" })).toBeVisible();
  await capture(page, "28-workflow-detail-versions.png", consoleIssues);
  await page.getByRole("tab", { name: "Executions" }).click();
  await expect(page.getByRole("columnheader", { name: "State" })).toBeVisible();
  await capture(page, "29-workflow-detail-executions.png", consoleIssues);
  await page.getByRole("tab", { name: "Versions" }).click();
  const publishedVersionLink = page.getByRole("link", { name: "v2", exact: true });
  const publishedVersionHref = await publishedVersionLink.getAttribute("href");
  expect(publishedVersionHref).not.toBeNull();
  await publishedVersionLink.click();
  await expect(page.getByRole("heading", { name: "Version 2" })).toBeVisible();
  for (const nodeName of ["prepare", "simulate", "merge", "train", "sweep"]) {
    await expect(page.getByText(nodeName, { exact: true }).first()).toBeVisible();
  }
  await capture(page, "30-workflow-version-graph.png", consoleIssues);
  const yamlWorkerEvent = page.waitForEvent("worker", { timeout: 15_000 });
  await page.getByRole("tab", { name: "YAML" }).click();
  const yamlWorker = await yamlWorkerEvent;
  expect(yamlWorker.url()).toMatch(/^https?:\/\//);
  await expect(page.locator(".monaco-editor .view-lines")).toContainText("apiVersion: custos.io/v1alpha1");
  await expect.poll(() => documentCspHeaders.some((header) => header.includes("worker-src 'self'"))).toBe(true);
  await capture(page, "31-workflow-version-yaml.png", consoleIssues);
  await page.getByRole("tab", { name: "Parameters" }).click();
  await expect(page.getByRole("columnheader", { name: "Constraints" })).toBeVisible();
  await capture(page, "32-workflow-version-parameters.png", consoleIssues);
  await page.goto(workflowHref ?? "/t/acme/workflows");
  await page.getByRole("button", { name: "Run" }).click();
  await expect(page.getByRole("dialog", { name: "Run Gaussian simulation" })).toBeVisible();
  await expectCompactMonoControl(page.getByLabel(/molecule/));
  await capture(page, "33-workflow-run-dialog.png", consoleIssues);
  await page.keyboard.press("Escape");

  await page.goto("/t/acme/executions");
  await expect(page.getByRole("heading", { name: "Executions" })).toBeVisible();
  await expectCompactMonoControl(page.getByRole("combobox", { name: "Workflow" }));
  await expectCompactMonoControl(page.getByRole("combobox", { name: "State" }));
  await capture(page, "34-executions.png", consoleIssues);
  const runningExecutionRow = page.getByRole("row").filter({ hasText: "Gaussian simulation" }).filter({ hasText: "RUNNING" }).first();
  await expect(runningExecutionRow).toBeVisible();
  const runningExecutionHref = await runningExecutionRow.getByRole("link").first().getAttribute("href");
  expect(runningExecutionHref).not.toBeNull();
  await runningExecutionRow.getByRole("link").first().click();
  await expect(page.getByText("2/4 COMPLETED")).toBeVisible();
  await expect(page.getByRole("columnheader", { name: "Index / count" })).toBeVisible();
  await capture(page, "35-execution-detail-running.png", consoleIssues);
  await page.getByText("simulate", { exact: true }).first().click();
  await expect(page.getByRole("heading", { name: "Task executions · simulate" })).toBeVisible();
  await capture(page, "36-execution-task-filter.png", consoleIssues);
  await page.getByRole("button", { name: "Inspect" }).first().click();
  await expect(page.getByRole("region", { name: "Frozen execution specification" })).toBeVisible();
  await expect(page.locator("pre").last()).toContainText("task_name");
  await capture(page, "36-execution-task-spec.png", consoleIssues);

  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/t/acme/workflows");
  await expectNoHorizontalOverflow(page, 390);
  await expectTableScrollContainer(page);
  await capture(page, "37-workflows-mobile-390x844.png", consoleIssues);
  await page.goto(publishedVersionHref ?? "/t/acme/workflows");
  await expectNoHorizontalOverflow(page, 390);
  await capture(page, "38-workflow-version-mobile-390x844.png", consoleIssues);
  await page.goto("/t/acme/executions");
  await page.getByRole("row").filter({ hasText: "Gaussian simulation" }).filter({ hasText: "RUNNING" }).first().getByRole("link").first().click();
  await expectNoHorizontalOverflow(page, 390);
  await expectTableScrollContainer(page);
  await capture(page, "39-execution-detail-mobile-390x844.png", consoleIssues);

  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/t/acme/jobs");
  await expect(page.getByRole("heading", { name: "Jobs" })).toBeVisible();
  await expectCompactMonoControl(page.getByRole("combobox", { name: "Project" }));
  await expectCompactMonoControl(page.getByRole("combobox", { name: "State" }));
  await expectCompactMonoControl(page.getByRole("textbox", { name: "Filter this page by job name" }));
  await expectCompactMonoControl(page.getByRole("button", { name: "Refresh" }));
  await capture(page, "11-jobs-mobile-390x844.png", consoleIssues);

  await page.goto("/t/acme");
  await page.getByRole("button", { name: "Open navigation menu" }).click();
  const mobileDrawer = page.getByRole("dialog", { name: "Navigation" });
  await expect(mobileDrawer).toBeVisible();
  const drawerBounds = await mobileDrawer.boundingBox();
  expect(drawerBounds).not.toBeNull();
  if (drawerBounds) {
    expect(drawerBounds.x).toBe(0);
    expect(drawerBounds.y).toBe(0);
    expect(drawerBounds.height).toBe(844);
    expect(drawerBounds.x + drawerBounds.width).toBeLessThanOrEqual(390);
  }
  await capture(page, "12-mobile-drawer-390x844.png", consoleIssues);
  await page.keyboard.press("Escape");
  await expect(mobileDrawer).toBeHidden();

  await page.goto("/t/acme/jobs");
  await page.getByRole("link", { name: "smoke-job" }).click();
  await expect(page.getByRole("heading", { name: "smoke-job" })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Details" })).toBeVisible();
  await capture(page, "13-job-detail-mobile-390x844.png", consoleIssues);

  await page.goto("/t/acme/projects");
  await expectCompactMonoControl(page.getByRole("textbox", { name: "Filter this page by project name or slug" }));
  await expectNoHorizontalOverflow(page, 390);
  await expectTableScrollContainer(page);
  await capture(page, "24-projects-mobile-390x844.png", consoleIssues);

  await page.goto("/t/acme/usage");
  await expectCompactMonoControl(page.getByRole("combobox", { name: "Group by" }));
  await expectCompactMonoControl(page.getByRole("combobox", { name: "Top metric" }));
  await expectCompactMonoControl(page.getByRole("combobox", { name: "Top by" }));
  await expectNoHorizontalOverflow(page, 390);
  await expectTableScrollContainer(page);
  await capture(page, "25-usage-mobile-390x844.png", consoleIssues);

  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByRole("button", { name: "User menu" }).click();
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page.getByRole("heading", { name: "You are signed out" })).toBeVisible();
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Choose a mock user" })).toBeVisible();
  await page.getByRole("button", { name: "Alice Researcher" }).click();
  await expect(page).toHaveURL(/\/(?:select-tenant|t\/acme)$/);
  if (new URL(page.url()).pathname.endsWith("/select-tenant")) await page.getByRole("button", { name: "Open tenant" }).click();
  await expect(page).toHaveURL(/\/t\/acme$/);
  await page.goto("/t/acme/secrets?tab=connectors");
  await expect(page.getByRole("heading", { name: "You don't have access to this view" })).toBeVisible();
  await capture(page, "23-403-secrets-connectors-alice.png", consoleIssues);

  expect(cspViolations).toEqual([]);
  expect(externalRequests).toEqual([]);
  expect(externalFontRequests).toEqual([]);
  expect(localFontRequests.length).toBeGreaterThan(0);
  expect(workerUrls.length).toBeGreaterThan(0);
  expect(workerUrls.every((workerUrl) => localHosts.has(new URL(workerUrl).hostname))).toBe(true);
  expect(documentCspHeaders.some((header) => header.includes("worker-src 'self'"))).toBe(true);
});
