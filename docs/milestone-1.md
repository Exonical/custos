# Milestone 1 — Project Foundation: Implementation Plan

Goal: a runnable, observable, testable `custos` binary with the HTTP skeleton,
database, migrations, and CI in place — and **no domain features**. Every
later milestone plugs into these seams.

## Deliverables

1. **Module & tooling**
   - `go.mod` (`module github.com/<org>/custos`, `go 1.27`).
   - `Makefile`/`Taskfile`: `build`, `test` (`go test -race ./...`), `lint`
     (`golangci-lint`), `vet`, `vuln` (`govulncheck`), `migrate`, `openapi-check`.
   - `.golangci.yml`: `govet`, `staticcheck`, `errcheck`, `gosec`, `revive`,
     `forbidigo` (forbid `fmt.Print*`, `log.Print*`, logging of `http.Header`),
     `depguard` (domain packages may not import `net/http`, `pgx`, slinky, openbao).
   - GitHub Actions: lint, test with PostgreSQL service, govulncheck,
     container build, secret scanning (gitleaks), `openapi-check`.

2. **`cmd/custos`**
   - Subcommands via `flag` + a tiny dispatcher: `serve`, `worker` (stub that
     starts the queue loop with no handlers), `migrate {up|down|status}`,
     `version`.
   - Signal handling; `errgroup`-based lifecycle; graceful shutdown with
     configurable timeout (default 20s).

3. **`internal/platform/config`**
   - Sources: config file (YAML) → env overrides (`CUSTOS_` prefix, `__`
     nesting). Secrets fields accept `_FILE` suffix variants pointing at
     mounted files. No secret values in env by default.
   - `Validate()` fails closed: HTTPS required unless `dev_mode: true`,
     OIDC block validated (even though authn ships in M2, the config schema
     lands now), DB DSN required, listener addresses parse.
   - `Redacted()` for logging the effective config.

4. **`internal/platform/log`** — `slog` JSON handler, level from config,
   `request_id`/`trace_id` injection from context, `Redacted` string type.

5. **`internal/platform/db`**
   - `pgxpool` with TLS config, statement timeout, pool limits from config.
   - `WithTx(ctx, fn)` helper; `Tenant scope` hook placeholder that will
     `SET LOCAL app.tenant_id` in M2.
   - Migration runner using `pressly/goose/v3` with `embed.FS` over
     `migrations/`. Migration `0001_init.sql`: `schema_migrations` (goose's),
     `work_items`, `idempotency_keys`, `audit_events` (partitioned parent +
     first partitions) — infrastructure tables only.
   - Test helper: testcontainers-go PostgreSQL 16, one database per test
     package, migrations applied, `TRUNCATE` between tests.

6. **`internal/platform/httpx`**
   - Middleware: recover (→ `INTERNAL` envelope, logs panic with request id),
     request-id (validate incoming `X-Request-ID` as ≤128 printable chars
     else generate UUIDv7), real-ip from trusted proxy CIDRs only,
     security headers, CORS (deny-by-default), body limit, per-route timeout,
     access log, otelhttp.
   - Error envelope type + `WriteError(ctx, w, err)` mapping domain error
     kinds (`NotFound`, `Invalid`, `Conflict`, `Forbidden`, `Unauthenticated`,
     `Unavailable`) to HTTP.
   - Server construction with `ReadHeaderTimeout`, `IdleTimeout`, TLS 1.3
     min, optional mTLS for the metrics listener.

7. **`internal/api`**
   - `openapi.yaml` v0.1 containing: `/api/v1/openapi.json`, `/health/live`,
     `/health/ready`, error schema, list envelope, `Idempotency-Key` header
     component, common parameters. `oapi-codegen` config producing
     `pkg/api/v1/types.gen.go` and a strict server interface; `make
     openapi-check` diffs regenerated output.
   - Handlers for the three endpoints above.

8. **Health**
   - `/health/live` trivially 200.
   - `/health/ready` runs registered `Checker`s (`db` required) with a
     500ms budget each; JSON body `{status, checks:[{name,status,latency_ms}]}`.

9. **`internal/platform/otel`**
   - Resource attributes (service name, version, instance).
   - Prometheus exporter registered on a **separate listener**
     (`metrics.listen: :9090`) by default; OTLP trace exporter optional
     (`otel.exporter: none|otlp`), sampling ratio config.
   - `custos_build_info`, HTTP metrics via otelhttp with route templates
     (a small wrapper sets `http.route` from `r.Pattern`).

10. **`internal/workqueue`** (foundation only)
    - Schema, `Enqueue` (with dedupe key), lease loop, backoff, heartbeat,
      dead-lettering, `Handler` registry, per-kind concurrency. One built-in
      handler: `maintenance.noop` used for tests; `maintenance.partitions`
      to keep `audit_events` partitions ahead.
    - Tests: concurrent workers on one table (`-race`), lease expiry
      recovery, backoff schedule, dedupe, dead-letter after N attempts.

11. **`internal/audit`** (foundation only)
    - `Event` struct, `Recorder` port, PostgreSQL sink writing append-only
      rows with `prev_hash` chaining per stream; a `slog`-based forwarder
      stub. No emitters yet beyond a `system.started` event.

12. **Containers & deploy**
    - Multi-stage `Containerfile` (distroless `static` or `scratch`,
      non-root UID, read-only FS compatible: no writes outside `/tmp`).
    - Helm chart skeleton: `Deployment` (serve), `Deployment` (worker),
      `Job` (migrate, pre-install/upgrade hook), `Service`,
      `ServiceMonitor` (optional), `NetworkPolicy`, `PodSecurityContext`
      (runAsNonRoot, readOnlyRootFilesystem, drop ALL caps), resource limits.

13. **Docs** — `docs/configuration.md` generated from the config struct
    tags (`make docs-config`), README quick start with `docker compose`
    (PostgreSQL + custos).

## Order of work (each step ends green: build, lint, `go test -race`)

1. go.mod, Makefile, lint config, CI (empty test passes).
2. config + log + `custos version`.
3. db pool + goose runner + `0001_init.sql` + testcontainers harness.
4. httpx middleware + error envelope + server lifecycle + `serve` with
   health endpoints; tests for every middleware.
5. OpenAPI document + codegen + `openapi-check`.
6. otel + metrics listener + build_info; test that `/metrics` contains
   expected series and no forbidden label names (a test-time cardinality
   linter that scans registered descriptors — this stays as a permanent test).
7. workqueue with tests.
8. audit sink with hash chain test.
9. Containerfile + Helm skeleton + compose file; smoke test in CI: build
   image, run migrate, run serve, curl `/health/ready`.

## Definition of done

- `make lint test vuln` clean; `go test -race ./...` green with a real
  PostgreSQL in CI.
- `custos migrate up && custos serve` works locally via compose;
  `/health/ready`, `/metrics`, `/api/v1/openapi.json` respond.
- Config validation tests prove fail-closed behavior for each insecure
  setting.
- Work queue tests prove lease recovery and dedupe under `-race`.
- No domain tables beyond infrastructure ones; no authentication yet
  (endpoints in M1 are unauthenticated **and** contain no tenant data —
  documented explicitly so M2 must add authn before any domain endpoint).

## Dependencies introduced in M1 (each justified)

| Module | Why | Std-lib alternative rejected because |
| --- | --- | --- |
| `github.com/jackc/pgx/v5` | PostgreSQL driver + pool | `database/sql` loses pg types/perf; pgx is the de-facto driver |
| `github.com/pressly/goose/v3` | SQL migrations, embed support | hand-rolled runner is ~200 lines but goose's edge cases (dirty state, versioning) are worth it |
| `go.opentelemetry.io/otel/*`, `otelhttp`, Prometheus exporter | traces + metrics | required by spec |
| `github.com/prometheus/client_golang` (via exporter) | `/metrics` | required by spec |
| `github.com/oapi-codegen/oapi-codegen/v2` (tool), `oapi-codegen/runtime` | types/server from OpenAPI | hand-maintaining DTOs drifts from the contract |
| `github.com/testcontainers/testcontainers-go` (+postgres module) | real PG in tests | mocks can't test SQL/RLS |
| `golang.org/x/sync/errgroup` | structured lifecycle | std-lib has no errgroup |
| `github.com/google/uuid` | UUIDv7 | std-lib lacks UUID; small, ubiquitous |
| `gopkg.in/yaml.v3` (or `goccy/go-yaml`) | config + later workflow YAML | std-lib has no YAML |

Deferred to M2+: `coreos/go-oidc/v3`, `SlinkyProject/slurm-client`,
`openbao/openbao/api/v2`.
