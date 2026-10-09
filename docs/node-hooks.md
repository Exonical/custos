# Node Hooks: Tenant NFS Mounts and Custom Prolog/Epilog

This is the platform-admin guide to the node hooks feature (ADR-032). It lets
Custos give each tenant's jobs their own NFS shares on the compute nodes and
lets you run extra Prolog/Epilog snippets, all managed from the Custos API.
Everything here is platform-admin only (`cluster.manage`). Tenants never see
or edit it.

Custos cannot push files to nodes: slurmrestd does not write node files and
cannot edit `slurm.conf`. The feature therefore produces a **bundle** that
you install on the nodes, either by hand or with the optional pull agent.

## How it fits together

1. You store a node configuration for a cluster: shared mounts, tenant mounts
   and hooks.
2. A tenant mount names a tenant (uuid) and an NFS export. Custos resolves
   which Slurm accounts belong to the tenant from the project cluster
   bindings on that cluster (every binding of every project of the tenant).
3. Custos renders the bundle: a tab-separated table of mounts per account,
   the Prolog/Epilog scripts, namespace scripts, the pull agent and systemd
   units.
4. On each node, Slurm runs the scripts. At job start the Prolog reads
   `SLURM_JOB_ACCOUNT`, finds the tenant in the table and arranges that the
   tenant's shares are mounted for that job (according to the isolation
   mode). Accounts without a binding get only the shared mounts.

## Example configuration

Tenant A gets two private shares; every job gets a read-only shared apps
tree. Tenant ids are the tenant uuids.

`PUT /api/v1/clusters/hpc1/node-config`

```json
{
  "version": 0,
  "config": {
    "isolation_mode": "namespace",
    "mount_timeout_seconds": 30,
    "shared_mounts": [
      {"name": "apps", "fstype": "nfs4", "source": "server:/hpc/apps",
       "target": "/apps", "options": ["ro", "nfsvers=4.2"]}
    ],
    "tenant_mounts": [
      {"tenant": "0192f6c4-0000-7000-8000-00000000000a", "name": "flight-data",
       "fstype": "nfs4", "source": "server:/tenantA/flight_data",
       "target": "/mnt/data", "options": ["rw", "hard"]},
      {"tenant": "0192f6c4-0000-7000-8000-00000000000a", "name": "scratch",
       "fstype": "nfs4", "source": "server:/tenantA/scratch",
       "target": "/scratch", "options": ["rw", "hard"]}
    ],
    "hooks": [
      {"name": "node-banner", "phase": "prolog", "order": 10,
       "script": "#!/bin/bash\nlogger -t node-banner \"job start\"\n"}
    ]
  }
}
```

The response carries `revision` (monotonic, bumped on every write),
`content_sha256`, the optimistic-concurrency `version` and `warnings`. Send
the returned `version` with the next update; a stale value returns
`409 VERSION_CONFLICT`. Tenant mounts always get `nosuid,nodev` added;
`ro` is the default for shared mounts and `rw` for tenant mounts.

### Validation rules

| Field | Rule |
| --- | --- |
| `name` | `^[a-z0-9][a-z0-9-]{0,62}$`; unique per list (tenant mounts: per tenant) |
| `fstype` | `nfs` or `nfs4` |
| `source` | `host:/path`, characters `[A-Za-z0-9._/-]`, no `..`, at most 512 |
| `target` | absolute, `[A-Za-z0-9._/-]`, no `..`, no trailing slash, at most 256; never `/` or at/under `/bin /boot /dev /etc /lib /lib64 /proc /root /run /sbin /sys /usr /var /tmp /dev/shm` |
| targets | shared targets unique; a tenant target may not equal or nest with a shared target or another target of the same tenant; the same target for different tenants is allowed |
| `options` | allow-list: `ro rw hard soft noatime nodiratime relatime nosuid nodev noexec nolock _netdev`, `nfsvers=`/`vers=` (3, 4, 4.0, 4.1, 4.2), `proto=` (tcp, rdma), `sec=` (sys, krb5, krb5i, krb5p), `timeo= retrans= rsize= wsize= actimeo= port= nconnect=` (bounded integers), `lookupcache=` (all, none, pos, positive); not both `ro` and `rw`, not both `hard` and `soft` |
| tenant | must exist, not be deleting/deleted, and be assigned to the cluster (unless the cluster is visible to all tenants) |
| hooks | script at most 64 KiB, first line `#!/bin/bash` or `#!/bin/sh`, LF endings, no NUL bytes, must parse as shell; `order` 0 to 99, unique per phase; at most 32 hooks |
| timeout | `mount_timeout_seconds` 5 to 300 |

Validation errors are `422` with codes `NODE_MOUNT_INVALID`,
`NODE_HOOK_INVALID` or `NODE_CONFIG_INVALID` and `details[].field` paths such
as `tenant_mounts[1].target`. Hook scripts are checked with the in-process
shell parser only; the ShellCheck sidecar is not reachable from the cluster
service, so lint hook text yourself if you want ShellCheck coverage.

## Choosing an isolation mode

| Mode | Where tenant shares are mounted | Needs | Use when |
| --- | --- | --- | --- |
| `namespace` (default) | Inside each job's private mount namespace only | Slurm 25.11+, cgroup v2, `PrologFlags=Contain`, `NamespaceType=namespace/linux`, `namespace.yaml` | You can run `namespace/linux`; jobs of different tenants may share nodes |
| `tenant_exclusive` | On the host while the tenant has jobs on the node | Placement that keeps tenants apart | No `namespace/linux`; combine with per-tenant partitions or node features |
| `node_exclusive` | On the host; a second Custos-account job always fails the Prolog | Exclusive node allocation | Strictest host isolation |

Rules common to all modes: shared mounts are mounted by the Prolog if not
already mounted and are never unmounted; a target already mounted from a
different source is an isolation violation and fails closed; a Slurm account
bound to more than one tenant on the cluster fails every job of that account.

Changing the mode: drain the affected nodes first so no node holds mounts of
the old mode, then install the new bundle and resume the nodes.

### Warnings

The configuration views carry non-blocking `warnings`:

- `SHARED_SERVICE_USER`: tenant mounts exist and bindings of two or more
  tenants on the cluster share the cluster's Slurm service user. All of their
  jobs run as one OS user, so file ownership cannot separate tenants and the
  mount isolation is the only barrier.
- `NAMESPACE_REQUIRES_SLURM_25_11`: namespace mode on a cluster whose
  reported Slurm version is older than 25.11, or unknown (the cluster has not
  synced capabilities yet).

## Installing the bundle

Download the archive and unpack it as root on the node (or into your image):

```sh
curl -fsS -H "Authorization: Bearer $OIDC_TOKEN" -o node.tar.gz \
  https://custos.example.org/api/v1/clusters/hpc1/node-config/bundle
tar -xzf node.tar.gz -C /
```

The archive contains `etc/custos/node/` (scripts, `mounts.tsv`, the agent) and
`etc/systemd/system/custos-node-sync.{service,timer}` plus a `README.md`
specific to the revision. Then edit `slurm.conf`:

```text
Prolog=/etc/custos/node/prolog.d/*
Epilog=/etc/custos/node/epilog.d/*
PrologFlags=Alloc,Contain
NamespaceType=namespace/linux        # namespace mode only
```

Restart slurmd (PrologFlags needs more than `scontrol reconfigure`). For
namespace mode create `namespace.yaml` next to `slurm.conf`:

```yaml
defaults:
  auto_base_path: true
  base_path: /var/nvme/storage
  clone_ns_script: /etc/custos/node/ns-clone.sh
  clone_ns_epilog: /etc/custos/node/ns-epilog.sh
  clone_ns_script_wait: 35
  clone_ns_epilog_wait: 35
```

The waits must be at least `mount_timeout_seconds` plus 5.
**Never list NFS targets in `dirs` or `dir_confs`**: those directories are
backed by per-job storage that Slurm deletes at job end.

Slurm runs scripts matched by `Prolog=/dir/*` in reverse alphabetical order.
The bundle names them so that `900-custos-mounts` runs first and
`100-custos-unmount` last; custom hooks are `<800-order>-<name>`, so order 0
runs first and order 99 last among custom hooks.

## The pull agent

`custos-node-sync` keeps nodes current. It runs from a systemd timer every two
minutes with a random delay, so a newly bound Slurm account reaches nodes
within about two minutes; plan job starts for that account accordingly.

1. Create a token (shown once; Custos stores only its hash):

   ```sh
   curl -fsS -X POST -H "Authorization: Bearer $OIDC_TOKEN" \
     -H 'Content-Type: application/json' -d '{"name":"rack1"}' \
     https://custos.example.org/api/v1/clusters/hpc1/node-tokens
   ```

2. On the node, write the `token` value to `/etc/custos/node-token` (root,
   mode 0600) and create `/etc/custos/agent.conf` (mode 0600):

   ```text
   CUSTOS_URL=https://custos.example.org
   CUSTOS_TOKEN_FILE=/etc/custos/node-token
   CUSTOS_CA_FILE=/etc/custos/ca.pem
   ```

3. `systemctl daemon-reload && systemctl enable --now custos-node-sync.timer`.

The agent requests `GET /api/v1/node/bundle` with `X-Custos-Node` set to the
short hostname (override with `CUSTOS_NODE_NAME`) and `If-None-Match` set to
the last bundle hash. It verifies the archive's SHA-256 against the `ETag`,
extracts into `/var/lib/custos-node/releases/<sha256>`, syntax-checks every
script with `bash -n`, and atomically repoints the symlink `/etc/custos/node`.
An existing real directory is moved to `/etc/custos/node.pre-custos-agent`.
The last three releases are kept.

Revoke a token with `DELETE /api/v1/clusters/hpc1/node-tokens/{id}`; the next
poll gets `401`. List tokens with `GET .../node-tokens` (metadata and
`last_used_at` only).

## Checking rollout

`GET /api/v1/clusters/hpc1/node-status` returns `current_revision`,
`current_bundle_sha256` and, per node, the revision and bundle hash it last
fetched with a `stale` flag. A node is stale when its bundle hash differs
from the bundle that would be served now. Bindings change the bundle without
changing the configuration revision, so compare hashes, not revisions.

## What the scripts do

All scripts set a fixed `PATH`, `umask 077`, log to syslog under the tag
`custos-node`, call no Slurm commands, serialize mount changes with
`flock /run/custos/mounts.lock` and run every mount under `timeout`.

- **Prolog** (`900-custos-mounts`): mounts missing shared mounts, validates
  `SLURM_JOB_ID`, records `/run/custos/jobs/<id>/{account,tenant}`. Host modes
  then refuse another tenant (any other job in `node_exclusive`) with
  `ISOLATION_VIOLATION`, mount the tenant's shares and record
  `/run/custos/active/<tenant>/<id>`. If a mount fails, this attempt's mounts
  are undone and the job's marker removed before the Prolog exits non-zero.
- **Epilog** (`100-custos-unmount`): host modes remove the marker and, when
  the tenant has no other job on the node, unmount its targets (retry, then
  lazy unmount) and verify with `mountpoint`. A target that stays mounted
  exits non-zero so Slurm drains the node. Namespace mode only removes
  `/run/custos/jobs/<id>`.
- **`ns-clone.sh` / `ns-epilog.sh`** (namespace mode): resolve the account from
  `SLURM_JOB_ACCOUNT`, or else from `/run/custos/jobs/$SLURM_JOB_ID/account`,
  and mount or unmount the tenant's shares with `nsenter --mount="$SLURM_NS"`.
  If the account cannot be resolved they fail closed. These two points
  (whether `clone_ns_script` receives the job id and account, and whether the
  Prolog runs before namespace creation under `PrologFlags=Contain`) are
  unverified; verify them on a test node before relying on namespace mode.

A non-zero Prolog exit drains the node and requeues the job; a non-zero Epilog
exit drains the node. That is deliberate. Resume the node after fixing the
cause (`scontrol update nodename=... state=resume`).

## Troubleshooting

| Symptom | Likely cause |
| --- | --- |
| Node drained, syslog `ISOLATION_VIOLATION` | Another tenant's job (or any job, in `node_exclusive`) is active on the node, a target is mounted from another source, or an account maps to two tenants. Fix placement or the bindings. |
| Node drained after a job, syslog `still mounted` | An NFS server is hung or a process holds the mount. Free the mount, then resume the node. |
| Job in namespace mode cannot see the share | `ns-clone.sh` could not resolve the account; check `/run/custos/jobs/<id>/account` and the `clone_ns_script_wait` value. |
| `401` from the agent | Token revoked, mistyped or for another cluster. |
| `403 CLUSTER_DISABLED` | The cluster is disabled in Custos. |
| `429` | The pull endpoint is rate limited per client address; nodes behind one NAT share the limit. |

## Planned, not implemented yet

These belong to a later phase and are **not** part of the current feature:

- Automatic container bind mounts of tenant shares into Apptainer/Pyxis tasks.
- Submission flags derived from the isolation mode: `--exclusive=user` for
  tenant isolation and `--exclusive` for node isolation. Today you must
  arrange exclusivity with Slurm configuration (partitions, node features,
  QOS or user-level policies) yourself.
