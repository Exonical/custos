// @ts-check

/** @typedef {import("../lib/api/schema").components["schemas"]["Me"]} Me */
/** @typedef {import("../lib/api/schema").components["schemas"]["Project"]} Project */
/** @typedef {import("../lib/api/schema").components["schemas"]["ProjectMembership"]} ProjectMembership */
/** @typedef {import("../lib/api/schema").components["schemas"]["ClusterBinding"]} ClusterBinding */
/** @typedef {import("../lib/api/schema").components["schemas"]["ClusterSummary"]} ClusterSummary */
/** @typedef {import("../lib/api/schema").components["schemas"]["PartitionRecord"]} PartitionRecord */
/** @typedef {import("../lib/api/schema").components["schemas"]["SecretConnector"]} SecretConnector */
/** @typedef {import("../lib/api/schema").components["schemas"]["SecretReference"]} SecretReference */
/** @typedef {import("../lib/api/schema").components["schemas"]["Allocation"]} Allocation */
/** @typedef {import("../lib/api/schema").components["schemas"]["AccountingAllocationItem"]} AccountingAllocationItem */
/** @typedef {import("../lib/api/schema").components["schemas"]["AccountingTopRow"]} AccountingTopRow */
/** @typedef {{day:string,user_id:string,project_id:string,cluster_id:string,account:string,partition:string,jobs:number,failed:number,cpu_seconds:number,gpu_seconds:number,node_seconds:number,mem_gb_seconds:number,wait_seconds:number[],run_seconds:number[]}} UsageDailyRecord */
/** @typedef {import("../lib/api/schema").components["schemas"]["Job"]} Job */
/** @typedef {import("../lib/api/schema").components["schemas"]["Workflow"]} Workflow */
/** @typedef {import("../lib/api/schema").components["schemas"]["WorkflowVersion"]} WorkflowVersion */
/** @typedef {Omit<WorkflowVersion, "spec" | "layout"> & {spec: import("../lib/workflow/spec").CustosWorkflow, layout?: unknown}} WorkflowVersionFixture */
/** @typedef {import("../lib/api/schema").components["schemas"]["WorkflowExecution"]} WorkflowExecution */
/** @typedef {Omit<WorkflowExecution, "parameters"> & {parameters: Record<string, unknown>}} WorkflowExecutionFixture */
/** @typedef {import("../lib/api/schema").components["schemas"]["TaskExecution"]} TaskExecution */
/** @typedef {import("../lib/api/schema").components["schemas"]["MembershipRef"]} MembershipRef */
/** @typedef {import("../lib/api/schema").components["schemas"]["ProjectMembershipRef"]} ProjectMembershipRef */
/** @typedef {"alice" | "admin" | "bob"} MockUserName */
/** @typedef {"acme" | "globex"} TenantSlug */
/** @typedef {"p1" | "genomics" | "climate" | "cfd"} ProjectSlug */
/** @typedef {"cluster-e2e" | "hopper" | "titan"} ClusterName */
/** @typedef {{ sub: string, name: string, email: string, me: Me }} MockUser */
/** @typedef {{ users: Record<MockUserName, MockUser>, projects: Record<TenantSlug, Project[]>, projectMembers: Record<string, ProjectMembership[]>, clusters: Record<TenantSlug, ClusterSummary[]>, clusterBindings: Record<TenantSlug, ClusterBinding[]>, partitions: Record<string, PartitionRecord[]>, connectors: Record<TenantSlug, SecretConnector[]>, references: Record<TenantSlug, SecretReference[]>, projectAllocations: Record<string, Allocation[]>, tenantAllocations: Record<TenantSlug, AccountingAllocationItem[]>, workflows: Record<TenantSlug, Workflow[]>, workflowVersions: Record<string, WorkflowVersionFixture[]>, workflowExecutions: Record<TenantSlug, WorkflowExecutionFixture[]>, taskExecutions: Record<string, TaskExecution[]>, frozenTaskSpecs: Map<string, unknown>, idempotency: Map<string, {bodyHash: string, execution: WorkflowExecutionFixture}>, jobs: Record<TenantSlug, Job[]>, usageRecords: Record<TenantSlug, UsageDailyRecord[]>, executionSpecs: Map<string, Record<string, never>> }} MockData */

/** @type {Record<TenantSlug, string>} */
const tenantIds = {
  acme: "11111111-1111-4111-8111-111111111111",
  globex: "66666666-6666-4666-8666-666666666666",
};

/** @type {Record<MockUserName, string>} */
const userIds = {
  alice: "44444444-4444-4444-8444-444444444444",
  admin: "77777777-7777-4777-8777-777777777777",
  bob: "88888888-8888-4888-8888-888888888888",
};

/** @type {Record<ProjectSlug, string>} */
const projectIds = {
  p1: "22222222-2222-4222-8222-222222222222",
  genomics: "22222222-2222-4222-8222-222222222223",
  climate: "22222222-2222-4222-8222-222222222224",
  cfd: "22222222-2222-4222-8222-222222222225",
};

const workflowIds = {
  gaussian: generatedUuid(10_001),
  chain: generatedUuid(10_002),
  archived: generatedUuid(10_003),
  draftOnly: generatedUuid(10_004),
  globexPipeline: generatedUuid(10_005),
};

const workflowVersionIds = {
  gaussianDeprecated: generatedUuid(11_001),
  gaussianPublished: generatedUuid(11_002),
  gaussianDraft: generatedUuid(11_003),
  chainPublished: generatedUuid(11_004),
  chainDraft: generatedUuid(11_005),
  archivedPublished: generatedUuid(11_006),
  draftOnly: generatedUuid(11_007),
  globexPublished: generatedUuid(11_008),
};

/** @type {Record<ClusterName, string>} */
const clusterIds = {
  "cluster-e2e": "33333333-3333-4333-8333-333333333333",
  hopper: "33333333-3333-4333-8333-333333333334",
  titan: "33333333-3333-4333-8333-333333333335",
};

/** @type {Record<MockUserName, {sub:string,name:string,email:string}>} */
const profiles = {
  alice: { sub: "alice-subject", name: "Alice Researcher", email: "alice@example.test" },
  admin: { sub: "admin-subject", name: "Platform Admin", email: "admin@example.test" },
  bob: { sub: "bob-subject", name: "Bob Newcomer", email: "bob@example.test" },
};

/** @param {number} timestamp */
function iso(timestamp) {
  return new Date(timestamp).toISOString();
}

/** @param {number} seed */
function seededRandom(seed) {
  let value = Math.abs(Math.trunc(seed)) % 2_147_483_647 || 1;
  return () => {
    value = value * 48_271 % 2_147_483_647;
    return value / 2_147_483_647;
  };
}

/** @param {number} value */
function digest(value) {
  return `sha256:${Math.trunc(value).toString(16).padStart(64, "0")}`;
}

/** @param {number} number */
function generatedUuid(number) {
  return `90000000-0000-4000-8000-${Math.trunc(number).toString(16).padStart(12, "0")}`;
}

/** @param {number} startedAt @returns {Record<TenantSlug, Project[]>} */
function makeProjects(startedAt) {
  return {
    acme: [
      { id: projectIds.p1, slug: "p1", name: "Project One", description: "Smoke project", state: "active", settings: {}, version: 1, created_at: iso(startedAt - 90_000_000), updated_at: iso(startedAt - 90_000_000) },
      { id: projectIds.genomics, slug: "genomics", name: "Genomics", description: "Population genomics research", state: "active", settings: {}, version: 1, created_at: iso(startedAt - 80_000_000), updated_at: iso(startedAt - 80_000_000) },
      { id: projectIds.climate, slug: "climate", name: "Climate Models", description: "Regional climate modeling", state: "active", settings: {}, version: 1, created_at: iso(startedAt - 70_000_000), updated_at: iso(startedAt - 70_000_000) },
    ],
    globex: [
      { id: projectIds.cfd, slug: "cfd", name: "CFD", description: "Computational fluid dynamics", state: "active", settings: {}, version: 1, created_at: iso(startedAt - 60_000_000), updated_at: iso(startedAt - 60_000_000) },
    ],
  };
}

/** @returns {Record<TenantSlug, ClusterSummary[]>} */
function makeClusters() {
  return {
    acme: [
      { id: clusterIds["cluster-e2e"], name: "cluster-e2e", display_name: "E2E Cluster", state: "active", slurm_version: "26.05.4", partitions: ["debug", "compute"], node_summary: { idle: 18, allocated: 6 }, defaults: {} },
      { id: clusterIds.hopper, name: "hopper", display_name: "Hopper", state: "active", slurm_version: "26.05.4", partitions: ["gpu", "batch"], node_summary: { idle: 4, allocated: 12 }, defaults: {} },
    ],
    globex: [
      { id: clusterIds.titan, name: "titan", display_name: "Titan", state: "degraded", slurm_version: "26.05.3", partitions: ["cpu", "long"], node_summary: { idle: 3, allocated: 21 }, defaults: {} },
    ],
  };
}

/** @param {string} issuer @returns {Record<MockUserName, MockUser>} */
export function createMockUsers(issuer) {
  /** @type {MembershipRef} */
  const acmeMembership = { tenant_id: tenantIds.acme, slug: "acme", name: "Acme Research", roles: ["researcher"] };
  /** @type {MembershipRef} */
  const adminAcmeMembership = { tenant_id: tenantIds.acme, slug: "acme", name: "Acme Research", roles: ["tenant-admin"] };
  /** @type {MembershipRef} */
  const adminGlobexMembership = { tenant_id: tenantIds.globex, slug: "globex", name: "Globex HPC", roles: ["tenant-admin"] };
  /** @type {ProjectMembershipRef[]} */
  const aliceProjectMemberships = [
    { project_id: projectIds.p1, slug: "p1", tenant_id: tenantIds.acme, roles: ["project-member"] },
    { project_id: projectIds.genomics, slug: "genomics", tenant_id: tenantIds.acme, roles: ["project-member"] },
    { project_id: projectIds.climate, slug: "climate", tenant_id: tenantIds.acme, roles: ["project-member"] },
  ];
  /** @type {ProjectMembershipRef[]} */
  const adminProjectMemberships = [
    ...aliceProjectMemberships.map((membership) => ({ ...membership, roles: ["project-admin"] })),
    { project_id: projectIds.cfd, slug: "cfd", tenant_id: tenantIds.globex, roles: ["project-admin"] },
  ];

  /** @param {{sub:string,name:string,email:string}} profile @param {string} userId @param {string[]} platformRoles @param {MembershipRef[]} memberships @param {ProjectMembershipRef[]} projectMemberships @returns {MockUser} */
  function makeUser(profile, userId, platformRoles, memberships, projectMemberships) {
    /** @type {Me} */
    const me = {
      principal: { issuer, subject: profile.sub, kind: "user", name: profile.name, email: profile.email },
      user_id: userId,
      platform_roles: platformRoles,
      memberships,
      project_memberships: projectMemberships,
    };
    return { ...profile, me };
  }

  return {
    alice: makeUser(profiles.alice, userIds.alice, [], [acmeMembership], aliceProjectMemberships),
    admin: makeUser(profiles.admin, userIds.admin, ["platform-admin"], [adminAcmeMembership, adminGlobexMembership], adminProjectMemberships),
    bob: makeUser(profiles.bob, userIds.bob, [], [], []),
  };
}

/** @param {string | undefined} token @returns {MockUserName | undefined} */
export function userNameFromAccessToken(token) {
  const username = token?.match(/^mock-access-(alice|admin|bob)-\d+$/)?.[1];
  if (username === "alice" || username === "admin" || username === "bob") return username;
  return undefined;
}

/** @param {number} startedAt @param {Record<MockUserName, MockUser>} users @param {number} seed @returns {Record<TenantSlug, Job[]>} */
function makeJobs(startedAt, users, seed) {
  const random = seededRandom(seed);
  /** @type {Job["state"][]} */
  const states = ["SUBMITTING", "QUEUED", "RUNNING", "COMPLETED", "FAILED", "CANCELED"];
  /** @type {Record<TenantSlug, ProjectSlug[]>} */
  const projectSlugs = { acme: ["p1", "genomics", "climate"], globex: ["cfd"] };
  /** @type {Record<TenantSlug, ClusterName[]>} */
  const clusterNames = { acme: ["cluster-e2e", "hopper"], globex: ["titan"] };
  /** @type {Record<TenantSlug, Job[]>} */
  const jobs = { acme: [], globex: [] };

  for (const tenant of /** @type {TenantSlug[]} */ (["acme", "globex"])) {
    const count = tenant === "acme" ? 59 : 60;
    for (let index = 0; index < count; index += 1) {
      const state = states[Math.floor(random() * states.length)];
      const projectSlug = projectSlugs[tenant][Math.floor(random() * projectSlugs[tenant].length)];
      const clusterName = clusterNames[tenant][Math.floor(random() * clusterNames[tenant].length)];
      const ageMinutes = 2 + Math.floor(random() * 43_200);
      const createdAt = startedAt - ageMinutes * 60_000;
      const started = ["RUNNING", "COMPLETED", "FAILED", "CANCELED"].includes(state);
      const terminal = ["COMPLETED", "FAILED", "CANCELED"].includes(state);
      const owner = index % 3 === 0 ? users.admin : users.alice;
      const number = (tenant === "acme" ? 1000 : 2000) + index;
      /** @type {Job} */
      const job = {
        id: generatedUuid(number),
        tenant_id: tenantIds[tenant],
        project_id: projectIds[projectSlug],
        cluster_id: clusterIds[clusterName],
        created_by: owner.me.user_id,
        name: `${tenant}-${projectSlug}-job-${String(index + 1).padStart(3, "0")}`,
        state,
        ...((state === "FAILED") ? { state_reason: "Worker process exited with status 1" } : {}),
        ...((state === "SUBMITTING" || state === "QUEUED") ? { state_reason: "Waiting for cluster capacity" } : {}),
        slurm_job_id: number + 1000,
        slurm_state: state === "QUEUED" || state === "SUBMITTING" ? "PENDING" : state,
        ...(state === "FAILED" ? { exit_code: 1 } : {}),
        resource_request: {},
        ...(terminal ? { resource_usage: { cpu_hours: Number((0.1 + random() * 240).toFixed(2)), max_rss_bytes: Math.floor(1_000_000_000 + random() * 15_000_000_000) } } : {}),
        script_digest: digest(number + 1),
        script_language: "bash",
        execution_spec_id: digest(number + 2000),
        submitted_at: iso(createdAt + 30_000),
        ...(started ? { started_at: iso(createdAt + 90_000) } : {}),
        ...(terminal ? { ended_at: iso(createdAt + 5_000_000), last_reconciled_at: iso(createdAt + 5_100_000) } : {}),
        version: terminal ? 3 : 2,
        created_at: iso(createdAt),
        updated_at: iso(terminal ? createdAt + 5_100_000 : started ? createdAt + 90_000 : createdAt + 30_000),
      };
      jobs[tenant].push(job);
    }
  }

  const smokeCreatedAt = startedAt - 60_000;
  /** @type {Job} */
  const smokeJob = {
    id: "55555555-5555-4555-8555-555555555555",
    tenant_id: tenantIds.acme,
    project_id: projectIds.p1,
    cluster_id: clusterIds["cluster-e2e"],
    created_by: users.alice.me.user_id,
    name: "smoke-job",
    state: "RUNNING",
    state_reason: "",
    slurm_job_id: 7,
    slurm_state: "RUNNING",
    resource_request: {},
    resource_usage: { cpu_hours: 0.25 },
    script_digest: digest(123456789),
    script_language: "bash",
    execution_spec_id: digest(987654321),
    submitted_at: iso(smokeCreatedAt + 30_000),
    started_at: iso(smokeCreatedAt + 60_000),
    version: 2,
    created_at: iso(smokeCreatedAt),
    updated_at: iso(smokeCreatedAt + 60_000),
  };
  jobs.acme.push(smokeJob);
  for (const tenant of /** @type {TenantSlug[]} */ (["acme", "globex"])) {
    jobs[tenant].sort((left, right) => right.created_at.localeCompare(left.created_at) || right.id.localeCompare(left.id));
  }
  return jobs;
}

/** @param {number} startedAt @param {Record<MockUserName, MockUser>} users @returns {Record<string, ProjectMembership[]>} */
function makeProjectMembers(startedAt, users) {
  /** @type {Record<string, ProjectMembership[]>} */
  const members = {};
  for (const user of Object.values(users)) {
    for (const [index, membership] of (user.me.project_memberships ?? []).entries()) {
      const createdAt = iso(startedAt - (index + 1) * 86_400_000);
      /** @type {ProjectMembership} */
      const record = {
        project_id: membership.project_id,
        user_id: user.me.user_id,
        roles: /** @type {ProjectMembership["roles"]} */ (membership.roles),
        source: "manual",
        created_at: createdAt,
        updated_at: createdAt,
      };
      (members[record.project_id] ??= []).push(record);
    }
  }
  return members;
}

/** @param {number} startedAt @param {Record<TenantSlug, Project[]>} projects @param {Record<TenantSlug, ClusterSummary[]>} clusters @returns {Record<TenantSlug, ClusterBinding[]>} */
function makeClusterBindings(startedAt, projects, clusters) {
  /** @type {Record<TenantSlug, ClusterBinding[]>} */
  const bindings = { acme: [], globex: [] };
  let index = 0;
  for (const tenant of /** @type {TenantSlug[]} */ (["acme", "globex"])) {
    for (const project of projects[tenant]) {
      const availableClusters = tenant === "acme" && project.slug === "p1"
        ? clusters.acme
        : tenant === "acme" && project.slug === "climate"
          ? clusters.acme.filter((cluster) => cluster.name === "hopper")
          : clusters[tenant];
      for (const cluster of availableClusters) {
        const checkedAt = iso(startedAt - index * 60_000);
        const isDrifting = cluster.state === "degraded";
        /** @type {ClusterBinding} */
        const binding = {
          id: generatedUuid(5_000 + index),
          project_id: project.id,
          cluster_id: cluster.id,
          slurm_account: `${tenant}-${project.slug}`,
          default_partition: cluster.partitions[0] ?? "default",
          allowed_partitions: cluster.partitions,
          default_qos: "normal",
          allowed_qos: ["normal", "high"],
          enabled: true,
          version: 1,
          created_at: iso(startedAt - 30 * 86_400_000),
          updated_at: checkedAt,
          drift_state: isDrifting ? "drift" : "ok",
          drift_checked_at: checkedAt,
          ...(isDrifting ? { drift: [{ code: "PARTITION_DRIFT", detail: "Cluster partition state differs from the assigned binding." }] } : {}),
        };
        bindings[tenant].push(binding);
        index += 1;
      }
    }
  }
  return bindings;
}

/** @param {number} startedAt @param {Record<TenantSlug, ClusterSummary[]>} clusters @returns {Record<string, PartitionRecord[]>} */
function makePartitions(startedAt, clusters) {
  /** @type {Record<string, PartitionRecord[]>} */
  const partitions = {};
  for (const clusterList of Object.values(clusters)) {
    for (const cluster of clusterList) {
      partitions[cluster.id] = cluster.partitions.map((name, index) => ({
        name,
        attributes: {},
        synced_at: iso(startedAt - index * 30_000),
      }));
    }
  }
  return partitions;
}

/** @param {Record<TenantSlug, Job[]>} jobs @param {Record<TenantSlug, Project[]>} projects @param {Record<TenantSlug, ClusterSummary[]>} clusters @param {number} seed @returns {Record<TenantSlug, UsageDailyRecord[]>} */
function makeUsageRecords(jobs, projects, clusters, seed) {
  const random = seededRandom(seed + 97);
  /** @type {Record<TenantSlug, UsageDailyRecord[]>} */
  const records = { acme: [], globex: [] };
  for (const tenant of /** @type {TenantSlug[]} */ (["acme", "globex"])) {
    for (const job of jobs[tenant]) {
      if (!job.ended_at) continue;
      const ended = Date.parse(job.ended_at);
      const started = Date.parse(job.started_at ?? job.submitted_at ?? job.created_at);
      const submitted = Date.parse(job.submitted_at ?? job.created_at);
      const runSeconds = Math.max(0, Math.floor((ended - started) / 1_000));
      const waitSeconds = Math.max(0, Math.floor((started - submitted) / 1_000));
      const project = projects[tenant].find((candidate) => candidate.id === job.project_id);
      const cluster = clusters[tenant].find((candidate) => candidate.id === job.cluster_id);
      const cpuHours = typeof job.resource_usage?.cpu_hours === "number" ? job.resource_usage.cpu_hours : 0.5 + random() * 8;
      const peakRss = typeof job.resource_usage?.max_rss_bytes === "number" ? job.resource_usage.max_rss_bytes : 1_000_000_000;
      const partition = cluster?.name === "hopper" || cluster?.name === "titan" ? "gpu" : "compute";
      records[tenant].push({
        day: new Date(ended).toISOString().slice(0, 10),
        user_id: job.created_by,
        project_id: job.project_id,
        cluster_id: job.cluster_id,
        account: `${tenant}-${project?.slug ?? "account"}`,
        partition,
        jobs: 1,
        failed: job.state === "FAILED" ? 1 : 0,
        cpu_seconds: Math.round(cpuHours * 3_600),
        gpu_seconds: partition === "gpu" ? Math.round(random() * 3_600) : 0,
        node_seconds: runSeconds,
        mem_gb_seconds: Math.round((peakRss / 1_000_000_000) * runSeconds),
        wait_seconds: [waitSeconds],
        run_seconds: [runSeconds],
      });
    }
  }
  return records;
}

/** @param {number} startedAt @returns {Record<TenantSlug, SecretConnector[]>} */
function makeConnectors(startedAt) {
  /** @type {Record<TenantSlug, SecretConnector[]>} */
  return {
    acme: [
      { id: generatedUuid(6_000), tenant_id: tenantIds.acme, name: "default", kind: "platform-openbao", state: "active", config: {}, has_credential: false, version: 1, created_at: iso(startedAt - 30 * 86_400_000), updated_at: iso(startedAt - 30 * 86_400_000) },
      { id: generatedUuid(6_001), tenant_id: tenantIds.acme, name: "research-vault", kind: "openbao", state: "active", config: { address: "https://vault.acme.example.test", namespace: "customers/acme", mount: "kv", auth: { method: "approle", role_id: "acme-research-role" } }, has_credential: true, version: 1, created_at: iso(startedAt - 14 * 86_400_000), updated_at: iso(startedAt - 14 * 86_400_000) },
    ],
    globex: [
      { id: generatedUuid(6_002), tenant_id: tenantIds.globex, name: "default", kind: "platform-openbao", state: "active", config: {}, has_credential: false, version: 1, created_at: iso(startedAt - 30 * 86_400_000), updated_at: iso(startedAt - 30 * 86_400_000) },
      { id: generatedUuid(6_003), tenant_id: tenantIds.globex, name: "globex-vault", kind: "openbao", state: "active", config: { address: "https://vault.globex.example.test", namespace: "customers/globex", mount: "kv", auth: { method: "jwt", role: "globex-custos" } }, has_credential: true, version: 1, created_at: iso(startedAt - 10 * 86_400_000), updated_at: iso(startedAt - 10 * 86_400_000) },
    ],
  };
}

/** @param {number} startedAt @param {Record<TenantSlug, SecretConnector[]>} connectors @param {Record<MockUserName, MockUser>} users @returns {Record<TenantSlug, SecretReference[]>} */
function makeReferences(startedAt, connectors, users) {
  /** @type {Record<TenantSlug, SecretReference[]>} */
  return {
    acme: [
      { id: generatedUuid(6_100), tenant_id: tenantIds.acme, owner_id: users.alice.me.user_id, project_id: projectIds.p1, name: "research-dataset", connector_id: connectors.acme[0].id, namespace: `custos/tenants/${tenantIds.acme}`, mount: "kv", path: "projects/p1/datasets", key: "read-token", secret_version: null, kind: "generic", allowed_uses: ["workflow_env"], version: 1, created_at: iso(startedAt - 8 * 86_400_000), updated_at: iso(startedAt - 8 * 86_400_000) },
      { id: generatedUuid(6_101), tenant_id: tenantIds.acme, owner_id: null, project_id: projectIds.genomics, name: "external-archive", connector_id: connectors.acme[1].id, namespace: "customers/acme", mount: "kv", path: "research/archive", key: "api-token", secret_version: 3, kind: "api_token", allowed_uses: ["workflow_env"], version: 2, created_at: iso(startedAt - 4 * 86_400_000), updated_at: iso(startedAt - 2 * 86_400_000) },
    ],
    globex: [
      { id: generatedUuid(6_102), tenant_id: tenantIds.globex, owner_id: null, project_id: projectIds.cfd, name: "cfd-storage", connector_id: connectors.globex[0].id, namespace: `custos/tenants/${tenantIds.globex}`, mount: "kv", path: "projects/cfd/storage", key: "object", secret_version: null, kind: "storage_credential", allowed_uses: ["workflow_env"], version: 1, created_at: iso(startedAt - 3 * 86_400_000), updated_at: iso(startedAt - 3 * 86_400_000) },
    ],
  };
}

/** @param {number} startedAt @param {Record<TenantSlug, Project[]>} projects @param {Record<TenantSlug, ClusterBinding[]>} bindings @returns {Record<string, Allocation[]>} */
function makeProjectAllocations(startedAt, projects, bindings) {
  /** @type {Record<string, Allocation[]>} */
  const allocations = {};
  let index = 0;
  for (const tenant of /** @type {TenantSlug[]} */ (["acme", "globex"])) {
    for (const project of projects[tenant]) {
      const binding = bindings[tenant].find((item) => item.project_id === project.id);
      if (!binding) continue;
      const budgets = tenant === "acme" && project.slug === "p1"
        ? [
            { name: "CPU budget", unit: "cpu_hours", limit: 500, consumed: 425.5, enforcement: "hard" },
            { name: "GPU budget", unit: "gpu_hours", limit: 100, consumed: 112.5, enforcement: "soft" },
          ]
        : [{ name: `${project.name} CPU`, unit: "cpu_hours", limit: 1_000, consumed: 420 + index * 30, enforcement: "hard" }];
      allocations[project.id] = budgets.map((budget) => {
        const now = iso(startedAt - 60_000);
        /** @type {Allocation} */
        const allocation = {
          id: generatedUuid(7_000 + index),
          tenant_id: tenantIds[tenant],
          project_id: project.id,
          binding_id: binding.id,
          name: budget.name,
          unit: /** @type {Allocation["unit"]} */ (budget.unit),
          limit_amount: budget.limit,
          period_start: iso(startedAt - 30 * 86_400_000),
          period_end: iso(startedAt + 30 * 86_400_000),
          enforcement: /** @type {Allocation["enforcement"]} */ (budget.enforcement),
          consumed_amount: budget.consumed,
          consumed_as_of: now,
          version: 1,
          created_at: iso(startedAt - 20 * 86_400_000),
          updated_at: now,
        };
        index += 1;
        return allocation;
      });
    }
  }
  return allocations;
}

/** @param {number} startedAt @param {Record<TenantSlug, Project[]>} projects @param {Record<string, Allocation[]>} projectAllocations @returns {Record<TenantSlug, AccountingAllocationItem[]>} */
function makeTenantAllocations(startedAt, projects, projectAllocations) {
  /** @type {Record<TenantSlug, AccountingAllocationItem[]>} */
  const result = { acme: [], globex: [] };
  for (const tenant of /** @type {TenantSlug[]} */ (["acme", "globex"])) {
    for (const project of projects[tenant]) {
      for (const allocation of projectAllocations[project.id] ?? []) {
        const consumed = allocation.consumed_amount;
        const remaining = allocation.limit_amount - consumed;
        const percent = allocation.limit_amount > 0 ? (100 * consumed) / allocation.limit_amount : 0;
        /** @type {AccountingAllocationItem} */
        const item = {
          allocation,
          consumed,
          remaining,
          percent_used: percent,
          as_of: allocation.consumed_as_of ?? iso(startedAt),
          active: startedAt >= Date.parse(allocation.period_start) && startedAt < Date.parse(allocation.period_end),
        };
        result[tenant].push(item);
      }
    }
  }
  return result;
}

/** @returns {Record<string, import("../lib/workflow/spec").CustosWorkflow>} */
function makeWorkflowDocuments() {
  const gaussian = /** @type {import("../lib/workflow/spec").CustosWorkflow} */ ({
    apiVersion: "custos.io/v1alpha1",
    kind: "Workflow",
    metadata: { name: "gaussian-simulation", labels: { domain: "chemistry" } },
    spec: {
      parameters: {
        molecule: { type: "string", required: true, pattern: "^[a-zA-Z0-9_-]{1,64}$" },
        iterations: { type: "integer", default: 100, minimum: 1, maximum: 100_000 },
        shards: { type: "integer", default: 4, minimum: 1, maximum: 256 },
      },
      placement: { cluster: "cluster-e2e" },
      defaults: { account: null, partition: "compute", qos: "normal", workingDirectory: "/scratch/{{ run.id }}", env: { OMP_NUM_THREADS: "{{ task.resources.cpu }}" } },
      secrets: {
        hfToken: { ref: "hf-token", use: "env", envName: "HF_TOKEN" },
        license: { ref: "license-key", use: "wrapped_token" },
      },
      execution: { strategy: "engine", failurePolicy: "continue" },
      tasks: [
        { name: "prepare", type: "batch", resources: { cpu: 4, memory: "8Gi", walltime: "30m" }, command: ["./prepare", "{{ parameters.molecule }}"], outputs: { shardList: { type: "file", path: "shards.json" } } },
        { name: "simulate", type: "mpi", dependsOn: ["prepare"], fanOut: { count: "{{ parameters.shards }}" }, resources: { nodes: 2, tasksPerNode: 4, memoryPerNode: "16Gi", walltime: "4h" }, command: ["./simulate", "--shard", "{{ item.index }}"], retry: { attempts: 2, on: ["FAILED", "NODE_FAIL"] } },
        { name: "merge", type: "batch", dependsOn: ["simulate"], when: "{{ tasks.simulate.succeededCount }} > 0", resources: { cpu: 4, memory: "16Gi", walltime: "1h" }, command: ["./merge"] },
        { name: "train", type: "gpu", dependsOn: ["merge"], resources: { gpu: { count: 1, type: "a100" }, cpu: 8, memory: "32Gi", walltime: "2h" }, script: { ref: digest(90_001), language: "python" }, args: ["--epochs", "{{ parameters.iterations }}"], env: { WANDB_MODE: "offline" } },
        { name: "sweep", type: "array", dependsOn: ["merge"], array: { start: 0, end: 3, maxConcurrent: 2 }, command: ["./sweep", "{{ array.taskId }}"] },
      ],
    },
  });
  const chain = /** @type {import("../lib/workflow/spec").CustosWorkflow} */ ({
    apiVersion: "custos.io/v1alpha1",
    kind: "Workflow",
    metadata: { name: "three-task-chain" },
    spec: {
      parameters: { input: { type: "string", required: true, pattern: "^[a-zA-Z0-9_./-]+$" } },
      tasks: [
        { name: "fetch", type: "batch", command: ["./fetch", "{{ parameters.input }}"], resources: { cpu: 1, memory: "2Gi", walltime: "20m" } },
        { name: "analyze", type: "batch", dependsOn: ["fetch"], command: ["./analyze"], resources: { cpu: 4, memory: "8Gi", walltime: "1h" } },
        { name: "report", type: "batch", dependsOn: ["analyze"], command: ["./report"], resources: { cpu: 1, memory: "2Gi", walltime: "20m" } },
      ],
    },
  });
  const archived = JSON.parse(JSON.stringify(chain));
  archived.metadata.name = "archived-climate-pipeline";
  const draftOnly = /** @type {import("../lib/workflow/spec").CustosWorkflow} */ ({
    apiVersion: "custos.io/v1alpha1",
    kind: "Workflow",
    metadata: { name: "draft-only-workflow" },
    spec: { parameters: {}, tasks: [{ name: "stage", type: "batch", command: ["./stage"] }] },
  });
  const globex = /** @type {import("../lib/workflow/spec").CustosWorkflow} */ ({
    apiVersion: "custos.io/v1alpha1",
    kind: "Workflow",
    metadata: { name: "cfd-postprocess" },
    spec: { parameters: {}, tasks: [{ name: "mesh", type: "batch", command: ["./mesh"] }, { name: "solve", type: "mpi", dependsOn: ["mesh"], command: ["./solve"] }, { name: "collect", type: "batch", dependsOn: ["solve"], command: ["./collect"] }] },
  });
  return { gaussian, chain, archived, draftOnly, globex };
}

/** @param {string} id @param {string} workflowId @param {number} number @param {WorkflowVersion["state"]} state @param {import("../lib/workflow/spec").CustosWorkflow} spec @param {number} startedAt @param {unknown} layout @returns {WorkflowVersionFixture} */
function workflowVersionFixture(id, workflowId, number, state, spec, startedAt, layout = {}) {
  return {
    id,
    workflowId,
    number,
    state,
    schemaVersion: "custos.io/v1alpha1",
    specHash: digest(100_000 + number + Number.parseInt(id.slice(-4), 16)),
    layout,
    spec,
    version: 1,
    createdAt: iso(startedAt),
    publishedAt: state === "draft" ? null : iso(startedAt + 3_600_000),
  };
}

/** @param {number} startedAt @param {Record<TenantSlug, Project[]>} projects @returns {{workflows:Record<TenantSlug,Workflow[]>,versions:Record<string,WorkflowVersionFixture[]>}} */
function makeWorkflowFixtures(startedAt, projects) {
  const documents = makeWorkflowDocuments();
  const gaussianLayout = { nodes: { prepare: { x: 40, y: 120 }, simulate: { x: 330, y: 120 }, merge: { x: 620, y: 120 }, train: { x: 910, y: 40 }, sweep: { x: 910, y: 220 } } };
  const versions = {
    [workflowIds.gaussian]: [
      workflowVersionFixture(workflowVersionIds.gaussianDraft, workflowIds.gaussian, 3, "draft", documents.gaussian, startedAt - 86_400_000, {}),
      workflowVersionFixture(workflowVersionIds.gaussianPublished, workflowIds.gaussian, 2, "published", documents.gaussian, startedAt - 7 * 86_400_000, gaussianLayout),
      workflowVersionFixture(workflowVersionIds.gaussianDeprecated, workflowIds.gaussian, 1, "deprecated", documents.gaussian, startedAt - 30 * 86_400_000, {}),
    ],
    [workflowIds.chain]: [
      workflowVersionFixture(workflowVersionIds.chainDraft, workflowIds.chain, 2, "draft", documents.chain, startedAt - 3_600_000, {}),
      workflowVersionFixture(workflowVersionIds.chainPublished, workflowIds.chain, 1, "published", documents.chain, startedAt - 12 * 86_400_000, {}),
    ],
    [workflowIds.archived]: [
      workflowVersionFixture(workflowVersionIds.archivedPublished, workflowIds.archived, 1, "published", documents.archived, startedAt - 20 * 86_400_000, {}),
    ],
    [workflowIds.draftOnly]: [
      workflowVersionFixture(workflowVersionIds.draftOnly, workflowIds.draftOnly, 1, "draft", documents.draftOnly, startedAt - 2 * 86_400_000, {}),
    ],
    [workflowIds.globexPipeline]: [
      workflowVersionFixture(workflowVersionIds.globexPublished, workflowIds.globexPipeline, 1, "published", documents.globex, startedAt - 5 * 86_400_000, {}),
    ],
  };
  /** @type {Record<TenantSlug, Workflow[]>} */
  const workflows = {
    acme: [
      { id: workflowIds.gaussian, tenantId: tenantIds.acme, projectId: projectIds.p1, name: "Gaussian simulation", description: "Fan-out MPI simulation with GPU training.", state: "active", latestPublishedVersionId: workflowVersionIds.gaussianPublished, version: 3, createdAt: iso(startedAt - 30 * 86_400_000), updatedAt: iso(startedAt - 86_400_000) },
      { id: workflowIds.chain, tenantId: tenantIds.acme, projectId: projectIds.genomics, name: "Three-task chain", description: "Fetch, analyze, and report.", state: "active", latestPublishedVersionId: workflowVersionIds.chainPublished, version: 2, createdAt: iso(startedAt - 18 * 86_400_000), updatedAt: iso(startedAt - 3_600_000) },
      { id: workflowIds.archived, tenantId: tenantIds.acme, projectId: projectIds.climate, name: "Archived climate pipeline", description: "A historical workflow.", state: "archived", latestPublishedVersionId: workflowVersionIds.archivedPublished, version: 1, createdAt: iso(startedAt - 40 * 86_400_000), updatedAt: iso(startedAt - 10 * 86_400_000) },
      { id: workflowIds.draftOnly, tenantId: tenantIds.acme, projectId: projectIds.p1, name: "Draft-only workflow", description: "No published version yet.", state: "active", latestPublishedVersionId: null, version: 1, createdAt: iso(startedAt - 4 * 86_400_000), updatedAt: iso(startedAt - 2 * 86_400_000) },
    ],
    globex: [
      { id: workflowIds.globexPipeline, tenantId: tenantIds.globex, projectId: projectIds.cfd, name: "CFD postprocess", description: "Three-step post-processing chain.", state: "active", latestPublishedVersionId: workflowVersionIds.globexPublished, version: 1, createdAt: iso(startedAt - 9 * 86_400_000), updatedAt: iso(startedAt - 5 * 86_400_000) },
    ],
  };
  for (const tenant of /** @type {TenantSlug[]} */ (["acme", "globex"])) {
    for (const workflow of workflows[tenant]) {
      if (!projects[tenant].some((project) => project.id === workflow.projectId)) throw new Error("workflow fixture project is missing");
    }
  }
  return { workflows, versions };
}

/** @param {number} startedAt @param {Record<MockUserName, MockUser>} users @param {Record<TenantSlug, Workflow[]>} workflows @param {Record<string, WorkflowVersionFixture[]>} versions @param {Record<TenantSlug, Job[]>} jobs @returns {{executions:Record<TenantSlug,WorkflowExecutionFixture[]>,tasks:Record<string,TaskExecution[]>,frozen:Map<string,unknown>}} */
function makeWorkflowExecutionFixtures(startedAt, users, workflows, versions, jobs) {
  /** @type {Record<TenantSlug, WorkflowExecutionFixture[]>} */
  const executions = { acme: [], globex: [] };
  /** @type {Record<string, TaskExecution[]>} */
  const tasksByExecution = {};
  const frozenTaskSpecs = new Map();
  let executionNumber = 12_000;
  let taskNumber = 13_000;
  const definitions = [
    { tenant: "acme", workflow: workflowIds.gaussian, version: workflowVersionIds.gaussianPublished, state: "RUNNING", owner: "alice", age: 180, mode: "fanout" },
    { tenant: "acme", workflow: workflowIds.chain, version: workflowVersionIds.chainPublished, state: "PENDING", owner: "alice", age: 4 },
    { tenant: "acme", workflow: workflowIds.chain, version: workflowVersionIds.chainPublished, state: "VALIDATING", owner: "admin", age: 5 },
    { tenant: "acme", workflow: workflowIds.gaussian, version: workflowVersionIds.gaussianPublished, state: "QUEUED", owner: "alice", age: 15 },
    { tenant: "acme", workflow: workflowIds.chain, version: workflowVersionIds.chainPublished, state: "SUCCEEDED", owner: "admin", age: 60 },
    { tenant: "acme", workflow: workflowIds.gaussian, version: workflowVersionIds.gaussianPublished, state: "FAILED", owner: "alice", age: 120 },
    { tenant: "acme", workflow: workflowIds.gaussian, version: workflowVersionIds.gaussianPublished, state: "PARTIAL_FAILURE", owner: "alice", age: 240 },
    { tenant: "acme", workflow: workflowIds.chain, version: workflowVersionIds.chainPublished, state: "CANCELING", owner: "admin", age: 30 },
    { tenant: "acme", workflow: workflowIds.chain, version: workflowVersionIds.chainPublished, state: "CANCELED", owner: "alice", age: 90 },
    { tenant: "globex", workflow: workflowIds.globexPipeline, version: workflowVersionIds.globexPublished, state: "SUCCEEDED", owner: "admin", age: 45 },
  ];
  const activeStates = new Set(["QUEUED", "RUNNING", "CANCELING"]);
  const endedStates = new Set(["SUCCEEDED", "FAILED", "PARTIAL_FAILURE", "CANCELED"]);

  for (const definition of definitions) {
    const tenant = /** @type {TenantSlug} */ (definition.tenant);
    const workflow = workflows[tenant].find((item) => item.id === definition.workflow);
    const version = versions[definition.workflow]?.find((item) => item.id === definition.version);
    if (!workflow || !version) continue;
    const id = generatedUuid(executionNumber++);
    const createdAt = startedAt - definition.age * 60_000;
    const started = activeStates.has(definition.state) || endedStates.has(definition.state);
    const ended = endedStates.has(definition.state);
    const executionUpdatedAt = iso(createdAt + (ended ? 90_000 : started ? 45_000 : 1_000));
    const parameters = definition.workflow === workflowIds.gaussian
      ? { molecule: "benzene", iterations: 120, shards: 4 }
      : definition.workflow === workflowIds.chain ? { input: "samples/cohort-a" } : {};
    /** @type {WorkflowExecutionFixture} */
    const execution = {
      id,
      tenantId: tenantIds[tenant],
      projectId: workflow.projectId,
      workflowId: workflow.id,
      workflowVersionId: version.id,
      specHash: version.specHash,
      parameters,
      strategy: version.spec.spec.execution?.strategy ?? "engine",
      state: /** @type {WorkflowExecution["state"]} */ (definition.state),
      stateReason: definition.state === "FAILED" ? "A task failed after all retry attempts." : definition.state === "PARTIAL_FAILURE" ? "Execution continued with one failed task." : definition.state === "CANCELED" ? "Canceled by requester." : "",
      requestedBy: users[/** @type {MockUserName} */ (definition.owner)].me.user_id,
      createdAt: iso(createdAt),
      startedAt: started ? iso(createdAt + 10_000) : null,
      endedAt: ended ? executionUpdatedAt : null,
      updatedAt: executionUpdatedAt,
      version: 2,
    };
    executions[tenant].push(execution);

    /** @type {TaskExecution[]} */
    const taskRows = [];
    let rowIndex = 0;
    const projectJobs = jobs[tenant].filter((job) => job.project_id === workflow.projectId);
    const jobPool = projectJobs.length ? projectJobs : jobs[tenant];
    /** @param {import("../lib/workflow/spec").Task} task @param {number} index @param {number} count @param {number} attempt @param {string} state @param {string} [reason] */
    const addTask = (task, index, count, attempt, state, reason = "") => {
      const taskId = generatedUuid(taskNumber++);
      const jobId = ["SUBMITTING", "QUEUED", "RUNNING", "COMPLETED", "FAILED"].includes(state) && jobPool.length > 0
        ? jobPool[(executionNumber + rowIndex) % jobPool.length].id
        : null;
      const digestValue = jobId ? digest(taskNumber + rowIndex) : undefined;
      const taskUpdatedAt = executionUpdatedAt;
      /** @type {TaskExecution} */
      const row = {
        id: taskId,
        executionId: id,
        taskName: task.name,
        index,
        count,
        attempt,
        state: /** @type {TaskExecution["state"]} */ (state),
        stateReason: reason,
        jobId,
        validationId: null,
        ...(digestValue ? { executionSpecDigest: digestValue } : {}),
        createdAt: iso(createdAt + rowIndex * 1_000),
        updatedAt: taskUpdatedAt,
        version: attempt,
      };
      taskRows.push(row);
      if (jobId && digestValue) {
        frozenTaskSpecs.set(taskId, {
          schema_version: 1,
          id: taskId,
          tenant_id: execution.tenantId,
          project_id: execution.projectId,
          principal_id: execution.requestedBy,
          workflow_version_id: execution.workflowVersionId,
          task_name: task.name,
          attempt,
          cluster: { id: clusterIds[tenant === "acme" ? "cluster-e2e" : "titan"], name: tenant === "acme" ? "cluster-e2e" : "titan", api_version: "slurm.v0_0_42" },
          account: `${tenant}-account`,
          partition: task.partition ?? "compute",
          qos: task.qos ?? "normal",
          resources: { nodes: task.resources?.nodes ?? 1, tasks: task.resources?.tasks ?? 1, tasks_per_node: task.resources?.tasksPerNode ?? 1, cpus_per_task: task.resources?.cpusPerTask ?? task.resources?.cpu ?? 1, walltime_seconds: 3_600 },
          placement: { cluster: tenant === "acme" ? "cluster-e2e" : "titan", reason: "mock fixture" },
          environment: { user: task.env ?? {}, controlled: {}, runtime: {}, secret_refs: [] },
          payload: { script_id: generatedUuid(taskNumber + 20_000), digest: digestValue, language: task.script?.language ?? "bash", interpreter: "/bin/bash" },
          argv: (task.command ?? []).map((literal) => ({ literal })),
          inputs: [],
          outputs: [],
          working_dir: task.workingDirectory ?? "/scratch/mock-execution",
          stdout: task.stdout ?? "",
          stderr: task.stderr ?? "",
          security: { slurm_user: "alice", impersonation_mode: "service", shell_task: false, wrapped_token_refs: [] },
          admission: { estimated_cost: { cpu_hours: 1 }, warnings: [] },
          digest: digestValue,
          admitted_at: taskUpdatedAt,
          admitted_by: "mock-worker",
        });
      }
      rowIndex += 1;
    };

    if (definition.mode === "fanout") {
      for (const task of version.spec.spec.tasks) {
        if (task.name === "simulate") {
          const taskStates = ["COMPLETED", "COMPLETED", "RUNNING", "QUEUED"];
          taskStates.forEach((state, index) => addTask(task, index, 4, index === 0 ? 2 : 1, state, index === 0 ? "Attempt 2 completed after attempt 1 FAILED." : ""));
        } else if (task.name === "sweep") {
          for (let index = 0; index < 4; index += 1) addTask(task, index, 4, 1, "PENDING");
        } else {
          const state = task.name === "prepare" ? "COMPLETED" : task.name === "merge" ? "BLOCKED" : "PENDING";
          addTask(task, 0, 1, 1, state);
        }
      }
    } else if (definition.state === "PENDING" || definition.state === "VALIDATING") {
      if (definition.state === "VALIDATING") {
        version.spec.spec.tasks.forEach((task, index) => addTask(task, 0, 1, 1, index === 0 ? "ADMITTING" : "BLOCKED"));
      }
    } else {
      version.spec.spec.tasks.forEach((task, index) => {
        let state = definition.state === "SUCCEEDED" ? "COMPLETED"
          : definition.state === "FAILED" ? index === 0 ? "FAILED" : "SKIPPED"
            : definition.state === "PARTIAL_FAILURE" ? index === 0 ? "COMPLETED" : index === 1 ? "FAILED" : "SKIPPED"
              : definition.state === "CANCELING" ? index === 0 ? "RUNNING" : "CANCELED"
                : definition.state === "CANCELED" ? "CANCELED"
                  : definition.state === "QUEUED" ? index === 0 ? "SUBMITTING" : "BLOCKED"
                    : "PENDING";
        const count = task.fanOut ? Number(parameters.shards ?? 1) : task.array ? 4 : 1;
        for (let taskIndex = 0; taskIndex < count; taskIndex += 1) {
          const instanceState = task.name === "simulate" && definition.state === "PARTIAL_FAILURE" && taskIndex === 1 ? "FAILED" : state;
          addTask(task, taskIndex, count, task.name === "simulate" && taskIndex === 0 && definition.state === "SUCCEEDED" ? 2 : 1, instanceState, instanceState === "FAILED" ? "Retry attempt failed." : "");
        }
      });
    }
    tasksByExecution[id] = taskRows;
  }
  for (const tenant of /** @type {TenantSlug[]} */ (["acme", "globex"])) {
    executions[tenant].sort((left, right) => (right.createdAt ?? "").localeCompare(left.createdAt ?? ""));
  }
  return { executions, tasks: tasksByExecution, frozen: frozenTaskSpecs };
}

/** @param {number} startedAt @param {string} issuer @param {number} seed @returns {MockData} */
export function createMockData(startedAt = Date.now(), issuer = "http://127.0.0.1:4300/realms/custos", seed = 20260927) {
  const users = createMockUsers(issuer);
  const projects = makeProjects(startedAt);
  const clusters = makeClusters();
  const jobs = makeJobs(startedAt, users, seed);
  const projectMembers = makeProjectMembers(startedAt, users);
  const clusterBindings = makeClusterBindings(startedAt, projects, clusters);
  const partitions = makePartitions(startedAt, clusters);
  const connectors = makeConnectors(startedAt);
  const references = makeReferences(startedAt, connectors, users);
  const projectAllocations = makeProjectAllocations(startedAt, projects, clusterBindings);
  const tenantAllocations = makeTenantAllocations(startedAt, projects, projectAllocations);
  const usageRecords = makeUsageRecords(jobs, projects, clusters, seed);
  const workflowData = makeWorkflowFixtures(startedAt, projects);
  const executionData = makeWorkflowExecutionFixtures(startedAt, users, workflowData.workflows, workflowData.versions, jobs);
  /** @type {Map<string, Record<string, never>>} */
  const executionSpecs = new Map();
  for (const tenantJobs of Object.values(jobs)) {
    for (const job of tenantJobs) executionSpecs.set(job.id, {});
  }
  return {
    users, projects, projectMembers, clusters, clusterBindings, partitions, connectors, references,
    projectAllocations, tenantAllocations, workflows: workflowData.workflows, workflowVersions: workflowData.versions,
    workflowExecutions: executionData.executions, taskExecutions: executionData.tasks, frozenTaskSpecs: executionData.frozen,
    idempotency: new Map(), jobs, usageRecords, executionSpecs,
  };
}
