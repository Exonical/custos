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
/** @typedef {import("../lib/api/schema").components["schemas"]["MembershipRef"]} MembershipRef */
/** @typedef {import("../lib/api/schema").components["schemas"]["ProjectMembershipRef"]} ProjectMembershipRef */
/** @typedef {"alice" | "admin" | "bob"} MockUserName */
/** @typedef {"acme" | "globex"} TenantSlug */
/** @typedef {"p1" | "genomics" | "climate" | "cfd"} ProjectSlug */
/** @typedef {"cluster-e2e" | "hopper" | "titan"} ClusterName */
/** @typedef {{ sub: string, name: string, email: string, me: Me }} MockUser */
/** @typedef {{ users: Record<MockUserName, MockUser>, projects: Record<TenantSlug, Project[]>, projectMembers: Record<string, ProjectMembership[]>, clusters: Record<TenantSlug, ClusterSummary[]>, clusterBindings: Record<TenantSlug, ClusterBinding[]>, partitions: Record<string, PartitionRecord[]>, connectors: Record<TenantSlug, SecretConnector[]>, references: Record<TenantSlug, SecretReference[]>, projectAllocations: Record<string, Allocation[]>, tenantAllocations: Record<TenantSlug, AccountingAllocationItem[]>, jobs: Record<TenantSlug, Job[]>, usageRecords: Record<TenantSlug, UsageDailyRecord[]>, executionSpecs: Map<string, Record<string, never>> }} MockData */

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
  /** @type {Map<string, Record<string, never>>} */
  const executionSpecs = new Map();
  for (const tenantJobs of Object.values(jobs)) {
    for (const job of tenantJobs) executionSpecs.set(job.id, {});
  }
  return { users, projects, projectMembers, clusters, clusterBindings, partitions, connectors, references, projectAllocations, tenantAllocations, jobs, usageRecords, executionSpecs };
}
