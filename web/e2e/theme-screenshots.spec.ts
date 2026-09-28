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
  const styles = await locator.evaluate((element) => {
    const computed = getComputedStyle(element);
    const fontVariable = getComputedStyle(document.documentElement).getPropertyValue("--font-plex-mono").trim();
    return {
      fontFamily: computed.fontFamily,
      monoFamily: fontVariable.split(",")[0].trim().replace(/[\"']/g, ""),
      fontSize: Number.parseFloat(computed.fontSize),
      textTransform: computed.textTransform,
      height: Math.round(element.getBoundingClientRect().height),
    };
  });
  expect(styles.fontFamily).toContain(styles.monoFamily);
  expect(styles.fontSize).toBeGreaterThanOrEqual(11);
  expect(styles.fontSize).toBeLessThanOrEqual(12);
  expect(styles.textTransform).toBe("uppercase");
  expect(styles.height).toBe(32);
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
  const externalFontRequests: string[] = [];
  const localFontRequests: string[] = [];
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
    if (url.hostname === "fonts.googleapis.com" || url.hostname.endsWith(".gstatic.com")) externalFontRequests.push(request.url());
    if (url.pathname.endsWith(".woff2")) localFontRequests.push(request.url());
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

  expect(cspViolations).toEqual([]);
  expect(externalFontRequests).toEqual([]);
  expect(localFontRequests.length).toBeGreaterThan(0);
});
