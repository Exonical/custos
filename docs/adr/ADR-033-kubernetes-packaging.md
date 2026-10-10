# ADR-033: Kubernetes packaging

Status: Accepted  
Date: 2026-10-10

## Context

The Milestone-1 Helm skeleton in `deploy/helm/custos` had no web frontend, no
OIDC or OpenBao wiring, ran migrations as a Helm hook that referenced a
ConfigMap that does not exist yet at hook time, and emitted the serve ports
and probes after the validator container so they landed on the validator.
Operators need a chart that deploys the whole product, works with
`helm install --wait` and GitOps tools, and is verifiable without a real
cluster fleet.

## Decision

- **Helm chart** `deploy/helm/custos` (apiVersion v2, Kubernetes >= 1.29).
  It deploys `serve`, `worker` and `web`; everything else is optional or
  external.
- **Optional dependency subcharts, off by default:** the CloudNativePG
  `cluster` chart (alias `postgresql`) and the OpenBao chart (alias
  `openbao`), pinned exactly with a committed `Chart.lock`. The chart never
  bundles operators; CloudNativePG must already be installed. The bundled
  PostgreSQL is **evaluation-grade**: one owner role is used for migrations
  and runtime, so the least-privilege app role of ADR-013 is not in effect.
  Production uses an external database with `database.appRole`. The OpenBao
  subchart is deploy-only; initialisation, unseal and JWT auth stay manual.
  Custos-side OpenBao settings live under `openbaoClient` so they do not
  collide with the subchart's values.
- **Migrations are a plain per-revision Job, not a Helm hook.** The Job is
  named `<fullname>-migrate-r<revision>` and runs `custos migrate up`.
  Hooks run before the release's ConfigMap and Secrets exist, deadlock with
  `helm install --wait` when PostgreSQL is a subchart (the hook waits for a
  database that the same release has not created yet) and are invisible or
  mis-ordered under Argo CD and Flux. A normal Job is part of the manifest, so
  old revisions are pruned automatically; `migrations.jobNameSuffix` gives
  GitOps tools a stable name. `serve` and `worker` pods carry an init
  container running `custos migrate wait` (read-only, works with the
  least-privilege runtime role), so they start only once the schema is
  current and rolling updates never serve against an older schema.
- **Gateway API only.** Exposure uses `HTTPRoute` (and `BackendTLSPolicy` for
  the HTTPS API backend, `gateway.networking.k8s.io/v1`, standard channel
  since Gateway API v1.4). There is no Ingress template; the chart renders
  routes only and expects a Gateway managed by the platform team.
- **Validator as a native sidecar** (an init container with
  `restartPolicy: Always`, Kubernetes >= 1.29) in `serve` and `worker`: it
  starts before and outlives the main container, is never exposed by a
  Service or NetworkPolicy, and is probed with an exec healthcheck because it
  listens on loopback only.
- **Restricted Pod Security Standard everywhere** (non-root, `RuntimeDefault`
  seccomp, all capabilities dropped, read-only root filesystem, no
  service-account token mounted; OpenBao workload identity uses an
  audience-bound projected token).
- **API TLS is required** (a Secret or cert-manager), because the API listener
  never runs plaintext outside `dev_mode` and the web frontend verifies it.
- **NetworkPolicies are on by default for ingress. Egress is unrestricted by
  default**: Custos must reach arbitrary slurmrestd, OpenBao and IdP endpoints
  that the chart cannot enumerate. `networkPolicy.egress.restrict` is
  available for sites that can list them.
- `config` is a free-form passthrough merged under chart-managed keys, so new
  configuration options need no chart release, while security-relevant keys
  (TLS, listeners, validator endpoint) cannot be overridden by accident.

## Alternatives considered

- **Helm hooks for migrations**: rejected (ordering and deadlock problems above).
- **Migrate in an init container of every pod**: rejected; concurrent
  migrations race and every replica needs the owner credential.
- **Ingress templates**: rejected by the product owner; Gateway API is the
  supported exposure.
- **Bundling operators**: rejected; operators are cluster-scoped and owned by
  the platform team.

## Consequences

- Chart changes are verified by `scripts/helm-check.sh` (lint, kubeconform
  with Gateway API, cert-manager, CloudNativePG and Prometheus Operator CRD
  schemas, helm-unittest) in CI, and by `scripts/helm-kind.sh` (a real install
  into a throwaway kind cluster on Podman, local/nightly only).
- Operators on Argo CD or Flux must set `migrations.jobNameSuffix` (for
  example to the image tag) and accept that the Job is re-created on change.
- The bundled PostgreSQL must not be used for production data.
- Kubernetes < 1.29 is unsupported (native sidecars).
- See [kubernetes.md](../kubernetes.md) for the operator guide.
