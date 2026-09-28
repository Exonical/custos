// @ts-check

/** @typedef {import("../lib/api/schema").components["schemas"]["Me"]} Me */
/** @typedef {import("../lib/api/schema").components["schemas"]["Project"]} Project */
/** @typedef {import("../lib/api/schema").components["schemas"]["ClusterSummary"]} ClusterSummary */
/** @typedef {import("../lib/api/schema").components["schemas"]["Job"]} Job */
/** @typedef {import("../lib/api/schema").components["schemas"]["MembershipRef"]} MembershipRef */
/** @typedef {import("../lib/api/schema").components["schemas"]["ProjectMembershipRef"]} ProjectMembershipRef */
/** @typedef {"alice" | "admin" | "bob"} MockUserName */
/** @typedef {"acme" | "globex"} TenantSlug */
/** @typedef {"p1" | "genomics" | "climate" | "cfd"} ProjectSlug */
/** @typedef {"cluster-e2e" | "hopper" | "titan"} ClusterName */
/** @typedef {{ sub: string, name: string, email: string, me: Me }} MockUser */
/** @typedef {{ users: Record<MockUserName, MockUser>, projects: Record<TenantSlug, Project[]>, clusters: Record<TenantSlug, ClusterSummary[]>, jobs: Record<TenantSlug, Job[]>, executionSpecs: Map<string, Record<string, never>> }} MockData */

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

/** @param {number} startedAt @param {string} issuer @param {number} seed @returns {MockData} */
export function createMockData(startedAt = Date.now(), issuer = "http://127.0.0.1:4300/realms/custos", seed = 20260927) {
  const users = createMockUsers(issuer);
  const projects = makeProjects(startedAt);
  const clusters = makeClusters();
  const jobs = makeJobs(startedAt, users, seed);
  /** @type {Map<string, Record<string, never>>} */
  const executionSpecs = new Map();
  for (const tenantJobs of Object.values(jobs)) {
    for (const job of tenantJobs) executionSpecs.set(job.id, {});
  }
  return { users, projects, clusters, jobs, executionSpecs };
}
