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
| TM-04 | Stolen access token replay | T6 | S | Short token lifetimes (IdP), no long-lived Custos-minted tokens, BFF session TTL, audit of unusual actions; future: DPoP/mTLS-bound tokens |
| TM-05 | Command injection into sbatch script | T2, T3 | T, E | argv arrays only; strict POSIX quoting; `shell` tasks explicitly privileged and policy-gated; scheduler fields sent as structured `JobDescMsg` values, never as `#SBATCH` text; payload base64-embedded and digest-checked (TM-28/29) |
| TM-06 | Injection through Slurm option values (e.g. `--constraint` expressions, `--output` path traversal) | T2 | T | Allow-listed options; grammar-validated constraint expressions; path policy (project scratch prefix) for output/chdir |
| TM-07 | Path traversal in artifact/output paths | T2 | I | Canonicalize; must be under project-allowed prefixes; no symlink following on control-plane file operations; jobs' own FS access is Slurm's/POSIX's concern |
| TM-08 | SSRF via cluster or tenant connector endpoint URLs | T3, T9 | I, E | Shared `safehttp` transport; TLS-only for tenant connectors; block link-local/metadata/loopback; vet every DNS answer and dial the vetted IP; no redirects; private networks require explicit operator policy |
| TM-09 | Job exfiltrates platform credentials | T4, T5 | I | Execution plane never receives A1/A2/A4; only response-wrapped, path-scoped, single-use tokens or env values the user is already entitled to |
| TM-10 | Compromised node reads other jobs' secrets from env | T5 | I | Prefer wrapped tokens over env values; document residual risk; recommend cgroup/pam isolation at Slurm level |
| TM-11 | Compromised Slurm credential (A1) | T5, T8 | S, E | Per-cluster credential in OpenBao, rotated; impersonation mode signs short-lived JWTs (minutes); slurmrestd network-restricted to Custos; audit correlation |
| TM-12 | Compromised Custos instance | T8 | all | Workload-identity token with minimum policies and short TTL; DB role without DELETE on audit/usage; no root token; k8s NetworkPolicy; read-only FS; non-root; alerting on anomalous OpenBao usage |
| TM-13 | Secret leakage through logs/audit/metrics/errors | T7, T2 | I | `secrets.Value` redaction types; lint forbids logging headers/bodies; error envelope never echoes upstream bodies; audit payload schema has no free-text value fields |
| TM-14 | OpenBao outage → fail-open | env | D | Fail closed for submissions; degraded readiness; cached tokens bounded |
| TM-15 | Duplicate job submission via retries | T2 (accidentally), network | T, D | Idempotency keys; reconcile-by-name before submit; unique `(cluster_id, slurm_job_id)` |
| TM-16 | Workflow "escape": referencing another tenant's secrets/clusters/versions | T2 | E | Validation step 7/8 resolves references inside the caller's tenant scope only |
| TM-17 | Malicious workflow DoS (huge fan-out, arrays, walltime) | T2, T3 | D | Policy limits: max tasks per execution, max array size, max concurrent executions per project, Slurm QoS limits remain in force |
| TM-18 | API DoS | T1 | D | Body size limits, timeouts, per-principal rate limits, pagination caps, no unbounded list endpoints |
| TM-19 | CSRF against BFF | T1 | S, T | SameSite=Lax + CSRF token + Origin checks; API itself is bearer-only (no ambient credentials) |
| TM-20 | XSS in UI (workflow names, job comments, stdout) | T2 | S | React escaping; CSP with nonces; stdout rendered as text in `<pre>`; no `dangerouslySetInnerHTML` |
| TM-21 | Open redirect after login | T1 | S | `returnTo` validated as same-origin relative path |
| TM-22 | Audit tampering | T3, T8 | R | Append-only table enforced by DB triggers (UPDATE/DELETE/TRUNCATE raise), least-privilege app role vs. migrate role split; hash chain (`prev_hash`) per tenant stream; forward to external SIEM |
| TM-23 | Tenant enumeration | T2 | I | 404 for tenants the principal isn't a member of; slugs not sequential |
| TM-24 | Malicious tenant admin harvesting members' identities | T3 | I | Members see only display name/email that the IdP exposes and the user consented to; no `sub` exposure beyond admins |
| TM-25 | Dependency / supply chain | any | T | `govulncheck`, dependabot, pinned module versions, `minimumReleaseAge`-style policy for new deps, SBOM in release |
| TM-26 | Unsafe archive extraction (future stage-in) | T2 | T, E | No server-side extraction in v1; if added, zip-slip checks, size/entry limits |
| TM-27 | Slurm user impersonation abuse (mint JWT for arbitrary user) | T8, T3 | E | Impersonation restricted to mapped usernames in the project binding; signing key only in OpenBao transit (sign-only, never exported) — Custos never holds `slurm.key` bytes |
| TM-28 | Policy bypass via `#SBATCH` directives (aliases, spacing, CRLF, duplicates, quoting) in the payload | T2 | E, T | `sbatchscan` parser-based scanner with canonicalization; every directive is `SECURITY_VIOLATION` by default; wrapper contains no `#SBATCH` and payload is base64-inert; envelope built only from `ExecutionSpec`; bypass corpus tests |
| TM-29 | Validated script swapped for a different one before/at submission | T2, T7 | T | Content-addressed `scripts`; digest pinned in immutable version spec, in `ScriptValidation`, in `ExecutionSpec`; runtime `sha256sum -c` in wrapper |
| TM-30 | BYO connector credential exposed through PostgreSQL or API | T2, T7 | I | Credential streamed directly to platform OpenBao; PostgreSQL stores only `credential_ref`; credential is write-only in OpenAPI and omitted from responses/audit/logs |
| TM-31 | Cross-tenant connector/reference substitution | T2 | I, E | Forced RLS; connector/reference tenant-integrity trigger; service lookup under tenant scope; owner filtering; connector foreign keys; tenant-derived default namespace |
| TM-30 | Environment-variable injection (`LD_PRELOAD`, `BASH_ENV`, `SLURM_*` pre-set, MPI/CUDA tuning) | T2 | T, E | `envcheck` classes: controlled/generated/secret/filtered/user; explicit env, nothing inherited; per-tenant allow-list for filtered names |
| TM-31 | Nested `sbatch`/`salloc`/`srun` from inside a job to exceed the grant | T4 | E | **Native Slurm controls are the enforcement**: association/QoS/TRES limits, cgroup constraints, `job_submit` plugin; Custos `policy.sync` mirrors limits to associations; static command scan is advisory only |
| TM-32 | Users with direct native Slurm credentials bypass Custos entirely | T2 | E | Documented boundary: Custos is only a boundary where users lack direct `sbatch` access or where tenant limits are also enforced by associations |
| TM-33 | Validator tooling (ShellCheck, parsers) exploited by malicious script | T2 | E, D | External tools in a credential-less, network-less, read-only sidecar with CPU/mem/pid/time limits; Go parsers fuzzed; size and line limits before parsing |
| TM-34 | Validation endpoint abused for DoS or as an oracle for policy | T2 | D, I | Separate rate limit, size limit, 20 s budget, result cache by digest; `effectivePolicy` in responses limited to author-relevant flags |
| TM-35 | Structured software field abused to inject module commands | T2 | T | Software requirements resolve against a catalog to a `ModuleSpec`; user strings never reach `module load` |

## Residual risks to document for operators

- Classic HPC sites with shared filesystems: any secret delivered to a job
  env is readable by root on nodes and by the job's own user; use wrapped
  tokens and short TTLs.
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
