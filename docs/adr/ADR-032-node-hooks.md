# ADR-032: Admin-managed node hooks for tenant NFS mounts

Status: Accepted  
Date: 2026-10-05

## Context

Tenants need their own NFS shares (for example `server:/tenantA/flight_data`
on `/mnt/data`) on the compute nodes where their jobs run, without those
shares being readable by other tenants' jobs, and sites need a controlled way
to run extra Prolog/Epilog snippets. Custos reaches Slurm only through
slurmrestd, which accepts job descriptions but cannot write files on compute
nodes or change `slurm.conf`. Slurm itself offers the primitives: the
`Prolog`/`Epilog` scripts run as root on every allocated node (a non-zero
exit drains the node; a failing Prolog also requeues the job), and
`namespace/linux` (Slurm 25.11+) gives each job a private mount namespace
with `clone_ns_script` and `clone_ns_epilog` hooks.

Custos already knows which Slurm account belongs to which tenant: project
cluster bindings map a `slurm_account` on a cluster to a project and therefore
to a tenant.

## Decision

- Each cluster carries a platform-admin-only node configuration
  (`cluster_node_config`): an isolation mode, a mount timeout, shared mounts
  (every job), tenant mounts (resolved through the job's Slurm account and
  the project cluster bindings on that cluster) and custom prolog/epilog
  hook scripts. It is versioned for optimistic concurrency, and every
  successful write bumps a monotonic `revision` and is audited as
  `cluster.node_config.updated` with hook names and hashes only.
- Custos renders a deterministic **bundle** from the validated
  configuration and the current bindings: a mount table (`mounts.tsv`), the
  Prolog/Epilog scripts, the namespace scripts, a pull agent and systemd
  units. Only validated values reach the scripts, and they parse the table
  as data; nothing is evaluated.
- **Delivery is a bundle plus a pull agent.** slurmrestd cannot write node
  files, so an admin either downloads the bundle
  (`GET /clusters/{cluster}/node-config/bundle`) or runs `custos-node-sync`
  on each node, which polls `GET /api/v1/node/bundle` with a per-cluster node
  token every two minutes, verifies the archive against the `ETag`, syntax
  checks the scripts and flips a symlink atomically. Tokens are random,
  stored only as a SHA-256 hash, revocable and grant read-only access to the
  bundle of one cluster.
- **Three isolation modes**:
  - `namespace` (default): shared mounts are mounted on the host by the
    Prolog; tenant mounts are mounted only inside each job's private mount
    namespace by `clone_ns_script` and unmounted by `clone_ns_epilog`.
    Needs Slurm 25.11+ with `PrologFlags=Contain`, cgroup v2 and
    `NamespaceType=namespace/linux`. Use it whenever the site can: nothing
    tenant-specific ever appears in the host namespace, so jobs of several
    tenants can share a node.
  - `tenant_exclusive`: the Prolog mounts the tenant's shares on the host and
    the Epilog unmounts them when the tenant's last job on the node ends.
    A job of another tenant arriving while a tenant is active fails the
    Prolog. Use it on clusters without `namespace/linux`; pair it with
    Slurm-level placement (partitions or node features per tenant, or
    exclusive node allocation) so conflicts do not drain nodes.
  - `node_exclusive`: like `tenant_exclusive` but a second Custos-account job
    on the node always fails the Prolog, even from the same tenant. Use it
    with exclusive node allocation for the strictest isolation.
- **Fail closed.** Any inability to mount, resolve the account, prove
  isolation or release a mount exits non-zero. Prolog failures requeue the
  job and drain the node; Epilog failures drain the node, which is the
  deliberate "ensure unmounted" signal. A failed mount attempt unmounts what
  that attempt mounted. A Slurm account bound to more than one tenant on the
  cluster is a conflict and fails every job of that account.
- **Non-Custos accounts** (accounts without a binding) get the shared mounts
  only and take no tenant markers.
- **Namespace-private directories.** NFS targets must never appear in
  `namespace.yaml` `dirs` or `dir_confs`: those directories are backed by
  per-job storage under `base_path` that Slurm deletes when the job ends,
  which would delete data on an NFS export. Tenant mounts are mounted by
  `clone_ns_script` into the namespace instead. Config validation therefore
  refuses reserved targets (`/tmp`, `/dev/shm`, `/run`, `/var`, `/etc` and
  similar) so the two lists cannot collide.
- **Hook ordering.** Slurm runs `Prolog=/dir/*` and `Epilog=/dir/*` in
  reverse alphabetical order. The bundle numbers scripts `NNN-name` with
  `NNN = 800 - order` for custom hooks, so a lower admin `order` runs
  earlier; `900-custos-mounts` always runs first in the Prolog and
  `100-custos-unmount` always last in the Epilog. Hook `order` is unique per
  phase.
- **Unverified namespace points** (to be confirmed on a real node, kept
  isolated in the scripts): whether `clone_ns_script` receives
  `SLURM_JOB_ID` and `SLURM_JOB_ACCOUNT` in its environment, and whether the
  Prolog has already run when the namespace is built under
  `PrologFlags=Contain`. The Prolog therefore records
  `/run/custos/jobs/<jobid>/account` (root-only); `ns-clone.sh` uses
  `SLURM_JOB_ACCOUNT` when present and otherwise the recorded file, and fails
  closed when neither resolves.
- **Operational rules.** Changing the isolation mode requires drained nodes
  (a node must not hold tenant mounts of the old mode). A newly bound account
  appears in the bundle immediately but reaches nodes only at the next pull,
  up to about two minutes (the timer polls every two minutes with a random
  delay). Bundle changes from bindings do not bump the configuration
  revision, so staleness is judged by bundle hash, not revision.
- **Warn, do not block, shared service users.** When tenant mounts exist and
  bindings of two or more tenants on a cluster use the same Slurm service
  user, the API returns a `SHARED_SERVICE_USER` warning: tenants' jobs run as
  one OS user, so file ownership cannot separate them and isolation rests on
  the mount scripts alone. Namespace mode on Slurm older than 25.11 (or an
  unknown version) returns `NAMESPACE_REQUIRES_SLURM_25_11`. Both are
  warnings because the platform owner may accept the risk.

## Alternatives considered

- **Configure mounts through slurmrestd or job submission flags**: neither
  can mount filesystems on a node; container bind mounts only cover
  containerized tasks (planned separately, see Consequences).
- **Pod/Kubernetes style volume mounts**: not applicable on bare-metal Slurm;
  reserved for a later Kubernetes/Slinky design.
- **Always mounting every tenant's shares on every node**: exposes tenant
  data to every job on the node.
- **Block rather than warn on shared service users**: would make the feature
  unusable on common single-service-user deployments; the decision is left to
  the operator.
- **A push channel from Custos to nodes**: needs inbound access to nodes and
  node credentials in Custos; the pull agent keeps Custos credential-free.

## Consequences

- Platform admins own root-run scripts on compute nodes. The scripts are
  generated, deterministic and covered by golden, syntax, shellcheck and
  stubbed behaviour tests; custom hooks are admin-supplied code, syntax
  checked in-process and audited by hash.
- A stolen node token yields read-only access to one cluster's bundle (mount
  topology, account names, tenant slugs and hook text). It can be revoked
  without redeploying the bundle.
- Host isolation modes depend on Slurm placement: jobs of non-Custos accounts
  are not blocked by tenant markers and can see tenant mounts that are active
  on the node. Use dedicated partitions or node features, or namespace mode.
- Planned, not implemented in this phase: automatic container bind mounts for
  tenant mounts and the matching submission flags (`--exclusive=user` for
  tenant isolation, `--exclusive` for node isolation).

See also `docs/node-hooks.md`, `docs/slurm.md`, `docs/threat-model.md`
(TM-40 to TM-43) and `docs/api.md`.
