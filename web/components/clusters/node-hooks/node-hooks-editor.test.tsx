// @vitest-environment jsdom
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const refresh = vi.fn();
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh }) }));
const saveNodeConfig = vi.fn();
vi.mock("@/lib/nodehooks/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/nodehooks/client")>()),
  saveNodeConfig: (...args: unknown[]) => saveNodeConfig(...args) as unknown,
  loadNodeStatus: () => Promise.resolve(null),
  loadNodeTokens: () => Promise.resolve([]),
}));

import { NodeHooksEditor } from "@/components/clusters/node-hooks/node-hooks-editor";

const tenant = { id: "11111111-1111-4111-8111-111111111111", slug: "acme", name: "Acme Research" };
const view = {
  cluster_id: "c1",
  config: {
    isolation_mode: "namespace" as const, tenant_exclusive_mechanism: "mcs_label" as const, mount_timeout_seconds: 30,
    shared_mounts: [], tenant_mounts: [], hooks: [],
  },
  revision: 2, content_sha256: "b".repeat(64), version: 3,
  warnings: [{ code: "NAMESPACE_REQUIRES_SLURM_25_11" as const, message: "namespace mode needs Slurm 25.11" }],
};

function mount() {
  return render(
    <NodeHooksEditor
      clusterId="c1" clusterName="e2e" initialView={view} initialTokens={[]}
      initialStatus={{ current_revision: 2, current_bundle_sha256: "c".repeat(64), items: [] }}
      tenants={[tenant]} csrfToken="csrf"
    />,
  );
}

async function addSharedMount(user: ReturnType<typeof userEvent.setup>, target: string) {
  await user.click(screen.getByRole("button", { name: "Add shared mount" }));
  const row = screen.getByTestId("mount-row");
  await user.type(within(row).getByLabelText("Name"), "apps");
  await user.type(within(row).getByLabelText("Source"), "server:/hpc/apps");
  await user.type(within(row).getByLabelText("Target"), target);
}

beforeEach(() => { saveNodeConfig.mockReset(); refresh.mockReset(); });
afterEach(() => { cleanup(); });

describe("NodeHooksEditor", () => {
  it("renders API warnings and the revision", () => {
    mount();
    expect(screen.getByText("NAMESPACE_REQUIRES_SLURM_25_11")).toBeInTheDocument();
    expect(screen.getByTestId("node-revision")).toHaveTextContent("2");
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("blocks saving invalid input client-side and highlights the field", async () => {
    const user = userEvent.setup();
    mount();
    await addSharedMount(user, "/etc/x");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(saveNodeConfig).not.toHaveBeenCalled();
    expect(await screen.findByText("This path is reserved for the operating system.")).toBeInTheDocument();
  });

  it("saves with the loaded version and updates the revision", async () => {
    const user = userEvent.setup();
    saveNodeConfig.mockResolvedValue({ kind: "saved", view: { ...view, revision: 3, version: 4, warnings: [] } });
    mount();
    await addSharedMount(user, "/apps");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => { expect(screen.getByTestId("node-revision")).toHaveTextContent("3"); });
    const [cluster, version, config] = saveNodeConfig.mock.calls[0] as [string, number, { shared_mounts: { target: string; options: string[] }[] }];
    expect(cluster).toBe("c1");
    expect(version).toBe(3);
    expect(config.shared_mounts[0]).toMatchObject({ target: "/apps", options: ["ro"] });
    expect(screen.getByText("Saved")).toBeInTheDocument();
    expect(screen.queryByText("NAMESPACE_REQUIRES_SLURM_25_11")).not.toBeInTheDocument();
  });

  it("maps a 422 field path onto the row", async () => {
    const user = userEvent.setup();
    saveNodeConfig.mockResolvedValue({
      kind: "invalid", message: "node configuration is invalid",
      issues: [{ field: "shared_mounts[0].source", reason: "server says no" }],
    });
    mount();
    await addSharedMount(user, "/apps");
    await user.click(screen.getByRole("button", { name: "Save" }));
    const row = screen.getByTestId("mount-row");
    expect(await within(row).findByText("server says no")).toBeInTheDocument();
    expect(screen.getByText(/Fix the highlighted fields/)).toBeInTheDocument();
  });

  it("offers Reload on a version conflict and only discards edits after confirming", async () => {
    const user = userEvent.setup();
    saveNodeConfig.mockResolvedValue({ kind: "conflict" });
    mount();
    await addSharedMount(user, "/apps");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("This configuration changed since you loaded it.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Reload" }));
    expect(refresh).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Keep editing" }));
    expect(refresh).not.toHaveBeenCalled();
    expect(screen.getAllByTestId("mount-row")).toHaveLength(1);

    await user.click(screen.getByRole("button", { name: "Reload" }));
    await user.click(screen.getByRole("button", { name: "Discard and reload" }));
    expect(refresh).toHaveBeenCalledTimes(1);
    await waitFor(() => { expect(screen.queryByTestId("mount-row")).not.toBeInTheDocument(); });
  });
});
