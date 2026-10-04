# Slurm Integration

## Facts the design depends on (verified against SlinkyProject/slurm-client, main branch)

- Module `github.com/SlinkyProject/slurm-client`, Apache-2.0, maintained by SchedMD.
- Two layers:
  1. `pkg/client` — controller-runtime-style wrapper with a cache/informer
     layer. **Only Node and Job (plus ping/partition/stats in some versions)
     objects are implemented.**
  2. `api/v0042`, `api/v0043`, `api/v0044`, `api/v0045` — fully generated
     OpenAPI clients for slurmrestd, including `slurmdb` endpoints
     (accounting jobs, associations, accounts, QoS, TRES).
- Requires Slurm >= 24.05. Auth via `TokenProvider` (static or file);
  we supply our own provider backed by OpenBao.
- 0.x: source tree "may evolve aggressively". This is the main reason for
  our own abstraction.

## Supported Slurm versions

Baseline is the **latest Slurm release: 26.05** (current patch 26.05.4,
data_parser/slurmrestd API `v0.0.45`, which drops fields deprecated in
v0.0.44). Support matrix:

| Slurm | slurmrestd API | Custos adapter | Status |
| --- | --- | --- | --- |
| 26.05 | v0.0.45 | `internal/slurm/slinky/v0045` | primary; all features, CI conformance target |
| 25.11 | v0.0.44 | `internal/slurm/slinky/v0044` | supported (SchedMD still maintains it) |
| 25.05 and older | v0.0.43 and older | none | not supported; sites must upgrade |

SchedMD maintains the current release plus the previous one for security
fixes; Custos tracks the same window and drops an adapter one release
after SchedMD drops the Slurm version. `sbatchscan`'s option table
(`docs/script-validation.md`) is generated for 26.05 and verified against
a 25.11 fixture.

## Port owned by Custos

`internal/slurm` defines neutral types and a small interface. Nothing outside
`internal/slurm/slinky` imports Slinky or generated types.

```go
package slurm

type Cluster interface {
    Ping(ctx context.Context) (PingResult, error)
    Capabilities(ctx context.Context) (Capabilities, error)   // version, partitions, qos, gres types, features

    SubmitJob(ctx context.Context, req JobSubmission) (JobRef, error)
    GetJob(ctx context.Context, id JobID) (Job, error)
    ListJobs(ctx context.Context, f JobFilter) ([]Job, error)  // filter: names, users, states, since
    CancelJob(ctx context.Context, id JobID, opts CancelOptions) error

    GetNodes(ctx context.Context) ([]Node, error)
    GetPartitions(ctx context.Context) ([]Partition, error)
    GetReservations(ctx context.Context) ([]Reservation, error)
}

// Accounting is separate: it targets slurmdbd, may be disabled, and has
// different failure modes and latency.
type Accounting interface {
    GetAssociations(ctx context.Context, f AssociationFilter) ([]Association, error)
    GetAccounts(ctx context.Context) ([]Account, error)
    GetQoS(ctx context.Context) ([]QoS, error)
    GetJobRecords(ctx context.Context, f JobRecordFilter) ([]JobRecord, error) // sacct-like, windowed
}

type AccountingAdmin interface { // optional; type-asserted by policy.sync
    UpsertAccounts(ctx context.Context, accounts []Account) error
    UpsertAssociations(ctx context.Context, associations []Association) error
    DeleteAssociation(ctx context.Context, key AssociationKey) error
    DeleteAccount(ctx context.Context, name string) error
}

type Factory interface {
    // Open builds clients for a registered cluster; credentials are resolved
    // through secrets.Resolver at call time, never stored on the struct.
    Open(ctx context.Context, c clusters.Cluster) (Cluster, Accounting, error)
}
```

Splitting `Cluster` and `Accounting` deviates from the single-interface
sketch in the brief because the two talk to different daemons
(slurmctld vs slurmdbd), fail independently, and accounting is optional on
some sites.

`policy.sync` uses `Accounting` for reads and type-asserts the optional
`AccountingAdmin` only in `enforce` mode. It reconciles binding accounts and
the dedicated service-user associations; other users' associations on those
accounts are never modified. `report` mode is the write-free rollout escape
hatch. See ADR-020. `policy.sync` never creates QoS or partitions. It creates
missing accounts, gives each binding's configured service user exactly the
allowed partitions/QoS, removes extra associations for that user on the bound
account, and applies hard limits to the account-level association. Slurm's
`IsDefault` service-user association grants all partitions and cannot be
removed; Custos retains it and reports `DEFAULT_ASSOCIATION_RETAINED`. Sites
that need native partition restriction must assign the service user a different
default account. Objects created by Custos are recorded for safe unbinding
cleanup; a created account is deleted only after a fresh read confirms it has
no associations.

Service-user `Association`s carry the exact binding QoS list and default QoS.
For account-level associations, QoS/default QoS are site-owned, preserved during
planning, and omitted from writes; only `GrpTRESMins` is binding-derived. Parent
and comment are likewise preserved/omitted for site-created associations.
Unused supported TRES are cleared with count `-1`; the slurmdbd `v0.0.45`
adapter maps this to `max.tres.group.minutes`. For an enabled
binding, enforce mode clears a supported unit's `GrpTRESMins` even if a site
administrator set it when no hard allocation is active. For a site-created
association, parent/comment are preserved and omitted from the upsert payload.
The worker pushes `ceil(hard_allocation_hours × 60)` for the minimum active hard
budget per unit and omits TRES unsupported by the cluster. Slurm counts usage within
its `UsageResetPeriod`/decay window rather than from the allocation's
`period_start`, so this native backstop may deny earlier than Custos admission
(ADR-020).

### Policy-management modes

Global mode is configured as `slurm.policy_management: enforce|report` and
defaults to `enforce`:

```yaml
slurm:
  policy_management: enforce
```

Each platform-owned cluster has a `policy_management` override (`inherit` by
default) and a `policy_parent_account` (`root` by default), editable only with
`cluster.manage`. Effective mode is the cluster override or the global setting
when inherited. `GET /clusters/{cluster}/policy-sync/plan` returns deterministic
dry-run operations in either mode; enforce applies at most 200 operations per
run, then re-reads slurmdbd before clearing drift. The policy writer is
implemented for the v0.0.45 adapter; v0.0.44 clusters should use report mode
until the writer is ported there.

### slurmdbd write privilege

The configured `Cluster.ServiceUser` identity is the caller for the
`AccountingAdmin` writes. On the live Slurm **26.05.4** e2e stack,
`AdminLevel=Operator` is sufficient for account and association writes,
including `GrpTRESMins`, and the corresponding cleanup. `scripts/e2e.sh`
grants it with `sacctmgr -i modify user name=custos set AdminLevel=Operator`.
Custos does not elevate user identity itself. Restrict the Slurm REST network
path to Custos and use `policy_management: report` when writes must be
suspended.

### Neutral types (excerpt)

```go
type JobSubmission struct {
    Name            string            // Custos-controlled: "custos-<job-uuid>"
    Account         string            // from ProjectClusterBinding
    Partition       string
    QoS             string
    Reservation     string
    Script          string            // generated by Custos, see below
    Argv            []string          // for srun-style tasks; never shell-joined
    WorkingDir      string
    Environment     map[string]string
    Stdout, Stderr  string
    Nodes, Tasks, TasksPerNode, CPUsPerTask int
    MemoryPerNodeMiB, MemoryPerCPUMiB int64
    GRES            []GRESRequest     // {Name:"gpu", Type:"h100", Count:8}
    Constraints     string            // validated feature expression
    Licenses        []string
    Walltime        time.Duration
    Array           *ArraySpec        // {Start,End,Step,MaxConcurrent}
    Dependencies    []Dependency      // {Kind: after|afterok|afternotok|afterany|singleton, JobIDs}
    Nice            *int
    Comment         string            // "custos:<execution-id>/<task-id>" for reconciliation
    UserName        string            // slurmrestd X-SLURM-USER-NAME when acting on behalf
}

type JobID struct{ ID uint32; ArrayTaskID *uint32; HetComponent *uint32 }

type Job struct {
    ID JobID; Name, Account, Partition, QoS, UserName string
    State JobState; StateReason string; ExitCode *ExitCode
    SubmitTime, EligibleTime, StartTime, EndTime time.Time
    Nodes int; CPUs int; TRESAlloc map[string]int64; NodeList string
    Comment string
}
```

## Adapter: `internal/slurm/slinky`

- One sub-package per supported slurmrestd version: `v0045` (primary) and
  `v0044` (see support matrix above). Each implements the
  port using the generated client for that version. The generated wrapper
  cache is **not** used (Custos owns its own persistence and reconciliation;
  a second cache with different staleness rules would confuse state).
- `Factory.Open` picks the implementation from `cluster.api_version`
  (discovered at registration via `/openapi/v3` or configured explicitly).
- Version mapping code is mechanical and boring on purpose; a contract test
  suite (`internal/slurm/conformance`) runs the **same tests** against every
  adapter version and against the in-memory fake, with recorded slurmrestd
  JSON fixtures captured from real clusters. The `v0045` fixtures under
  `internal/slurm/slinky/v0045/testdata/` are recorded from the e2e
  stack's live Slurm **26.05.4** cluster (see `docs/e2e.md`).

### Authentication to slurmrestd

- `auth/jwt` with tokens from OpenBao (`X-SLURM-USER-TOKEN`) plus
  `X-SLURM-USER-NAME`.
- Two operating modes, chosen per cluster:
  1. **Service identity** (default): Custos submits as a dedicated Slurm user
     (e.g. `custos`) with `SlurmUser`-adjacent privileges only where needed.
     Jobs run as that user unless the site enables user impersonation.
     Simplest, but jobs share a Unix identity — acceptable for
     container/sandboxed sites, not for classic shared filesystems.
  2. **Per-user impersonation**: Custos holds a signing key (`slurm.key`) in
     OpenBao and mints short-lived JWTs (`sun` claim = target Slurm user).
     Requires mapping Custos user → Slurm username in
     `ProjectClusterBinding`/tenant settings. This is the mode for classic
     HPC centers.
  Both are supported by the port (`UserName`); the choice is cluster config.
  **Decision:** service identity is the default; impersonation is a
  per-cluster opt-in (`identity_mode`), implemented in M4 — until then
  `identity_mode: impersonate` is rejected at `Factory.Open`
  (`slurm.identity_mode_unsupported`).
- TLS: server CA pinning per cluster (`tls.ca_bundle_ref`), optional mTLS
  client cert from OpenBao. `InsecureSkipVerify` is not a config option.
- Credentials resolve through `secrets.Resolver` at `Factory.Open` time,
  never stored on the client struct. Until OpenBao lands (M6), the
  interim `file` provider serves tokens/certs from allow-listed paths —
  see `docs/secrets.md`.

### SSRF protection

Cluster endpoints are registered by platform admins only, validated as
`https://host[:port]` with a host allow-list/deny-list (no link-local, no
metadata IPs, no RFC1918 unless explicitly enabled per deployment), and
resolved DNS is re-checked in a custom `DialContext`.

## Job description generation

Custos never executes user strings through a shell on the control plane,
and never encodes scheduler settings as `#SBATCH` text. `JobSubmission` is
a pure function of the admitted `ExecutionSpec` (`docs/script-validation.md`):

- every scheduler field (account, partition, QoS, resources, walltime,
  dependencies, array, environment, paths) is sent as a structured
  `JobDescMsg` field through the generated client — the adapter's job is a
  typed field-to-field mapping;
- the `script` field is the Custos-generated **wrapper**, which contains no
  `#SBATCH` lines and embeds the user payload as base64 with a runtime
  SHA-256 check, then executes it as a file (`exec <interpreter> payload
  args…`); argv elements are single-quoted through one escaping function;
- for `type: shell` tasks the payload is the same wrapper-executed file;
  the difference is only what validation permits inside it. The task is
  flagged `privileged: shell`, gated by `workflow.publish` plus
  `allowShellTasks` in the effective ValidationPolicy (see
  `docs/script-validation.md`), and is shown as such in the UI.

Job and accounting operations are implemented for `v0.0.45`
(`internal/slurm/slinky/v0045`). `GetJobRecords` maps slurmdb job timestamps,
allocated TRES, folded step consumed-TRES totals, node count, state/exit code,
and step count onto the neutral immutable accounting record.

Job operations:
`SubmitJob` maps `JobSubmission` onto `V0045JobDescMsg` strictly per the
allow-list below (a reflection test asserts no other field is ever set —
mail, `user_id`/`group_id`, and any environment-inheritance fields are
never emitted). `GetJob`/`ListJobs`/`CancelJob` map `V0045JobInfo` back
to the neutral `slurm.Job`. `GET /jobs` has no server-side name filter,
so `ListJobs` fetches the queue and filters `Names`/`Users`/`States`/
`Since` client-side — bounded by queue size; `jobs.sweep` relies on this.

Slurm option allow-list (v1) — the only `JobDescMsg` fields the adapter
ever sets: account, partition, qos, reservation, nodes, tasks,
tasks_per_node, cpus_per_task, memory_per_node/memory_per_cpu, tres_per_node
(gres), constraints, licenses, time_limit, current_working_directory,
standard_output, standard_error, environment (explicit, no inheritance),
dependency, array, nice, name, comment. Mail is never set. Anything the
spec cannot express cannot reach Slurm.
Workflow `resources.memoryPerCpu` maps to `memory_per_cpu`; it is mutually
exclusive with `memory` and `memoryPerNode`.

Service-task edges use Slurm's native `after:<job-id>` dependency: the
dependent allocation becomes eligible once the service job starts, rather
than waiting for it to complete. Other workflow edges remain engine-driven;
Custos still applies `onDependencyFailure` before submitting the dependent.

## Per-cluster container runtime

The platform cluster API stores nullable `container_runtime` JSONB. `null`
means the cluster accepts no image tasks. A configured object selects
`type: apptainer|pyxis`, an optional `allowed_image_prefixes` list, optional
`require_digest`, `slurm_in_container`, and `mpi_plugin` (default `pmix`);
`binary` is Apptainer-only and defaults to `apptainer`. An omitted or empty
prefix list permits any otherwise-valid image URI; a nonempty list is matched
against the original URI before runtime conversion. Digest pinning is
recommended for production images.

```json
{
  "type": "apptainer",
  "binary": "apptainer",
  "allowed_image_prefixes": ["oras://docker.io/anderbubble/"],
  "require_digest": true,
  "slurm_in_container": true,
  "mpi_plugin": "pmix"
}
```

Apptainer receives the URI unchanged and uses `exec --no-eval`, which
requires Apptainer 1.1 or later. Pyxis accepts `docker://` references and
absolute paths (not `oras://`); the scheme is stripped, and
`docker://registry.host/path:tag` becomes `registry.host#path:tag` when the
first component looks like a registry host (`docker://ubuntu:22.04` becomes
`ubuntu:22.04`). Both runtimes mount only `$CUSTOS_JOB_DIR`; site bind paths
are not part of the cluster configuration. User `KEY=VALUE` settings are
passed through `/usr/bin/env` inside either image, so images must contain
`/usr/bin/env`. Pyxis `--container-env` carries only runtime, secret, and
`MULTINODE_*` names whose host-provided values must override image defaults;
user env is never exported on the host. Image tasks are denied when no
runtime is configured or a cluster's prefix/digest policy is not met.

Image pull secrets may use `docker://` with either runtime; Apptainer also
accepts `oras://`. Apptainer pulls with `--disable-cache` to a local
`$CUSTOS_JOB_DIR/image.sif` and scopes `APPTAINER_DOCKER_USERNAME` /
`APPTAINER_DOCKER_PASSWORD` to that pull command. Pyxis uses `enroot import`
with a temporary credentials file outside the mounted job directory, then
launches the local `$CUSTOS_JOB_DIR/image.sqsh`. The `enroot` CLI must be
available on compute nodes for Pyxis pull-secret tasks. In both cases the
pull credentials are removed from the wrapper environment before the user
payload starts. Pull-secret tasks must be single-node; arrays are supported.

OpenMPI/MPICH multinode tasks use `srun --mpi=<mpi_plugin>` and the resolved
per-rank task shape. Generic multinode tasks run the command once, export the
Fuzzball-compatible `MULTINODE_*` variables, and use a generated
`srun --overlap` remote-launch wrapper. A generic image requires
`slurm_in_container: true` and a compatible Slurm client in the image. The
wrapper distributes payloads with `sbcast` for srun-launched tasks because
`$CUSTOS_JOB_DIR` may be node-local. CPU affinity maps to `--cpu-bind`; for
batch scripts without a final srun, the wrapper exports `SLURM_CPU_BIND` so
nested steps inherit it. Multinode `resources.cpu` and memory are per node.
Portable sbatch export rejects image and multinode tasks because runtime
configuration is cluster-specific.

## Cluster synchronization

Work item `cluster.sync` is a **self-rescheduling chain** per cluster: each
run re-enqueues itself with `run_at = now + interval` where the interval is
`worker.cluster_sync_interval` (default 60s) ±10% jitter. `custos worker`
bootstraps one item per non-disabled cluster at startup; dedupe on
`(kind, key)` keeps the chain single. Slurm errors never return an error to
the queue — they are recorded and the *next scheduled run* is the retry.

Each run does `Ping` → `Capabilities` (which already contains partitions —
no extra partition call) → `RecordSyncResult`: one transaction updates
`clusters.capabilities` JSONB + `last_sync_at` + the hysteresis counters
(`consecutive_failures` / `consecutive_successes`, `last_error` truncated
to 1024 chars and token-scrubbed), and replaces `cluster_partitions` rows
atomically. State: 2 consecutive successes → `active`; a failure →
`degraded`; 3 consecutive failures → `unreachable`; `disabled` never
changes (and stops the chain — no re-enqueue). While `unreachable` the
re-enqueue interval is 5× the base, capped at 10 min.

For `visibility = all_tenants` clusters a successful run also reconciles
`cluster_tenant_assignments`: `source = auto` rows are upserted for every
tenant in `active`/`suspended` state and removed for tenants that leave
those states; `manual` rows are never touched. Readiness reports an
unreachable non-disabled cluster as a degraded (optional) check — still
HTTP 200.

## Job reconciliation

See `docs/workers.md`. The `Comment`/`Name` embedding of the Custos job ID is
the mechanism that makes "did my submit actually succeed?" answerable:
before any resubmit, `ListJobs{Names: ["custos-<id>"]}` and, if accounting is
available, `GetJobRecords{Names: [...]}` for the last 24h.

## Metrics data sources

| Data | Source | Endpoint family |
| --- | --- | --- |
| Node states/counts, CPUs/mem/GRES total/alloc | slurmctld | `GET /slurm/v0.0.4x/nodes` |
| Partitions, limits | slurmctld | `/partitions` |
| Queue depth, pending/running jobs, reasons | slurmctld | `/jobs` |
| Scheduler stats (cycle time, backfill) | slurmctld | `/diag` |
| Historical per-job usage (CPU time, MaxRSS, TRES usage, energy if enabled) | slurmdbd | `GET /slurmdb/v0.0.4x/jobs` |
| Associations, accounts, QoS, fairshare | slurmdbd | `/slurmdb/.../associations`, `/accounts`, `/qos` |
| GPU utilization over time | **not Slurm** — DCGM/node exporters | out of scope for Custos collection; linkable by node+time |
| Job energy | slurmdbd (`tres/usage energy`) when `AcctGatherEnergyType` configured | `/slurmdb/.../jobs` |

## Fake for tests

`internal/slurm/fake` is an in-memory `Cluster`+`Accounting` with a
controllable state machine (advance job states, inject transient errors,
simulate "accepted but response lost"). Used by all job/workflow/worker
tests. Integration tests against a real `slurmrestd` run in CI via a
containerized Slurm (e.g. `giovtorres/slurm-docker-cluster` with
slurmrestd enabled) on a nightly/optional job, not on every PR.
