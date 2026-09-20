# Observability, Metrics and Accounting

## Two different things

| | Operational metrics | Analytics / accounting |
| --- | --- | --- |
| Purpose | Is Custos / a cluster healthy right now? | What did user/project/tenant consume? |
| Store | Prometheus (scraped `/metrics`) | PostgreSQL (`usage_records`, `usage_daily`, `jobs`) |
| Cardinality | Bounded labels only | Arbitrary; it's a database |
| Served by | `/metrics` | `/api/v1/tenants/{t}/accounting/*`, dashboards |

Rule: labels on Prometheus metrics come from a closed set —
`cluster`, `partition` (bounded per cluster), `tenant` (only on a small
number of platform counters; disable via config above ~500 tenants),
`kind`, `state`, `result`, `route` (templated, not raw path), `method`,
`status_class`. **Never** `user`, `job_id`, `execution_id`, `workflow`,
raw URL path.

## Stack

- OpenTelemetry Go SDK for traces and metrics; Prometheus exporter for
  `/metrics`; OTLP exporter for traces (and optionally metrics) to a
  collector. `slog` for logs with `trace_id`/`span_id`/`request_id`
  attributes. ADR-008.
- `net/http` instrumentation via `otelhttp` with a route-template span name.
- `pgx` tracer hook for DB spans (statement name, not full SQL).

## Platform metrics (`/metrics`)

```text
custos_http_requests_total{route,method,status_class}
custos_http_request_duration_seconds{route,method}
custos_http_request_size_bytes / response_size_bytes
custos_authn_failures_total{reason}
custos_authz_denied_total{action}
custos_db_query_duration_seconds{name}
custos_db_pool_{acquired,idle,max}
custos_secrets_requests_total{op,result}
custos_slurm_requests_total{cluster,op,result}
custos_slurm_request_duration_seconds{cluster,op}
custos_work_items_*  (see workers.md)
custos_job_submissions_total{cluster,result}
custos_jobs_by_state{cluster,state}                   (gauge, from sweep)
custos_executions_by_state{state}
custos_workflow_validation_failures_total{stage}
custos_build_info{version,commit}
```

## Cluster metrics (`/metrics`, populated by `cluster.sync`)

```text
custos_cluster_up{cluster}
custos_cluster_nodes{cluster,state}                   idle|allocated|mixed|drained|down|...
custos_cluster_cpus{cluster,kind}                     total|allocated|idle
custos_cluster_gpus{cluster,type,kind}                total|allocated
custos_cluster_memory_bytes{cluster,kind}
custos_cluster_jobs{cluster,partition,state}          running|pending
custos_cluster_sched_cycle_seconds{cluster}           from /diag
custos_cluster_sync_age_seconds{cluster}
```

These are also persisted as `cluster_snapshots` rows (5-minute resolution,
90-day retention) so the UI can chart cluster history without querying
Prometheus.

## Accounting pipeline

```text
slurmdbd ── GET /slurmdb/v0.0.4x/jobs?start=<watermark> ──► accounting.collect worker
      └► usage_records (append-only; one row per ended job, step count/usage folded in)
             └► usage.aggregate ──► usage_daily(tenant, project, user, cluster, account, partition, day,
                                                jobs, cpu_seconds, gpu_seconds, node_seconds, mem_gb_seconds,
                                                wait_seconds_sum, run_seconds_sum, failed, energy_joules)
                                        └► allocations.consumed (derived on read or materialized nightly)
```

Correlation: `usage_records.job_id` is filled by matching
(`cluster_id`, `slurm_job_id`) to `jobs`; Slurm records for jobs not
submitted through Custos are kept (with `job_id NULL`) and attributed to a
project via `ProjectClusterBinding.slurm_account` when exactly one binding
matches, so "utilization by cluster" and "CPU-hours by account" are complete
even when users bypass Custos. Ambiguous/unmatched records remain platform-only.
Slurm username → Custos user mapping is not implemented; external records keep
the raw Slurm username and a NULL user (ADR-018).

Implemented in M7-A: `accounting.collect` runs per cluster every five minutes
with a two-hour overlap and bounded 24-hour chunks; immutable inserts mark dirty
days. `usage.aggregate` hourly recomputes each dirty cluster/day, including
p50/p90/p99 wait/runtime. `usage_records` partitions retain 400 days of ended
facts; tenant RLS excludes unattributed rows. Operational collect/aggregate
endpoints can pull the periodic work forward.

Per-job metrics persisted on `jobs.resource_usage` include elapsed, derived CPU
seconds, node count, allocated/consumed TRES, wait seconds, exit code, and the
derived failure flag. Derivations are: allocated CPUs/GPUs × elapsed,
node count × elapsed, allocated memory MiB × elapsed / 1024, and
start − max(submit, eligible), clamped to zero.

## Queries the API must answer (Milestone 7)

All via `usage_daily` with keyset pagination and bounded date ranges
(max 400 days per query):

- CPU/GPU-hours by user | project | tenant | account | cluster | partition
- jobs, failure rate, average/percentile wait, runtime distribution
  (percentiles precomputed per day: p50/p90/p99 wait and run)
- top-N consumers within a scope the caller may read
- allocation consumed / remaining per `ProjectClusterBinding`

Implemented endpoints are `GET /tenants/{tenant}/accounting/usage` (group by
user/project/cluster/account/partition/day) and `/accounting/top` (CPU/GPU
seconds or jobs by user/project), with RFC 3339 required bounds, a 400-day cap,
and stable group-key cursors. Authorization is
`accounting.read.self|project|tenant`: self sees own rows, project readers also
see NULL-user rows in their projects, and tenant readers see all attributed
rows.

## Logging

`slog` JSON to stdout. Mandatory fields: `time`, `level`, `msg`,
`request_id`, `trace_id`, `tenant_id` (when in scope), `principal_id`
(UUID, not email). Redaction: `secrets.Value` and `authn.Token` types
implement `LogValuer` → `[REDACTED]`; a lint rule (custom `go vet`
analyzer or `forbidigo` patterns) forbids logging `http.Header` and raw
request bodies.

## Health

- `/health/live`: process alive, event loop responsive. Always cheap.
- `/health/ready`: DB ping (required); OpenBao token valid (degraded if
  not); JWKS loaded (required for `serve`; if never loaded → not ready);
  at least one cluster reachable is **not** required (clusters failing must
  not take down the control plane). Body lists component states; HTTP 200
  when ready-or-degraded, 503 when required components fail.
