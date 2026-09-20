# Script Validation, Admission and Submission Security

This document defines how user-authored workload scripts are edited,
validated, stored, admitted and finally handed to Slurm, and the security
invariant that binds it all together.

## The invariant

> Every policy-sensitive attribute sent to Slurm MUST originate from a
> validated, persisted Custos `ExecutionSpec`, and MUST NOT originate from
> untrusted workload script contents.

Enforced three ways:

1. **By types.** The admission pipeline that produces `ExecutionSpec` does
   not receive script bodies — only `ScriptRef{ID, Digest, Language}`. The
   Slurm adapter's `JobSubmission` is built from `ExecutionSpec` alone. There
   is no code path in which a string from a script reaches a scheduler field.
2. **By validation.** Scripts containing policy-controlled `#SBATCH`
   directives fail validation with `SECURITY_VIOLATION` (default) or are
   converted into an ordinary resource *request* (import mode) that still
   goes through policy.
3. **By tests.** A dedicated `internal/admission/invariant_test.go` suite and
   the bypass corpus in "Testing" below; any failure is a security defect.

## Terminology

| Term | Meaning |
| --- | --- |
| Payload | The user's script (bash, python, ...). Untrusted. |
| Wrapper | Custos-generated batch script that sets up the environment and executes the payload. Trusted. |
| Submission envelope | Structured scheduler fields (account, partition, resources, ...) sent through slurmrestd's job description. Produced only by Custos. |
| `ExecutionSpec` | Immutable canonical record of what Custos intends to run: envelope + payload reference + environment + security context. |
| Admission | The pipeline that turns (principal, task definition, resource request) into an `ExecutionSpec`, or rejects. |

## Pipeline

```text
User edits payload in Monaco
    ↓ (debounced, cancellable)
Frontend diagnostics                       usability only; never authoritative
    ↓
POST .../versions/{v}/tasks/{task}/validate
    ↓
Backend parse            shsyntax (mvdan/sh) | python AST | yaml | json
    ↓
Static validation        shellcheck (sandboxed), language linters
    ↓
Security validation      sbatchscan (directive scanner), forbidden-construct scan (advisory)
    ↓
Policy validation        ValidationPolicy severity mapping; shell/import permissions
    ↓
Environment validation   env var classes, software environment resolution
    ↓                                   ── persisted as ScriptValidation(digest, tools, result)
Resource admission       authn → authz → ResourcePolicy → entitlement → allocation → admission → placement
    ↓                                   ── persisted as ExecutionSpec(digest)
Generated submission     wrapper + envelope from ExecutionSpec only
    ↓
slurmrestd
```

Two moments of enforcement:

- **Author time** (`.../validate`, `.../publish`): full validation, results persisted
  against the script digest.
- **Execution time** (`TaskExecution: READY → ADMITTING`): verify the payload
  digest equals the validated digest, the validation is still current
  (same `ValidationPolicy` version and tool versions, not expired), then
  build the `ExecutionSpec`. Stale or missing validation → re-run
  validation as a work item before admission. Failure → task `FAILED`
  with reason `VALIDATION_FAILED` / `ADMISSION_DENIED`; never `SUBMITTING`.

## Packages

```text
internal/
  scripts/        content-addressed script storage (tenant-scoped), digests, size limits
  validation/     ScriptValidator port, registry, pipeline, Diagnostic, Severity, ValidationPolicy
    shsyntax/     bash/POSIX parse via mvdan.cc/sh/v3/syntax (pure Go)
    sbatchscan/   Slurm directive scanner + canonicalizer (uses shsyntax comment nodes)
    shellcheck/   external tool runner (sandboxed sidecar), maps JSON output → Diagnostic
    envcheck/     environment variable classification/validation
    softwareenv/  structured software requirement resolution against the cluster catalog
    pyast/        (later) python parse via sandboxed `python -m ast`/ruff
  admission/      ExecutionSpec, builder pipeline, invariant tests
  submission/     wrapper generation + JobSubmission mapping from ExecutionSpec
```

## Validator port

```go
package validation

type Severity string // INFO | WARNING | ERROR | POLICY_VIOLATION | SECURITY_VIOLATION

type Diagnostic struct {
    Source   string   // "shsyntax" | "shellcheck" | "sbatchscan" | "envcheck" | "softwareenv" | "policy"
    Code     string   // "SC2086", "CUSTOS101", ...
    Severity Severity
    Line, Column, EndLine, EndColumn int   // 1-based; 0 = whole document
    Message  string
    Field    string   // canonical scheduler field for sbatchscan findings, e.g. "qos"
    Fix      string   // optional actionable hint ("Configure GPUs using Resources → GPU")
}

type Input struct {
    Language     Language            // bash | sh | python | yaml | json
    Script       []byte              // bounded by limits before reaching validators
    Digest       Digest              // sha256 of Script, computed once by the pipeline
    Resources    workflowspec.Resources
    Environment  map[string]string   // user-configurable vars only
    Software     []workflowspec.SoftwareRequirement
    Cluster      *clusters.Snapshot  // capabilities, if placement is explicit
    Policy       policies.Effective  // resolved tenant ∩ project policy (read-only view)
}

type Result struct {
    Diagnostics []Diagnostic
    Tool        ToolVersion          // {Name, Version} for reproducibility
}

type ScriptValidator interface {
    Name() string
    Languages() []Language
    Validate(ctx context.Context, in Input) (Result, error)   // error = validator failure, not "invalid script"
}
```

The pipeline runs applicable validators (by language) concurrently with a
per-validator timeout, merges diagnostics, applies `ValidationPolicy`, and
returns a `ScriptValidation`:

```go
type ScriptValidation struct {
    ID                 uuid.UUID
    TenantID           uuid.UUID
    WorkflowVersionID  *uuid.UUID   // nil for ad-hoc validate calls
    TaskName           string
    ScriptDigest       Digest
    Language           Language
    Valid              bool         // no diagnostic at or above the blocking threshold
    Diagnostics        []Diagnostic
    ToolVersions       map[string]string
    PolicyVersion      int64        // ValidationPolicy version applied
    ValidatedAt        time.Time
    ExpiresAt          time.Time    // default 30d; re-validate after
}
```

A validator *failing* (timeout, crash, sandbox error) yields a synthetic
`ERROR` diagnostic `CUSTOS900 validator <name> unavailable` — fail closed.

Custos diagnostic codes are `CUSTOS` + three digits, allocated by range so
sources never collide:

| Range | Source | Examples |
| --- | --- | --- |
| `CUSTOS0xx` | `shsyntax` / general | `010` CRLF line endings, `011` directive-like text outside a comment, `012` foreign scheduler directive (`#PBS`, `#BSUB`), `013` byte-order mark (ERROR — bash executes it as a command), `014` suspicious directive-like comment (zero-width characters) |
| `CUSTOS1xx` | `sbatchscan` — one code per canonical field family | `101` gres/gpus, `102` qos, `103` account, `104` partition, `105` reservation, `106` nodes/tasks/cpus, `107` memory, `108` walltime, `109` array, `110` dependencies, `111` priority, `112` node selection/topology/constraints/licenses, `113` identity/env/export, `114` naming, `115` io paths, `116` cluster, `117` mail, `118` behavioural/misc options, `199` unknown directive |
| `CUSTOS2xx` | legacy import | `201` directive cannot be imported |
| `CUSTOS3xx` | `envcheck` | `301` controlled name, `302` generated name, `303` filtered name, `304` invalid name/value, `305` case variant of a filtered/controlled name (WARNING) |
| `CUSTOS4xx` | `softwareenv` | `401` unknown/unauthorized software |
| `CUSTOS5xx` | command scan (advisory) | `501` forbidden command |
| `CUSTOS9xx` | pipeline/infrastructure | `900` validator unavailable, `901` script exceeds limits |

The codes in the remainder of this document use this scheme.

Validators are deterministic given (`Script`, `Input` config, tool version);
the pipeline sorts diagnostics by (line, column, source, code) so the
persisted result is reproducible and diffable.

## Severity and `ValidationPolicy`

```yaml
# tenant or cluster scope; effective = most restrictive
validation:
  blockAt: ERROR                     # minimum severity that blocks submission (INFO|WARNING|ERROR)
  overrides:                         # per (source, code)
    - { source: shellcheck, code: SC2086, severity: WARNING }
    - { source: shellcheck, code: SC2046, severity: ERROR }   # escalate
    - { source: shellcheck, code: SC1091, severity: INFO }    # sourced file not followed — noise
  disabledCodes: [SC2034]            # only allowed for INFO/WARNING-origin codes
  shellcheckShell: bash
  allowShellTasks: false
  allowLegacySbatchImport: false
  forbiddenCommands: [sbatch, salloc]   # advisory scan → WARNING by default; see "Prohibited operations"
  forbiddenCommandsSeverity: WARNING     # admins may raise to ERROR
```

Hard floor, not configurable: `POLICY_VIOLATION` and `SECURITY_VIOLATION`
always block, cannot be disabled, downgraded, or overridden by users or
tenant admins. Only a platform admin can set cluster-scope policies; tenant
admins can only make tenant policy *stricter* than the cluster's (enforced
at write time by comparing effective severities).

## Script storage and integrity

- Payloads are stored in `scripts` (tenant-scoped, content-addressed):
  `(tenant_id, sha256) PRIMARY KEY, language, size, body BYTEA, created_by, created_at`.
  Limits (configurable, defaults): 256 KiB per script, 64 scripts per
  workflow version, 2 MiB total workflow source, 10 000 lines, 16 KiB per
  line. The digest is over the exact stored bytes: NUL bytes and invalid
  UTF-8 are rejected, nothing is normalized (CRLF is preserved and
  produces a `shsyntax` WARNING `CUSTOS010` because bash will choke on
  `\r`; the author fixes it).
- Tasks reference scripts by digest: `script: { ref: "sha256:...", language: bash }`
  inside the immutable `WorkflowVersion.spec`. The spec hash therefore
  covers every script digest. Publishing requires a current, valid
  `ScriptValidation` for every script-bearing task.
- At execution, the wrapper embeds the digest and verifies the payload on
  the compute node before executing it (see wrapper). Integrity is checked
  at three points: publish (validation ↔ digest), admission (spec ↔
  digest), runtime (bytes ↔ digest).

## Slurm directive scanner (`sbatchscan`)

Design goals: recognize what `sbatch` would recognize, plus a safety
margin; canonicalize before judging; never regex-only.

1. **Parse** the payload with `mvdan.cc/sh/v3/syntax` (`LangBash` or
   `LangPOSIX` per task language). Parse errors are already `ERROR`
   diagnostics from `shsyntax`; the scanner still runs a line-based
   fallback over raw lines so a syntactically broken script cannot hide
   directives.
2. **Collect comment nodes** with positions. A directive candidate is any
   comment whose text, after stripping the `#`, optional whitespace, and
   an optional `!`, begins with `SBATCH` (case-insensitive, to catch
   `#sbatch` which sbatch ignores but a future version or a site patch may
   not). Also flagged as candidates: `#SBATCH` appearing anywhere in a
   line that is *not* a comment (e.g. inside a heredoc or string) —
   `INFO CUSTOS011` because Slurm won't honor it but the author probably
   intended it.
3. **Placement awareness**: sbatch honors directives only before the first
   non-comment, non-blank line. The scanner records `honored: bool` for
   each candidate. Policy-controlled directives are rejected regardless of
   placement (defense in depth); unhonored ones carry a note.
4. **Tokenize** the directive remainder with shell-word splitting
   (quotes removed; **no expansion** — sbatch reads `$VAR` as literal
   text), matching sbatch's own `getopt_long` behaviour: `--opt=value`,
   `--opt value`, `-o value`, `-ovalue`, bundled short flags where
   sbatch permits, repeated options (last wins for scalars; accumulated
   for `--gres`, `--constraint` when Slurm merges them), `--opt` with
   optional argument, and unambiguous prefix abbreviations
   (`--parti=gpu` resolves to `--partition`; the diagnostic notes the
   abbreviation).
5. **Canonicalize** via a generated option table (`sbatchscan/options.go`,
   derived from sbatch's option list for Slurm 26.05 and checked by a test
   against `sbatch --help` output captured as a fixture per supported
   version). Each entry: long name, short alias(es), canonical `Field`,
   value kind, policy class. Examples:

   | Aliases | Field | Class |
   | --- | --- | --- |
   | `-A`, `--account` | `account` | controlled |
   | `-p`, `--partition` | `partition` | controlled |
   | `-q`, `--qos` | `qos` | controlled |
   | `--reservation` | `reservation` | controlled |
   | `-N`, `--nodes` | `nodes` | controlled |
   | `-n`, `--ntasks` | `tasks` | controlled |
   | `--ntasks-per-node` | `tasks_per_node` | controlled |
   | `-c`, `--cpus-per-task` | `cpus_per_task` | controlled |
   | `--mem`, `--mem-per-cpu`, `--mem-per-gpu` | `memory.*` | controlled |
   | `-G`, `--gpus`, `--gpus-per-node`, `--gpus-per-task`, `--gres`, `--tres-per-task` | `gres` | controlled |
   | `-C`, `--constraint`, `--prefer` | `constraints` | controlled |
   | `-L`, `--licenses` | `licenses` | controlled |
   | `-t`, `--time`, `--time-min`, `--deadline` | `walltime.*` | controlled |
   | `-a`, `--array` | `array` | controlled |
   | `-d`, `--dependency` | `dependencies` | controlled |
   | `--nice`, `--priority` | `priority` | controlled |
   | `--exclusive`, `--oversubscribe` | `exclusive` | controlled |
   | `-w`, `--nodelist`, `-x`, `--exclude`, `-F`, `--nodefile` | `node_selection` | controlled |
   | `--switches`, `--network`, `--bb`, `--bbf` | `topology/burst_buffer` | controlled |
   | `-M`, `--clusters` | `cluster` | controlled |
   | `--uid`, `--gid`, `--wckey`, `--export`, `--get-user-env`, `--propagate` | `identity/env` | controlled |
   | `-J`, `--job-name`, `--comment` | `naming` | controlled (Custos uses these for correlation) |
   | `-o`, `--output`, `-e`, `--error`, `-i`, `--input`, `-D`, `--chdir` | `io_paths` | controlled (path policy) |
   | `--mail-type`, `--mail-user` | `mail` | controlled (disabled) |
   | anything unrecognized | `unknown` | controlled (reject: unknown scheduler directive) |

   Every sbatch option is classified `controlled` in v1: there is no
   directive a payload may legitimately carry, because Custos supplies the
   whole envelope. The table still exists so import mode can *convert*
   recognized options and so error messages can name the Custos field.
6. **Judge**: default policy → every honored-or-not directive yields
   `SECURITY_VIOLATION CUSTOS101..CUSTOS199` (one code per field family)
   with the offending line and the `Fix` hint pointing to the Resources
   panel. Conflicting or duplicate directives are each reported;
   nothing is "resolved" by the scanner in reject mode.

Also scanned (INFO/WARNING, never silently honored): `#PBS`, `#$` (SGE),
`#BSUB`, `#COBALT` — signals a script ported from another scheduler.

Environment-based bypasses are closed structurally: `SBATCH_*` input
environment variables are honored by the `sbatch` CLI, not by slurmrestd
job submission, and are in the protected env class regardless (see
Environment).

## Legacy import mode

Enabled per tenant by `allowLegacySbatchImport: true` **and** requested
explicitly by the author (`POST .../tasks/{task}/import-sbatch`). Steps:

1. Run the scanner; convert `controlled` directives with a known mapping
   into a `workflowspec.Resources` + placement fragment
   (`--nodes=4 --gres=gpu:h100:4 --time=04:00:00` →
   `resources: {nodes: 4, gpu: {type: h100, count: 4}, walltime: 4h}`).
   Unmappable directives (`--uid`, `--nodelist`, `--export`, mail, ...)
   are reported as `ERROR CUSTOS201 directive cannot be imported` and left
   for the author to delete.
2. Return the **proposed** resource request and a **rewritten payload** in
   which each directive line is replaced by `# [custos] imported: <original>`
   (same line count → diagnostics still align). Nothing is saved yet.
3. The author reviews and saves; the rewritten payload becomes a new
   script digest, the resources go into the task definition, and the
   normal validate → policy → admission chain runs. Import output is a
   *request*; policy may deny it exactly as if typed into the panel.
4. Audit `workflow.script.import` with digests before/after and the list
   of imported fields (not the script body).

## `ExecutionSpec` and admission

```go
package admission

type ExecutionSpec struct {
    SchemaVersion   int
    ID              uuid.UUID          // = TaskExecution attempt id
    TenantID, ProjectID, PrincipalID uuid.UUID
    WorkflowVersionID uuid.UUID
    TaskName        string
    Attempt         int

    Cluster         ClusterRef         // id + name + api version used
    Account         string             // from ProjectClusterBinding
    Partition, QoS, Reservation string

    Resources       Resources          // fully resolved, units normalized (MiB, seconds)
    Placement       PlacementDecision  // engine output + reason

    Software        []ResolvedSoftware // {Name, Version, ModuleSpec} from the cluster catalog
    Environment     EnvSet             // classified: controlled / user / secret-refs (refs only)

    Payload         PayloadRef         // {ScriptID, Digest, Language, Interpreter}
    Inputs, Outputs []ArtifactRef
    WorkingDir      string             // validated against project path policy
    Stdout, Stderr  string

    Security        SecurityContext    // {SlurmUser, ImpersonationMode, ShellTask bool, WrappedTokenRefs}

    Digest          Digest             // sha256 over canonical JSON of everything above
    AdmittedAt      time.Time
    AdmittedBy      string             // "admission/v1", policy versions, allocation snapshot id
}
```

The builder is a fixed sequence of pure functions, each returning a typed
denial:

```text
authn → authz(job.submit on project) → ResourcePolicy (limits, allowed partitions/qos, shell)
 → Entitlement (project ↔ cluster binding exists; account; QoS list) → Allocation (budget remaining)
 → Admission (cluster capabilities: partition exists, GRES type exists, within partition limits, array size)
 → Placement (explicit cluster or PlacementEngine) → ExecutionSpec (freeze + digest)
```

`ExecutionSpec` is persisted on `task_executions.execution_spec` (JSONB) +
`execution_spec_digest`. It is never updated. The API exposes it as the
read-only **submission preview** (`GET .../tasks/{task}/execution-spec`),
including a dry-run form during authoring
(`POST .../versions/{v}/tasks/{task}/preview-submission`) that runs the
same builder without persisting or reserving allocation. The UI renders
the preview from this response and nothing else.

## Wrapper and envelope generation (`internal/submission`)

`JobSubmission` (see `docs/slurm.md`) is a pure function of
`ExecutionSpec`. Envelope fields map to structured slurmrestd
`JobDescMsg` fields; the `script` field is the wrapper below. The wrapper
is generated from a Go `text/template` whose data struct contains only
values already validated by admission; every value interpolated into a
shell context goes through a single-quote escaper, and the payload itself
is never interpolated as text.

```bash
#!/bin/bash
# custos wrapper v1 — generated; do not edit
# execution: {{ .ExecutionID }} digest: {{ .SpecDigest }}
set -euo pipefail
umask 077
export CUSTOS_EXECUTION_ID={{ q .ExecutionID }}
export CUSTOS_TASK={{ q .TaskName }}
export CUSTOS_JOB_DIR="${SLURM_TMPDIR:-${TMPDIR:-/tmp}}/custos-${SLURM_JOB_ID}"
mkdir -p "$CUSTOS_JOB_DIR"
trap 'rm -rf "$CUSTOS_JOB_DIR"' EXIT
{{ range .Modules }}module load {{ q . }}
{{ end }}{{ range .Env }}export {{ .Name }}={{ q .Value }}
{{ end }}
# --- payload (base64, verified) ---
base64 -d > "$CUSTOS_JOB_DIR/payload" <<'CUSTOS_PAYLOAD_{{ .Nonce }}'
{{ .PayloadBase64 }}
CUSTOS_PAYLOAD_{{ .Nonce }}
echo {{ q .PayloadDigest }}"  $CUSTOS_JOB_DIR/payload" | sha256sum -c --quiet
chmod 0500 "$CUSTOS_JOB_DIR/payload"
cd {{ q .WorkingDir }}
{{ if .MPI }}exec srun --ntasks={{ .Tasks }} {{ q .Interpreter }} "$CUSTOS_JOB_DIR/payload" {{ range .Args }}{{ q . }} {{ end }}
{{ else }}exec {{ q .Interpreter }} "$CUSTOS_JOB_DIR/payload" {{ range .Args }}{{ q . }} {{ end }}{{ end }}
```

Properties:

- The wrapper contains **no `#SBATCH` lines** and the first non-comment
  line appears before any user-derived bytes, so neither sbatch-style
  directive processing nor slurmrestd could pick directives out of the
  payload even if it were plain text — and it isn't: base64 makes the
  payload inert to shell parsing and heredoc-delimiter collisions
  (the delimiter is additionally checked against the encoded body).
- `{{ q }}` is the only interpolation function; the template is rejected
  at build time (unit test) if any `{{ . }}` appears without `q` except
  for fields typed as `SafeToken` (validated by regex at admission:
  env var names, digests, nonces, interpreter enum).
- The digest check on the node is the runtime leg of integrity.
- `module load` values come from `ResolvedSoftware.ModuleSpec` produced by
  the software catalog, never from user strings.
- `Interpreter` is an enum per language (`/bin/bash`, `/bin/sh`,
  `python3` via the resolved software env), not user-specified.
- Payload size in the wrapper is bounded by the 256 KiB script limit
  (~350 KiB base64); slurmrestd/sbatch accept scripts far larger. When
  the artifact/storage layer lands, large payloads move to staged files
  and the heredoc is replaced by a path + digest check.

## Environment variables (`envcheck`)

Slurmrestd job submission takes an explicit `environment` array; Custos's
own process environment is never consulted. Classes:

| Class | Source | Names | Rule |
| --- | --- | --- | --- |
| Controlled | Custos | `CUSTOS_*`, `SLURM_*`, `SBATCH_*`, `SRUN_*`, `SALLOC_*`, `PATH`(base), `HOME`, `USER`, `TMPDIR` | set by Custos/Slurm; user values rejected `CUSTOS301` |
| Generated | Software catalog | vars from `ResolvedSoftware` (e.g. `OMPI_MCA_*` defaults) | user cannot override `CUSTOS302` |
| Secret-injected | `SecretReference` | declared `secrets.<handle>` using env or wrapped-token mode; only reference UUID/name/mode/handle enter `ExecutionSpec`; values are resolved directly into the scheduler request | |
| Filtered | policy | default-denied names; tenant policy may allow specific ones: `LD_PRELOAD`, `LD_AUDIT`, `LD_LIBRARY_PATH`, `LD_DEBUG*`, `GLIBC_TUNABLES`, `BASH_ENV`, `ENV`, `PROMPT_COMMAND`, `SHELLOPTS`, `PYTHONSTARTUP`, `PYTHONHOME`, `PERL5OPT`, `RUBYOPT`, `NODE_OPTIONS`, `JAVA_TOOL_OPTIONS`, `*_PROXY`/`*_proxy`, `CUDA_VISIBLE_DEVICES`, `ROCR_VISIBLE_DEVICES`, `OMPI_*`, `PMIX_*`, `I_MPI_*`, `UCX_*`, `NCCL_*` | `CUSTOS303 variable is filtered by policy` |
| User-configurable | task `env` | everything else | name `^[A-Za-z_][A-Za-z0-9_]*$`, ≤ 128 chars; value ≤ 32 KiB, no NUL/CR/LF; total ≤ 256 KiB |

Rationale for filtering MPI/CUDA/NCCL vars *by default*: they can silently
change binding, transport and device visibility across a shared node; sites
that want users to tune them enable them per tenant. `SLURM_*` is not
"forbidden" wholesale — Slurm sets those at runtime and payloads read them
freely; what's rejected is a user *pre-setting* them in the submission
environment, which can alter srun/step behavior (`SLURM_NTASKS`,
`SLURM_CPUS_PER_TASK`, `SLURM_EXPORT_ENV`, ...).

Inherited: nothing. `--export=NONE` semantics are the default; the
wrapper sets what `ExecutionSpec.Environment` says.

## Software environments (`softwareenv`)

Structured requirement in the task: `software: [{name: openmpi, version: "5.0.7"}, {name: hdf5, version: "1.14"}]`.
Resolution against a per-cluster **catalog** (`software_catalog` rows
synced by `cluster.sync` from a site-provided manifest or a future
Lmod/Spack export): name+version constraint → `ModuleSpec` (exact module
string, dependency modules, generated env). Unknown name/version →
`CUSTOS401 unauthorized or unknown software`. Users never type module
strings into a structured field; if a payload itself runs `module load
$(…)`, that's payload code and is subject to the same runtime controls as
any other command — the distinction is what Custos *vouches for*.

## Prohibited operations (advisory) and the enforcement boundary

Custos performs a **command scan** over the parsed AST (`syntax.Walk` over
`CallExpr` first words, including within functions, subshells, pipes,
`eval`/`bash -c` arguments where literal): matches against
`forbiddenCommands` produce `WARNING CUSTOS501` (or `ERROR` if the admin
escalates). This is intentionally not called a control:

> Static scanning of shell source cannot prevent a hostile payload from
> invoking `sbatch`/`salloc`/`srun --more-resources`. Enforcement of
> "consume only what you were granted" lives in Slurm and the OS.

Custos's stance on nested scheduler commands, documented for operators:

| Threat | Native control (required for hostile-workload sites) | Custos contribution |
| --- | --- | --- |
| `srun` inside a job asking for more than allocated | Slurm rejects steps exceeding the allocation; `cgroup` plugin with `ConstrainCores/ConstrainRAMSpace/ConstrainDevices` | none needed; verifies cluster reports `cgroup` in capabilities and warns otherwise |
| `sbatch`/`salloc` from inside a job as the same user | association/QoS limits (`MaxJobs`, `MaxSubmitJobs`, `MaxTRESPerJob`, `GrpTRES`), `job_submit` plugin (e.g. Lua) rejecting jobs not carrying the Custos comment/marker from users designated Custos-only, partition `AllowAccounts`, `AllowQOS` | `policy.sync` (M7+) writes binding limits to slurmdbd associations via the Accounting API so Custos policy has a native shadow; CUSTOS501 advisory |
| User holds their own Slurm credentials (ssh to login node, `sbatch` directly) | Site decision. If users can `sbatch` directly, Custos is a convenience layer for them, not a boundary; the tenant's limits must live in associations | Documented as the primary residual risk; `docs/threat-model.md` TM-32 |
| Payload reads another job's secrets on a node | `PrivateData`, `cgroup` device/namespace isolation, per-job `TMPDIR` | wrapped single-use tokens, per-job dir with `umask 077` |

## Validation sandboxing

Go-native validators (`shsyntax`, `sbatchscan`, `envcheck`,
`softwareenv`) run in-process: pure Go parsers with fuzz tests, bounded
input, and a context timeout. External tools (ShellCheck, later
ruff/`python -m ast`) run in a **validator sidecar**:

```text
custos worker pod
  ├─ container: custos (worker)  — has DB + OpenBao credentials
  └─ container: custos-validator — NO credentials, no ServiceAccount token,
                                    read-only rootfs, non-root, no network
                                    (NetworkPolicy + no egress), emptyDir /tmp,
                                    CPU/memory limits, pids limit,
                                    listens on localhost:8481 only
```

Protocol: `POST /v1/shellcheck` with `{shell, script}` (bounded to the
script limit), returns ShellCheck's `--format=json1` output; the sidecar
runs `shellcheck` under a per-invocation `timeout`, `ulimit -v/-u`, in a
fresh temp dir, and never writes outside it. The worker maps codes to
`Diagnostic`. For non-Kubernetes deployments the same binary runs as a
separate systemd service with `ProtectSystem=strict`, `PrivateNetwork=yes`,
`DynamicUser=yes`. Fallback when the sidecar is unavailable: `CUSTOS900`
fail-closed diagnostic (configurable to WARNING for dev only).

ShellCheck choice: mature, the de-facto shell linter, JSON output with
line/column/end positions. **License note**: ShellCheck is GPLv3; Custos
(Apache-2.0) invokes it as a separate process/container and never links
it, which is a standard, compliant integration. Alternatives: none of
comparable coverage; `mvdan/sh` provides parsing and formatting, not
lint rules, so both are used.

Python (later): `ruff` (MIT, single static binary) for lint and
`python -c "import ast, sys; ast.parse(...)"` for syntax, both inside the
sidecar; **no execution of user code** — `ast.parse` does not execute.

## API

```text
POST /api/v1/tenants/{t}/workflows/{w}/versions/{v}/tasks/{task}/validate
POST /api/v1/tenants/{t}/workflows/{w}/versions/{v}/tasks/{task}/import-sbatch
POST /api/v1/tenants/{t}/workflows/{w}/versions/{v}/tasks/{task}/preview-submission
GET  /api/v1/tenants/{t}/workflows/{w}/versions/{v}/validations
GET  /api/v1/tenants/{t}/workflow-executions/{e}/tasks/{task}/validation
GET  /api/v1/tenants/{t}/workflow-executions/{e}/tasks/{task}/execution-spec
POST /api/v1/tenants/{t}/scripts/validate        # ad-hoc, for the editor before a task exists
```

`.../validate` request/response:

```json
{ "language": "bash", "script": "…", "resources": {…}, "environment": {…}, "software": […] }
```

```json
{
  "valid": false,
  "digest": "sha256:…",
  "blockAt": "ERROR",
  "diagnostics": [
    { "source": "shellcheck", "code": "SC2086", "severity": "WARNING", "line": 17, "column": 8, "endLine": 17, "endColumn": 14, "message": "Double quote to prevent globbing and word splitting." },
    { "source": "sbatchscan", "code": "CUSTOS101", "severity": "SECURITY_VIOLATION", "line": 4, "column": 1, "field": "gres", "message": "Scheduler resource directives must be configured through the Custos resource panel.", "fix": "Configure GPUs using Resources → GPU" }
  ],
  "toolVersions": { "shellcheck": "0.11.0", "shsyntax": "mvdan.cc/sh/v3 v3.x", "sbatchscan": "slurm-26.05" },
  "effectivePolicy": { "blockAt": "ERROR", "allowShellTasks": false, "allowLegacySbatchImport": false, "filteredEnvAllowed": [] }
}
```

`effectivePolicy` exposes only what the author needs to understand the
result; numeric limits are surfaced as diagnostics on the fields they
affect, not dumped wholesale.

Controls on these endpoints: authenticated; `workflow.create` (or
`workflow.read` for `GET`s); body limit = script limit + 64 KiB; per-principal
rate limit (default 30/min, burst 10) separate from the general bucket;
validator work runs with a 20 s total budget; results are cached by
(`digest`, `policyVersion`, `resourcesHash`) for 10 minutes so
debounced retries are cheap.

## Persistence

```sql
scripts(tenant_id, sha256 BYTEA, language, size_bytes, body BYTEA, created_by, created_at,
        PRIMARY KEY (tenant_id, sha256))
script_validations(id, tenant_id, workflow_version_id NULL, task_name, script_digest, language,
        valid, diagnostics JSONB, tool_versions JSONB, policy_version, validated_at, expires_at)
        INDEX (tenant_id, script_digest, policy_version)
validation_policies(id, scope_kind, scope_id, version, body JSONB, updated_by, updated_at)
task_executions(... execution_spec JSONB, execution_spec_digest BYTEA, script_validation_id ...)
software_catalog(cluster_id, name, version, module_spec JSONB, env JSONB, synced_at)
```

## Audit

Events: `workflow.script.validate` (result, blocking codes, fields;
digest; version/task ids), `workflow.script.import`,
`workflow.task.admit` (result, denial stage + field), `job.submit`
(execution spec digest). Payload bodies are never in audit; they live in
`scripts`. Denials at `SECURITY_VIOLATION` also increment
`custos_validation_security_violations_total{field}` (bounded label set =
canonical fields).

## Frontend (Monaco) — Milestone 8

- `@monaco-editor/react` with languages `shell`, `python`, `yaml`, `json`
  registered; a language registry in `web/lib/editor/languages.ts` so
  adding PowerShell later is one file.
- Task editor layout: Monaco left, **Resources panel** right, **Problems**
  panel bottom (mirrors the design in the brief). Resources panel binds to
  the task's structured `resources`/`software`/`env`; the editor binds to
  the payload. The two are visibly labelled "Workload logic" and
  "Scheduler configuration".
- Diagnostics: local (Monaco's built-in tokenization, a lightweight
  client-side `#SBATCH` highlighter that shows a *hint* immediately) plus
  backend results from the validate endpoint (debounced 600 ms, in-flight request
  aborted on new edits, response ignored if digest ≠ current) mapped to
  `monaco.editor.setModelMarkers` with severity mapping
  `INFO→Hint, WARNING→Warning, ERROR/POLICY/SECURITY→Error` and the code
  shown as `CUSTOS101` with a link to docs. Clicking a Problems row calls
  `revealLineInCenter` + sets the selection.
- Snippets: `#!/bin/bash` header with `set -euo pipefail`, `srun` step,
  array-index loop, module loads (inserted as structured software instead
  where possible), stage-in/out placeholders.
- Read-only **Submission preview** tab renders `.../preview-submission`
  (the backend `ExecutionSpec` projection) — including the generated
  wrapper text, so users see exactly what Slurm receives.
- Legacy import: "Import #SBATCH directives" action appears only when the
  tenant policy allows it and the scanner found directives; shows the
  proposed resource diff before applying.

## Testing

Mandatory suites (M4 for scanner/admission/wrapper/env, M5 for pipeline
persistence, M8 for UI):

1. **Directive bypass corpus** (`sbatchscan/testdata/bypass/*.sh`, table
   test asserting `SECURITY_VIOLATION` with the expected canonical field):
   `--gres=gpu:8`, `--gres gpu:8`, `-G8`, `-G 8`, `--gpus=8`,
   `--gpus-per-node=8`, `--tres-per-task=gres/gpu:8`, `--qos=admin`,
   `--qos admin`, `-q admin`, `-qadmin`, `--account=other`, `-A other`,
   `-Aother`, `--partition=restricted`, `-p restricted`, `-prestricted`,
   `--reservation=x`, `-N64`, `--nodes=64`, `--mem=0`, `--mem-per-gpu=…`,
   `--time=7-00:00:00`, `-t 7-0`, `--licenses=…`, `--nodelist`, `-w`,
   `--exclusive`, `--clusters`, `--uid`; whitespace variants (tabs,
   multiple spaces, trailing spaces), CRLF, `#SBATCH` after blank lines,
   after other comments, after the first command (unhonored but still
   rejected), lower-case `#sbatch`, `# SBATCH` (space), `#!SBATCH`,
   quoted values `--qos="admin"`, `--qos='admin'`, duplicates,
   conflicting (`-p a` then `-p b`), directives inside heredocs and
   strings (INFO), Unicode homoglyph in option name (unknown → reject),
   zero-width characters, BOM at file start, 16 KiB line, 10 000
   directive lines (performance bound), NUL byte (rejected before
   parsing).
2. **Canonicalization**: for every aliased option, all forms produce the
   same `Field` and value.
3. **Resource tampering (end-to-end with fake Slurm)**: policy max
   GPUs/job = 4; task requests 4; payload contains `#SBATCH --gres=gpu:8`
   → reject mode: no submission, `SECURITY_VIOLATION`; import mode:
   request becomes 8 → `POLICY_VIOLATION`, no submission. Assert the fake
   Slurm received **zero** submissions. Repeat for qos, account,
   partition, reservation, nodes, memory, walltime, licenses.
4. **Invariant tests**: `JobSubmission` built from an `ExecutionSpec`
   whose payload contains every bypass string still carries exactly the
   spec's values (property test over random specs); wrapper never
   contains `#SBATCH` regardless of payload; payload bytes round-trip
   through base64 and the embedded digest matches; template contains no
   unquoted interpolation (static test).
5. **Env**: protected/filtered names rejected in all case variants; name
   validation; value size limits; secrets never present in the spec JSON
   (property: marshal spec, assert no secret values).
6. **Validation freshness**: change policy version → admission triggers
   re-validation; tamper `scripts.body` in the test DB → admission fails
   with digest mismatch.
7. **Sandbox**: sidecar fuzzed with malformed scripts; timeout enforced;
   no network (test asserts connection attempts fail) — integration, CI.
8. **Fuzz**: `FuzzScan`, `FuzzTokenizeDirective`, `FuzzEnvName` with the
   Go native fuzzer, run in CI for a bounded time.

## Milestone placement

| Milestone | Delivered |
| --- | --- |
| M4 Jobs (delivered) | `scripts` storage + digests; `shsyntax`; `sbatchscan` (reject mode); `envcheck`; `admission.ExecutionSpec`; `submission` wrapper; invariant + bypass tests; the secure job submission path (`job.submit/reconcile/cancel`, `jobs.sweep`). A batch job cannot be submitted without them. Until M5 persists `ValidationPolicy`, the submission path applies the hardcoded default `{BlockAt: ERROR}` — `POLICY`/`SECURITY` severities are immutable regardless. |
| M5 Workflows (delivered) | Durable validation pipeline (`internal/validation/pipeline`) with persisted `script_validations`; `validation_policies` at tenant + cluster scope (optimistic version, stricter-than-cluster merge, per-scope `shellcheckShell`, `allowShellTasks`, `unavailable_severity`); the loopback-only ShellCheck sidecar (`custos-validator`, `POST /v1/shellcheck` → `{"version","result"}`); ad-hoc `scripts/validate` + `scripts/import-sbatch` routes and validation-policy routes; task-level `validate`, `import-sbatch` and `preview-submission` endpoints on workflow versions; `ADMITTING` realized as the `task.admit` work item (currency check → `ExecutionSpec` freeze → linked `jobs` row + `job.submit` in one transaction); the publish gate (`Publish` 422s when any task lacks a current `ScriptValidation`, including after draft edits — currency is keyed by `input_hash`, an FNV-64a of the canonical validation input persisted on `script_validations`); `jobs.script_validation_id`. Implementation refinements: `ScriptValidation.PolicyVersion` is the FNV-64a fingerprint of the effective policy (canonical JSON); `disabledCodes` may not contain any `CUSTOS*` code because Custos-origin severity is known and never disableable; the pipeline keeps a 10-minute, 1024-entry cache keyed by (tenant, digest, policy fingerprint, input hash); a hit reuses the diagnostics but mints a fresh `ScriptValidation` (new ID, request's `WorkflowVersionID`/`TaskName`) so each persisted row is its own run. `unavailable_severity` only softens external (sidecar) validators; in-process validator failures always stay ERROR. **Not delivered in M5** (M8): the ValidationPolicy UI — policies are API-only today. |
| M6 Secrets (delivered) | Tenant connectors and SecretReferences; publish/execute/ad-hoc authorization; metadata-only `Environment.SecretRefs` and `Security.WrappedTokenRefs`; submit-time env resolution with wiping/retry semantics; platform-OpenBao response-wrapped, per-reference child tokens; value-free preview/audit/metrics. |
| M7 Accounting/Policy | `softwareenv` catalog sync; `policy.sync` to slurmdbd associations |
| M8 UI | Monaco editor, Resources/Problems panels, preview tab, import UX |
| Later | Python (`ruff`/AST), PowerShell, staged payload files, external PDP hooks in admission |
