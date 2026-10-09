import { describe, expect, it } from "vitest";
import type { NodeConfig, NodeConfigDraft } from "@/lib/nodehooks/config";
import {
  configFromDraft,
  draftFromConfig,
  emptyHook,
  emptyMount,
  groupIssues,
  interpretSave,
  isDirty,
  issuesFromDetails,
  validateDraft,
  validateExtraOption,
} from "@/lib/nodehooks/config";
import { interpretCreateToken } from "@/lib/nodehooks/client";

const tenantA = "11111111-1111-4111-8111-111111111111";
const tenantB = "66666666-6666-4666-8666-666666666666";

function config(): NodeConfig {
  return {
    isolation_mode: "tenant_exclusive",
    tenant_exclusive_mechanism: "mcs_label",
    mount_timeout_seconds: 30,
    shared_mounts: [{ name: "apps", fstype: "nfs4", source: "server:/hpc/apps", target: "/apps", options: ["ro", "nfsvers=4.2"] }],
    tenant_mounts: [
      { tenant: tenantA, name: "flight-data", fstype: "nfs4", source: "server:/tenantA/flight_data", target: "/mnt/data", options: ["hard", "nodev", "nosuid", "rw"] },
      { tenant: tenantA, name: "scratch", fstype: "nfs4", source: "server:/tenantA/scratch", target: "/scratch", options: ["nodev", "nosuid", "rw"] },
      { tenant: tenantB, name: "flight-data", fstype: "nfs4", source: "server:/tenantB/flight_data", target: "/mnt/data", options: ["nodev", "nosuid", "ro"] },
    ],
    hooks: [{ name: "metrics", phase: "prolog", order: 1, script: "#!/bin/bash\necho hi\n" }],
  };
}

describe("draft conversion", () => {
  it("round-trips a server config, hiding implied options", () => {
    const draft = draftFromConfig(config());
    expect(draft.shared[0]).toMatchObject({ readOnly: true, extraOptions: ["nfsvers=4.2"] });
    expect(draft.tenantMounts[0]).toMatchObject({ readOnly: false, extraOptions: ["hard"] });
    expect(draft.tenantMounts[2]?.readOnly).toBe(true);
    expect(draft.tenantGroups).toEqual([tenantA, tenantB]);
    const rebuilt = configFromDraft(draft);
    expect(rebuilt.shared_mounts[0]?.options).toEqual(["ro", "nfsvers=4.2"]);
    expect(rebuilt.tenant_mounts[0]?.options).toEqual(["rw", "hard"]);
    expect(rebuilt.mount_timeout_seconds).toBe(30);
  });

  it("tracks dirty state", () => {
    const saved = draftFromConfig(config());
    const copy: NodeConfigDraft = structuredClone(saved);
    expect(isDirty(copy, saved)).toBe(false);
    copy.timeout = "45";
    expect(isDirty(copy, saved)).toBe(true);
    const grouped = structuredClone(saved);
    grouped.tenantGroups = [...grouped.tenantGroups, "extra-tenant"];
    expect(isDirty(grouped, saved)).toBe(true);
  });
});

describe("client-side validation mirrors the server rules", () => {
  const field = (draft: NodeConfigDraft) => validateDraft(draft).map((issue) => issue.field);

  it("accepts a valid configuration", () => {
    expect(validateDraft(draftFromConfig(config()))).toEqual([]);
  });

  it("rejects reserved, relative, traversal and trailing-slash targets", () => {
    for (const target of ["/etc/x", "/tmp", "/dev/shm/a", "relative", "/a/../b", "/data/", "/"]) {
      const draft = draftFromConfig(config());
      draft.shared[0] = { ...draft.shared[0], target };
      expect(field(draft), target).toContain("shared_mounts[0].target");
    }
  });

  it("rejects bad sources, names and options", () => {
    const draft = draftFromConfig(config());
    draft.shared[0] = { ...draft.shared[0], source: "no-colon", name: "Bad Name", extraOptions: ["suid", "hard", "soft"] };
    expect(field(draft)).toEqual(expect.arrayContaining([
      "shared_mounts[0].source", "shared_mounts[0].name", "shared_mounts[0].options",
    ]));
    expect(validateExtraOption("ro")).toMatch(/toggle/);
    expect(validateExtraOption("nconnect=99")).toMatch(/1..16/);
    expect(validateExtraOption("sec=krb5p")).toBeNull();
    expect(validateExtraOption("nfsvers=5")).not.toBeNull();
  });

  it("flags duplicates, nesting and cross-list clashes", () => {
    const draft = draftFromConfig(config());
    draft.shared.push({ ...emptyMount(true), name: "apps", source: "s:/a", target: "/apps" });
    draft.tenantMounts.push({ ...emptyMount(false), tenant: tenantA, name: "flight-data", source: "s:/b", target: "/scratch/sub" });
    draft.tenantMounts.push({ ...emptyMount(false), tenant: tenantB, name: "extra", source: "s:/c", target: "/apps/nested" });
    const fields = field(draft);
    expect(fields).toEqual(expect.arrayContaining([
      "shared_mounts[1].name", "shared_mounts[1].target",
      "tenant_mounts[3].name", "tenant_mounts[3].target", "tenant_mounts[4].target",
    ]));
    // The same target for different tenants is allowed.
    expect(fields).not.toContain("tenant_mounts[2].target");
  });

  it("checks hooks and the mount timeout", () => {
    const draft = draftFromConfig(config());
    draft.timeout = "3";
    draft.hooks.push({ ...emptyHook(), name: "second", order: "1", script: "echo no shebang" });
    draft.hooks.push({ ...emptyHook(), name: "third", order: "100" });
    expect(field(draft)).toEqual(expect.arrayContaining([
      "mount_timeout_seconds", "hooks[1].script", "hooks[1].order", "hooks[2].order",
    ]));
    expect(emptyHook().script.startsWith("#!/bin/bash\n")).toBe(true);
  });
});

describe("field path to row mapping", () => {
  it("attaches server paths to the row id at that index", () => {
    const draft = draftFromConfig(config());
    const grouped = groupIssues([
      { field: "tenant_mounts[2].target", reason: "reserved" },
      { field: "tenant_mounts[2].options", reason: "bad option" },
      { field: "shared_mounts[0].name", reason: "duplicate" },
      { field: "hooks[0].script", reason: "must parse" },
      { field: "mount_timeout_seconds", reason: "range" },
      { field: "tenant_mounts[9].target", reason: "out of range" },
      { field: "", reason: "general failure" },
    ], draft);
    expect(grouped.rows[draft.tenantMounts[2].id]).toEqual({ target: "reserved", options: "bad option" });
    expect(grouped.rows[draft.shared[0].id]).toEqual({ name: "duplicate" });
    expect(grouped.rows[draft.hooks[0].id]).toEqual({ script: "must parse" });
    expect(grouped.general).toEqual({ mount_timeout_seconds: "range" });
    expect(grouped.unmapped).toEqual(["tenant_mounts[9].target: out of range", "general failure"]);
  });

  it("keeps a whole-row error under the row key", () => {
    const draft = draftFromConfig(config());
    const grouped = groupIssues([{ field: "hooks[0]", reason: "invalid" }], draft);
    expect(grouped.rows[draft.hooks[0].id]).toEqual({ row: "invalid" });
  });

  it("parses apperr details defensively", () => {
    expect(issuesFromDetails([{ field: "a", reason: "b" }, null, { reason: "c" }, { field: 3, reason: "x" }, { field: "d" }]))
      .toEqual([{ field: "a", reason: "b" }, { field: "", reason: "c" }, { field: "", reason: "x" }]);
    expect(issuesFromDetails(undefined)).toEqual([]);
  });
});

describe("save outcomes (optimistic version flow)", () => {
  const view = { cluster_id: "c", config: config(), revision: 4, content_sha256: "a".repeat(64), version: 5, warnings: [] };

  it("accepts the stored view", () => {
    expect(interpretSave(200, view)).toEqual({ kind: "saved", view });
  });

  it("reports a version conflict so the UI can offer Reload", () => {
    expect(interpretSave(409, { error: { code: "VERSION_CONFLICT" } })).toEqual({ kind: "conflict" });
  });

  it("returns field issues for 422", () => {
    const outcome = interpretSave(422, {
      error: { code: "NODE_MOUNT_INVALID", message: "invalid", details: [{ field: "tenant_mounts[2].target", reason: "reserved" }] },
    });
    expect(outcome).toEqual({
      kind: "invalid", message: "invalid", issues: [{ field: "tenant_mounts[2].target", reason: "reserved" }],
    });
  });

  it("maps 403 and unexpected bodies", () => {
    expect(interpretSave(403, null)).toEqual({ kind: "forbidden" });
    expect(interpretSave(200, { nope: true }).kind).toBe("error");
    expect(interpretSave(500, { error: { code: "INTERNAL" } })).toEqual({ kind: "error", message: "INTERNAL" });
  });
});

describe("token creation response", () => {
  it("returns the secret only for 201", () => {
    expect(interpretCreateToken(201, { id: "i", name: "rack-1", token: "cnt_secret" }))
      .toEqual({ ok: true, created: { id: "i", name: "rack-1", token: "cnt_secret" } });
    expect(interpretCreateToken(201, { id: "i" }).ok).toBe(false);
    const rejected = interpretCreateToken(422, { error: { code: "NODE_TOKEN_INVALID", details: [{ field: "name", reason: "bad" }] } });
    expect(rejected).toEqual({ ok: false, message: "name: bad" });
  });
});
