import { expect, test } from "@playwright/test";

const jobId = "55555555-5555-4555-8555-555555555555";
const requestedUser = process.env.MOCK_USER;
const selectedUser = requestedUser === "admin" || requestedUser === "bob" ? requestedUser : "alice";
const pickerLabels = { alice: "Alice Researcher", admin: "Platform Admin", bob: "Bob Newcomer" };

function lastSegment(href: string | null): string {
  const segment = href?.split("/").at(-1);
  if (!segment) throw new Error(`expected an href with an id, got ${String(href)}`);
  return segment;
}

test("login, workflows, executions, jobs, CSRF-protected mutations, and logout", async ({ page, baseURL }) => {
  test.setTimeout(120_000);
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
    await page.goto("/t/globex/workflows");
    await expect(page.getByRole("link", { name: "CFD postprocess" })).toBeVisible();
  }

  if (selectedUser === "alice") {
    const workflowConsoleIssues: string[] = [];
    page.on("console", (message) => {
      const path = new URL(page.url()).pathname;
      const expectedNegativeCall = /Failed to load resource: the server responded with a status of (?:400|409)/.test(message.text());
      if (!expectedNegativeCall && /\/t\/[^/]+\/(?:workflows|executions)(?:\/|$)/.test(path) && (message.type() === "error" || message.type() === "warning")) {
        workflowConsoleIssues.push(`${path} [${message.type()}]: ${message.text()}`);
      }
    });
    page.on("pageerror", (error) => {
      const path = new URL(page.url()).pathname;
      if (/\/t\/[^/]+\/(?:workflows|executions)(?:\/|$)/.test(path)) workflowConsoleIssues.push(`${path} [pageerror]: ${error.message}`);
    });
    await page.goto("/t/acme/workflows");
    await expect(page.getByRole("heading", { name: "Workflows" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Gaussian simulation" })).toBeVisible();
    const workflowUrl = await page.getByRole("link", { name: "Gaussian simulation" }).getAttribute("href");
    const draftWorkflowUrl = await page.getByRole("link", { name: "Draft-only workflow" }).getAttribute("href");
    expect(workflowUrl).toBeTruthy();
    expect(draftWorkflowUrl).toBeTruthy();
    const workflowId = lastSegment(workflowUrl);
    const draftWorkflowId = lastSegment(draftWorkflowUrl);
    await page.getByRole("link", { name: "Gaussian simulation" }).click();
    await expect(page.getByRole("tab", { name: "Versions" })).toBeVisible();
    const publishedVersionHref = await page.getByRole("link", { name: "v2", exact: true }).getAttribute("href");
    expect(publishedVersionHref).toBeTruthy();
    const publishedVersionId = lastSegment(publishedVersionHref);
    await page.getByRole("link", { name: "v2", exact: true }).click();
    for (const nodeName of ["prepare", "simulate", "merge", "train", "sweep"]) await expect(page.getByText(nodeName, { exact: true }).first()).toBeVisible();
    await page.getByRole("tab", { name: "YAML" }).click();
    await expect(page.locator(".monaco-editor .view-lines")).toContainText("apiVersion: custos.io/v1alpha1");
    await page.goto(workflowUrl ?? "/t/acme/workflows");
    const workflowCsrfToken = (await page.context().cookies()).find((cookie) => cookie.name === "custos_csrf")?.value ?? "";
    expect(workflowCsrfToken).toBeTruthy();
    const callExecute = (key: string | undefined, requestBody: Record<string, unknown>) => page.evaluate(async ({ idempotencyKey, body, csrfToken }) => {
      const headers: Record<string, string> = { Accept: "application/json", "Content-Type": "application/json", "X-CSRF-Token": csrfToken };
      if (idempotencyKey) headers["Idempotency-Key"] = idempotencyKey;
      const response = await fetch("/api/bff/tenants/acme/workflow-executions", {
        method: "POST", credentials: "same-origin", headers, body: JSON.stringify(body),
      });
      const responseBody: unknown = await response.json().catch(() => null);
      return { status: response.status, body: responseBody };
    }, { idempotencyKey: key, body: requestBody, csrfToken: workflowCsrfToken });
    const idempotencyKey = "smoke-workflow-idempotency-key";
    const executeBody = { workflow: workflowId, version: publishedVersionId, parameters: { molecule: "water", iterations: 20, shards: 2 } };
    const firstRun = await callExecute(idempotencyKey, executeBody);
    expect(firstRun.status).toBe(202);
    const firstExecutionId = (firstRun.body as { id?: unknown }).id;
    expect(typeof firstExecutionId).toBe("string");
    const replay = await callExecute(idempotencyKey, executeBody);
    expect(replay.status).toBe(202);
    expect((replay.body as { id?: unknown }).id).toBe(firstExecutionId);
    const keyConflict = await callExecute(idempotencyKey, { ...executeBody, parameters: { ...executeBody.parameters, molecule: "methane" } });
    expect(keyConflict.status).toBe(409);
    expect((keyConflict.body as { error?: { code?: string } }).error?.code).toBe("IDEMPOTENCY_KEY_REUSED");
    const missingKey = await callExecute(undefined, executeBody);
    expect(missingKey.status).toBe(400);
    expect((missingKey.body as { error?: { code?: string } }).error?.code).toBe("IDEMPOTENCY_KEY_REQUIRED");
    const noPublishedVersion = await callExecute("smoke-draft-only-key", { workflow: draftWorkflowId, parameters: {} });
    expect(noPublishedVersion.status).toBe(409);
    expect((noPublishedVersion.body as { error?: { code?: string } }).error?.code).toBe("NO_PUBLISHED_VERSION");

    await page.getByRole("button", { name: "Run" }).click();
    await expect(page.getByRole("dialog", { name: "Run Gaussian simulation" })).toBeVisible();
    await page.getByLabel(/molecule/).fill("benzene");
    await page.getByRole("button", { name: "Run workflow" }).click();
    await expect(page).toHaveURL(/\/t\/acme\/executions\/[0-9a-f-]+$/);
    await expect(page.getByRole("heading", { level: 1, name: /^Execution / })).toBeVisible();
    await page.getByRole("button", { name: "Cancel execution" }).click();
    await page.getByRole("button", { name: "Confirm cancel" }).click();
    await expect(page.getByText("CANCELED").first()).toBeVisible();

    await page.goto("/t/acme/executions");
    const runningRow = page.getByRole("row").filter({ hasText: "Gaussian simulation" }).filter({ hasText: "RUNNING" }).first();
    await expect(runningRow).toBeVisible();
    await runningRow.getByRole("link").first().click();
    await expect(page.getByText("2/4 COMPLETED")).toBeVisible();
    await expect(page.getByRole("columnheader", { name: "Index / count" })).toBeVisible();
    await page.getByText("simulate", { exact: true }).first().click();
    await expect(page.getByRole("heading", { name: "Task executions · simulate" })).toBeVisible();
    await page.getByRole("button", { name: "Inspect" }).first().click();
    await expect(page.getByRole("region", { name: "Frozen execution specification" })).toBeVisible();
    await page.goto("/t/acme/templates");
    await expect(page.getByRole("heading", { name: "Templates", level: 1 })).toBeVisible();
    await expect(page.getByRole("link", { name: /Hybrid MPI \+ OpenMP/ })).toBeVisible();
    await page.getByRole("link", { name: /MPI \(by task count\)/ }).click();
    await expect(page.getByRole("heading", { name: "MPI (by task count)", level: 1 })).toBeVisible();
    await expect(page.getByText("run", { exact: true }).first()).toBeVisible();
    await page.getByRole("tab", { name: "YAML" }).click();
    await expect(page.locator(".monaco-editor .view-lines")).toContainText("openmpi");
    await page.getByRole("button", { name: "Use template" }).click();
    await expect(page.getByRole("dialog", { name: "Use MPI (by task count)" })).toBeVisible();
    await page.getByLabel("Workflow name").fill("Smoke MPI template");
    await page.getByRole("button", { name: "Create workflow" }).click();
    await expect(page).toHaveURL(/\/t\/acme\/workflows\/[0-9a-f-]+$/);
    await expect(page.getByRole("heading", { name: "Smoke MPI template", level: 1 })).toBeVisible();
    await expect(page.getByRole("link", { name: "v1", exact: true })).toBeVisible();

    await page.goto("/t/acme/workflows");
    await page.getByRole("link", { name: "Import sbatch" }).click();
    await expect(page.getByRole("heading", { name: "Import sbatch" })).toBeVisible();
    await page.getByLabel("Workflow name (optional)").fill("smoke-import");
    await page.locator('input[type="file"]').setInputFiles({
      name: "smoke-import.sbatch",
      mimeType: "text/x-shellscript",
      buffer: Buffer.from("#!/bin/bash\n#SBATCH --job-name=smoke-import\n#SBATCH --nodes=1\n#SBATCH --ntasks=1\n#SBATCH --time=00:05:00\necho smoke\n"),
    });
    await page.getByRole("button", { name: "Import", exact: true }).click();
    await expect(page.getByRole("cell", { name: "smoke-import.sbatch" })).toBeVisible();
    await expect(page.locator(".monaco-editor .view-lines")).toContainText("smoke-import");
    await expect(page.getByText("No workflow is created until", { exact: false })).toBeVisible();
    await page.getByRole("button", { name: "Create workflow" }).click();
    await expect(page).toHaveURL(/\/t\/acme\/workflows\/[0-9a-f-]+$/);
    await expect(page.getByRole("heading", { name: "smoke-import", level: 1 })).toBeVisible();
    await page.getByRole("link", { name: "v1", exact: true }).click();
    await expect(page.getByRole("button", { name: "Download YAML" })).toBeVisible();
    const yamlDownloadPromise = page.waitForEvent("download");
    await page.getByRole("button", { name: "Download YAML" }).click();
    const yamlDownload = await yamlDownloadPromise;
    expect(yamlDownload.suggestedFilename()).toMatch(/\.yaml$/);
    await page.locator(".workflow-canvas .react-flow__node").filter({ hasText: "smoke-import" }).click();
    await expect(page.getByRole("button", { name: "Download sbatch" })).toBeVisible();
    const sbatchDownloadPromise = page.waitForEvent("download");
    await page.getByRole("button", { name: "Download sbatch" }).click();
    const sbatchDownload = await sbatchDownloadPromise;
    expect(sbatchDownload.suggestedFilename()).toMatch(/\.sbatch$/);

    expect(workflowConsoleIssues).toEqual([]);

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
