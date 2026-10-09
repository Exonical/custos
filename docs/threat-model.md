# Threat Model

Living document; revisit before each milestone that adds an attack surface
(M2 identity, M4 submission, M6 secrets, M8 browser).

## Assets

| Asset | Sensitivity |
| --- | --- |
| A1 slurmrestd credentials / Slurm JWT signing key | critical — full cluster control as `custos` or any user |
| A2 OpenBao token held by Custos | critical — leads to A1 and tenant secrets |
| A3 Tenant secrets (SSH keys, API tokens) | high |
| A4 PostgreSQL credentials and data (metadata, usage, audit) | high |
| A5 User OIDC tokens / BFF refresh tokens | high |
| A6 Workflow definitions (IP) and job outputs | medium–high per tenant |
| A7 Audit log integrity | high |
| A8 Availability of the control plane | medium (Slurm keeps running without Custos) |

## Actors

| Actor | Capability |
| --- | --- |
| T1 Anonymous internet attacker | network access to API/UI |
| T2 Authenticated malicious user (tenant member) | valid token, low-privilege role |
| T3 Malicious tenant admin | full control within one tenant |
| T4 Malicious/compromised workflow or job payload | arbitrary code on compute nodes as the job's Unix user |
| T5 Compromised compute node | root on a node; sees job envs, local filesystem |
| T6 Attacker with a stolen access token | acts as the user until expiry |
| T7 Attacker with Custos DB read | metadata; must not yield secrets |
| T8 Compromised Custos instance | everything the process can reach |
| T9 Malicious platform admin | out of scope (trusted), but audited |
| T10 Network attacker between components | MITM if TLS is misconfigured |

## Trust boundaries

See `docs/architecture.md` §3 (B1–B6). Key asymmetry: the execution plane
(B5) is **always** treated as hostile input, including job output and
anything a job reports back.

## Threats and mitigations (STRIDE-tagged)

| ID | Threat | Actor | STRIDE | Mitigations |
| --- | --- | --- | --- | --- |
| TM-01 | Cross-tenant read/write via IDs (BOLA/IDOR) | T2, T6 | I, E | Tenant in path + membership check; `Scope`-typed repository API; RLS; isolation test matrix; 404 on foreign tenant |
| TM-02 | Privilege escalation via role/tenant fields in request bodies (mass assignment) | T2 | E | Write DTOs with explicit fields, `DisallowUnknownFields`, roles only via member endpoints requiring `*.members.manage` |
| TM-03 | JWT confusion (`alg=none`, HS/RS swap, wrong audience, wrong issuer) | T1 | S | Algorithm allow-list, key-alg binding, exact `iss`, `aud` intersection, `typ` check, go-oidc verifier |
| TM-04 | Stolen access token replay | T6 | S | Short IdP access-token lifetime; Auth.js encrypted HttpOnly JWT session cookie; 8-hour idle and 24-hour absolute session limits; refresh-token revocation on logout; audit unusual actions; future: DPoP/mTLS-bound tokens |
| TM-05 | Command injection into sbatch script | T2, T3 | T, E | argv arrays only; strict POSIX quoting; `shell` tasks explicitly privileged and policy-gated; scheduler fields sent as structured `JobDescMsg` values, never as `#SBATCH` text; payload base64-embedded and digest-checked (TM-28/29) |
| TM-06 | Injection through Slurm option values (e.g. `--constraint` expressions, `--output` path traversal) | T2 | T | Allow-listed options; grammar-validated constraint expressions; path policy (project scratch prefix) for output/chdir |
| TM-07 | Path traversal in artifact/output paths | T2 | I | Canonicalize; must be under project-allowed prefixes; no symlink following on control-plane file operations; jobs' own FS access is Slurm's/POSIX's concern |
| TM-08 | SSRF via cluster or tenant connector endpoint URLs | T3, T9 | I, E | Shared `safehttp` transport; TLS-only for tenant connectors; block link-local/metadata/loopback; vet every DNS answer and dial the vetted IP; no redirects; private networks require explicit operator policy |
| TM-09 | Job exfiltrates platform credentials | T4, T5 | I | Execution plane never receives A1/A2/A4; only response-wrapped, path-scoped, single-use tokens or env values the user is already entitled to |
| TM-10 | Job owner or compromised node reads raw env-injected secrets through `scontrol show job` or process state | T4, T5 | I | Env mode requires explicit `workflow_env`, generic kind, and use authorization; prefer wrapped tokens; document residual risk; recommend Slurm/POSIX isolation |
| TM-11 | Compromised Slurm credential (A1) | T5, T8 | S, E | Per-cluster credential in OpenBao, rotated; impersonation mode signs short-lived JWTs (minutes); slurmrestd network-restricted to Custos; audit correlation |
| TM-12 | Compromised Custos instance | T8 | all | Workload-identity token with minimum policies and short TTL; DB role without DELETE on audit/usage; no root token; k8s NetworkPolicy; read-only FS; non-root; alerting on anomalous OpenBao usage |
| TM-13 | Secret leakage through logs/audit/metrics/errors | T7, T2 | I | `secrets.Value` redaction types; lint forbids logging headers/bodies; error envelope never echoes upstream bodies; audit payload schema has no free-text value fields |
| TM-14 | OpenBao outage → fail-open | env | D | Fail closed for submissions; degraded readiness; cached tokens bounded |
| TM-15 | Duplicate job submission or foreign-job adoption after Slurm ID reuse | T2 (accidentally), network, cluster state loss/wrap | T, D | Idempotency keys; reconcile/adopt by deterministic `custos-<uuid>` name; validate names on ID reads; partial unique `(cluster_id, slurm_job_id)` for active jobs only; time-bounded accounting fallback |
| TM-16 | Workflow "escape": referencing another tenant's secrets/clusters/versions | T2 | E | Validation step 7/8 resolves references inside the caller's tenant scope only |
| TM-17 | Malicious workflow DoS (huge fan-out, arrays, walltime) | T2, T3 | D | Policy limits: max tasks per execution, max array size, max concurrent executions per project, Slurm QoS limits remain in force |
| TM-18 | API DoS | T1 | D | Body size limits, timeouts, per-principal rate limits, pagination caps, no unbounded list endpoints |
| TM-19 | CSRF against BFF | T1 | S, T | SameSite=Lax; double-submit header matches the CSRF cookie; HMAC is bound to the subject with an HKDF key from the first Auth.js secret; exact Origin and `Sec-Fetch-Site` checks; logout is protected; API stays bearer-only |
| TM-20 | XSS in UI (job names, API data, stdout) | T2 | S | React escaping; nonce CSP set by Next `proxy.ts`; stdout and ExecutionSpec rendered as text; no `dangerouslySetInnerHTML`; access/refresh tokens remain in HttpOnly encrypted session chunks |
| TM-21 | Open redirect after login | T1 | S | Auth.js validates OIDC state, nonce, and PKCE; the custom login route validates `returnTo` as a same-origin relative path before `signIn("custos")` |
| TM-22 | Audit tampering | T3, T8 | R | Append-only table enforced by DB triggers (UPDATE/DELETE/TRUNCATE raise), least-privilege app role vs. migrate role split; hash chain (`prev_hash`) per tenant stream; forward to external SIEM |
| TM-23 | Tenant enumeration | T2 | I | 404 for tenants the principal isn't a member of; slugs not sequential |
| TM-24 | Malicious tenant admin harvesting members' identities | T3 | I | Members see only display name/email that the IdP exposes and the user consented to; no `sub` exposure beyond admins |
| TM-25 | Dependency / supply chain | any | T | `govulncheck`, dependabot, pinned module versions, `minimumReleaseAge`-style policy for new deps, SBOM in release |
| TM-26 | Unsafe archive extraction (future stage-in) | T2 | T, E | No server-side extraction in v1; if added, zip-slip checks, size/entry limits |
| TM-27 | Slurm user impersonation abuse (mint JWT for arbitrary user) | T8, T3 | E | Impersonation restricted to mapped usernames in the project binding; signing key only in OpenBao transit (sign-only, never exported) — Custos never holds `slurm.key` bytes |
| TM-28 | Policy bypass via `#SBATCH` directives (aliases, spacing, CRLF, duplicates, quoting) in the payload | T2 | E, T | `sbatchscan` parser-based scanner with canonicalization; every directive is `SECURITY_VIOLATION` by default; wrapper contains no `#SBATCH` and payload is base64-inert; envelope built only from `ExecutionSpec`; bypass corpus tests |
| TM-29 | Validated script swapped for a different one before/at submission | T2, T7 | T | Content-addressed `scripts`; digest pinned in immutable version spec, in `ScriptValidation`, in `ExecutionSpec`; runtime `sha256sum -c` in wrapper |
| TM-30 | BYO connector credential or platform credential reference exposed through PostgreSQL/API | T2, T7 | I | Credential streams directly to platform OpenBao; PostgreSQL retains the internal `credential_ref`; connector responses omit that reference and values and expose only `has_credential`; typed non-secret config rejects unknown keys with HTTP 422; credentials are write-only |
| TM-31 | Cross-tenant connector/reference substitution | T2 | I, E | Forced RLS; connector/reference tenant-integrity trigger; service lookup under tenant scope; owner/project/admin use checks repeated at execution; connector foreign keys; tenant-derived default namespace |
| TM-32 | Wrapping token stolen while queued or per-reference token over-scoped | T4, T5 | I | One-hour response-wrap TTL; single unwrap; child TTL is walltime + 15m capped at 24h; two uses; no parent; idempotent `ref-<uuid>` policy permits only the exact KV data and metadata paths |
| TM-30 | Environment-variable injection (`LD_PRELOAD`, `BASH_ENV`, `SLURM_*` pre-set, MPI/CUDA tuning) | T2 | T, E | `envcheck` classes: controlled/generated/secret/filtered/user; explicit env, nothing inherited; per-tenant allow-list for filtered names |
| TM-31 | Nested `sbatch`/`salloc`/`srun` from inside a job to exceed the grant | T4 | E | **Native Slurm controls are the enforcement**: association/QoS/TRES limits, cgroup constraints, `job_submit` plugin; Custos `policy.sync` mirrors limits to associations; static command scan is advisory only |
| TM-32 | Users with direct native Slurm credentials bypass Custos entirely | T2 | E | Documented boundary: Custos is only a boundary where users lack direct `sbatch` access or where tenant limits are also enforced by associations |
| TM-33 | Validator tooling (ShellCheck, parsers) exploited by malicious script | T2 | E, D | External tools in a credential-less, network-less, read-only sidecar with CPU/mem/pid/time limits; Go parsers fuzzed; size and line limits before parsing |
| TM-34 | Validation endpoint abused for DoS or as an oracle for policy | T2 | D, I | Separate rate limit, size limit, 20 s budget, result cache by digest; `effectivePolicy` in responses limited to author-relevant flags |
| TM-35 | Structured software field abused to inject module commands | T2 | T | Software requirements resolve against a catalog to a `ModuleSpec`; user strings never reach `module load` |
| TM-36 | Compromised Custos uses slurmdbd write authority to alter site policy | T8 | E, D | Platform-only enablement; report mode; full-reconcile boundary excludes other users; ownership table limits unbinding deletes; 200-op cap; per-operation audit; network-restricted slurmrestd; minimum Slurm AdminLevel |
| TM-37 | Stolen or replayed stateless BFF session cookie | T1, T6 | S, I | Auth.js encrypted JWT cookie; HttpOnly/Secure/SameSite; 8-hour JWT lifetime; short access-token TTL; fail-closed `getToken` checks including `RefreshTokenError`; logout revokes refresh token best-effort; `CUSTOS_WEB_AUTH_SECRETS` rotation. No server-side session list exists, so an individual cookie cannot be revoked locally. |
| TM-38 | Untrusted container image or generic remote-launch configuration | T2, T3, T4 | T, E, I | Images are user-selected code running as the job Unix user; platform clusters may restrict URI prefixes and require sha256 digest pinning. Only `$CUSTOS_JOB_DIR` is mounted. Generic multinode uses a Custos-generated, quoted `srun --overlap` rsh wrapper and Slurm node lists (no SSH); containerized generic launch requires `slurm_in_container` and a compatible Slurm client. User environment values enter either runtime through `/usr/bin/env` inside the image (images must contain it); Pyxis `--container-env` carries only runtime, secret, `MULTINODE_*`, and generated `CUSTOS_SERVICE_*` names. Controlled/runtime/secret variables remain host-side and container-runtime control names are blocked. |
| TM-39 | Private image pull credentials exposed to job owners or compute-node actors | T2, T3, T4, T5 | I | Admission freezes only secret reference IDs/handles; the worker resolves values immediately before submission. The wrapper scopes Apptainer credentials to `apptainer pull`, or writes Pyxis credentials to a host-only directory (`0700`) and file (`0600`) for `enroot import`, removes them before user launch, and has an EXIT cleanup. The EXIT trap removes both the temporary credentials directory on import failure and `$CUSTOS_JOB_DIR` (including the local image) when the job exits. Pull variables are unset before the workload and omitted from container environment lists. Residual: Slurm job environment may be inspectable by authorized job owners and Slurm/node administrators while pending or starting. Pull-secret tasks are single-node only. |
| TM-40 | Node hook scripts run as root on every allocated compute node and could be tampered with in storage or transit, or abused by a malicious platform-admin hook | T5, T8, T9 | T, E | The Prolog/Epilog and namespace scripts are generated from validated values only (strict name, source, target and option allow-lists; reserved targets such as `/etc`, `/usr` and `/tmp` are rejected) and parse `mounts.tsv` as data without `eval`. Custom hooks are platform-admin only, size and syntax checked, and audited by name and SHA-256 (never body). The bundle is deterministic, its SHA-256 is the `ETag` the agent verifies before install, and the agent syntax-checks scripts and flips a symlink atomically. Scripts set `PATH`, `umask 077`, call no Slurm commands and fail closed. Residual: a compromised Custos (T8) or platform admin (T9) can ship root scripts to nodes that pull; restrict who holds `cluster.manage` and protect the pull path with TLS. |
| TM-41 | Node pull token stolen from a node or log | T5, T7 | I | Token is `cnt_` plus 32 random bytes, shown once and stored only as a SHA-256 hash, so a database read yields nothing usable. It grants read-only access to one cluster's bundle (mount topology, account and tenant slugs, hook text), never the API, secrets or other clusters, and is revocable (`DELETE .../node-tokens/{id}`) with `last_used_at` and per-node status for detection. The pull endpoint skips OIDC but is rate limited per client address, treats malformed and unknown tokens identically, and the token is never logged. Keep the token file root `0600` and out of the bundle. |
| TM-42 | One tenant's job reads another tenant's NFS data on a shared node | T2, T3, T4 | I, E | Per mode: `namespace` mounts tenant shares only inside the job's private mount namespace (never the host); `tenant_exclusive` refuses a job of a different tenant while a tenant is active and unmounts when its last job ends; `node_exclusive` refuses any second Custos-account job. A target mounted from an unexpected source, an account bound to two tenants, an unresolvable account or an unmount that fails exits non-zero (node drain, Prolog also requeues) rather than continuing. Tenant mounts get `nosuid,nodev`. NFS targets must not appear in `namespace.yaml` `dirs`/`dir_confs` (their per-job backing storage is deleted at job end). Residual: host modes rely on Slurm placement; jobs of accounts without a binding are not blocked and can see tenant mounts active on the host; the namespace-script environment is not yet verified on a real node (account falls back to a root-only file written by the Prolog). |
| TM-43 | Tenants sharing one Slurm service user defeat NFS file-ownership separation | T2, T3, T4 | I, E | Custos warns (`SHARED_SERVICE_USER`) when tenant mounts exist and bindings of two or more tenants on a cluster share the service user; the decision is the operator's. With a shared OS user, isolation depends on the mount scripts alone, so prefer `namespace` mode or per-tenant placement, and export NFS shares per tenant host or network where possible. |

## Residual risks to document for operators

- Classic HPC sites with shared filesystems: any secret delivered to a job
  env is readable by root on nodes and by the job's own user; use wrapped
  tokens and short TTLs.
- Node hooks (ADR-032) put Custos-generated and admin-written root scripts on
  compute nodes. Treat `cluster.manage` and node tokens as root-adjacent, keep
  the pull endpoint behind TLS, and see TM-40 to TM-43 for per-mode NFS
  isolation limits (host modes depend on Slurm placement; a shared Slurm
  service user removes file-ownership separation).
- slurmrestd's own authorization model: Custos's service identity may be
  more privileged than any single user; the network path to slurmrestd
  must be limited to Custos.
- Custos metadata (job names, workflow names, parameters) is visible to
  Slurm administrators through `scontrol`/`sacct`.
- Custos application-layer resource policy is **not** a sandbox. It
  prevents *requesting* more than permitted through Custos. Preventing a
  running payload from *obtaining* more (nested submissions, direct
  `sbatch` with the user's own credentials) requires Slurm-native limits
  on the associations Custos uses and, ideally, a `job_submit` plugin that
  restricts direct submissions for Custos-managed accounts.
- Stateless BFF cookies have no server-side revocation list. Ending the IdP
  session or revoking its refresh token prevents renewal; a stolen access token
  remains usable until its short expiry, bounded by the session's absolute TTL.
