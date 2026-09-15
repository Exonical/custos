# ADR-008: Observability — OTel SDK, Prometheus on a separate listener, slog JSON

Status: Accepted
Date: 2026-09-14

## Context

Custos needs operational telemetry (is Custos/a cluster healthy now?)
separate from analytics/accounting (what did a user/project/tenant
consume?) — the two have different stores, cardinality limits, and
serving paths (docs/observability.md). Prometheus cardinality explosion
is a named risk (docs/architecture.md §10); secret leakage through
logs/metrics is TM-13.

## Decision

- **Traces & metrics**: OpenTelemetry Go SDK. `/metrics` is served by the
  Prometheus exporter on a **separate listener** (`metrics.listen`,
  default `:9090`, optional mTLS); OTLP exporter sends traces (and
  optionally metrics) to a collector, configurable `none|otlp`.
- Instrumentation: `otelhttp` for HTTP with route-template span names
  (`http.route` from `r.Pattern`, never raw path); a `pgx` tracer hook
  emitting statement names, not full SQL.
- **Strict label allow-list**: `cluster`, bounded `partition`, `tenant`
  only on a few platform counters (disable-able above ~500 tenants),
  `kind`, `state`, `result`, templated `route`, `method`, `status_class`.
  Never `user`, `job_id`, `execution_id`, `workflow`, raw path. A
  permanent CI test scans registered descriptors for forbidden labels
  (docs/milestone-1.md order-of-work step 6).
- **Logs**: `slog` JSON to stdout with `request_id`, `trace_id`,
  `span_id`, `tenant_id`, `principal_id` (UUID not email). `secrets.Value`
  and `authn.Token` self-redact via `LogValuer`; lint (`forbidigo` or a
  custom vet analyzer) forbids logging `http.Header` and raw bodies.
- **Analytics in PostgreSQL, not Prometheus**: usage/accounting lives in
  `usage_records`/`usage_daily`/`jobs`; cluster metric series are also
  persisted as `cluster_snapshots` (5-min resolution, 90-day retention)
  so the UI charts history without querying Prometheus.
- Health: `/health/live` trivially cheap; `/health/ready` checks DB
  (required), OpenBao token (degraded), JWKS loaded (required for serve);
  cluster reachability never gates readiness. 200 ready-or-degraded, 503
  on required-component failure.

## Alternatives considered

- **Prometheus client_golang directly** — rejected as the primary SDK:
  OTel gives traces + metrics in one SDK with OTLP export; the Prometheus
  exporter still serves `/metrics`.
- **StatsD/Datadog-style agents** — rejected: OTel is the standard and
  vendor-neutral.
- **High-cardinality Prometheus metrics for usage analytics** — rejected:
  that is what the PostgreSQL accounting pipeline is for.
- **Metrics on the main listener** — rejected: separate listener avoids
  exposing metrics on the API surface and allows separate access control.

## Consequences

- Label discipline is enforced by code review plus the descriptor-scanning
  test, not by convention alone.
- Traces propagate into work items via `payload.trace_context`.
- Operators get history charts from `cluster_snapshots` even without
  Prometheus deployed.

Source: docs/observability.md (all sections); docs/milestone-1.md §9.
