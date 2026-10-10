# Kubernetes deployment

Custos ships a Helm chart in `deploy/helm/custos` (chart 0.2.0, Kubernetes
>= 1.29). The design decisions are in
[ADR-033](adr/ADR-033-kubernetes-packaging.md); the full values reference is
`deploy/helm/custos/README.md`.

## What is deployed

| Workload | Purpose | Ports |
| --- | --- | --- |
| `serve` Deployment | API (HTTPS) | 8443 api, 9090 metrics |
| `worker` Deployment | Work-queue lease loop, cluster sync | 9090 metrics (also `/health/ready`) |
| `web` Deployment | Next.js frontend (BFF) | 3000 HTTP |
| migrate Job | `custos migrate up`, one per Helm revision | none |

`serve` and `worker` run the ShellCheck validator as a native sidecar
(loopback 8481, never exposed) and wait for the schema with
`custos migrate wait` in an init container. Everything runs under the
restricted Pod Security Standard with no service-account token mounted.

## Prerequisites

- Kubernetes >= 1.29 (native sidecars), a default StorageClass only if you use
  the bundled PostgreSQL.
- An OIDC provider (issuer reachable from the pods) with a confidential client
  for the web frontend and an API audience, see
  [authentication.md](authentication.md).
- PostgreSQL 16+ with SCRAM and TLS (ADR-013), or the CloudNativePG operator
  for the bundled evaluation database.
- A Gateway API implementation and a `Gateway` if you want routes created.
- cert-manager only if `api.tls.certManager.enabled=true`.

## Secrets to create

```sh
kubectl create namespace custos

# API certificate: must cover custos-api.custos.svc (the web frontend
# connects to it) and carry the issuing CA as ca.crt.
kubectl -n custos create secret generic custos-api-tls --type=kubernetes.io/tls \
  --from-file=tls.crt --from-file=tls.key --from-file=ca.crt

# Database DSNs (TLS is configured by database.sslMode, never in the URL).
kubectl -n custos create secret generic custos-db-owner --from-literal=url='postgres://custos_owner:...@pg.example.org:5432/custos'
kubectl -n custos create secret generic custos-db-app   --from-literal=url='postgres://custos_app:...@pg.example.org:5432/custos'

# Web frontend: OIDC client secret and Auth.js session secrets (>= 32 bytes each).
kubectl -n custos create secret generic custos-web-oidc --from-literal=client-secret='...'
kubectl -n custos create secret generic custos-web-auth --from-literal=auth-secrets="$(openssl rand -base64 32)"
```

## Minimal values

```yaml
config:
  auth:
    oidc:
      issuer: https://idp.example.org/realms/custos
      client_id: custos
      audiences: [custos]

database:
  migrateURLSecret: {name: custos-db-owner, key: url}
  appURLSecret: {name: custos-db-app, key: url}
  appRole: custos_app          # least-privilege runtime role (ADR-013)

api:
  tls:
    secretName: custos-api-tls

web:
  publicOrigin: https://custos.example.org
  client:
    secret: {name: custos-web-oidc, key: client-secret}
  authSecrets: {name: custos-web-auth, key: auth-secrets}
```

```sh
helm dependency build deploy/helm/custos
helm install custos deploy/helm/custos -n custos -f values.yaml --wait
```

The image repositories default to the published names `exonical/custos`,
`exonical/custos-web` and `exonical/custos-validator` without a registry host:
set `image.repository`, `web.image.repository` and `validator.image.repository`
(and optionally `*.digest`) for your registry. The render fails with a clear
message when the API TLS source, the OIDC issuer or `web.publicOrigin` is
missing.

The web frontend rejects requests whose `Host` does not match
`web.publicOrigin` (HTTP 421); the chart's probes send that host. Register
`<publicOrigin>/api/auth/callback/custos` as the redirect URI of the OIDC client
and make sure the web pods can reach the issuer (`web.idpCaSecret` for a private
CA).

The first platform admin is granted from inside the cluster:

```sh
kubectl -n custos exec deploy/custos-serve -c custos -- /custos admin \
  platform-role grant --issuer https://idp.example.org/realms/custos --subject <sub> --role platform-admin
```

## Optional dependencies

Both are disabled by default and pinned in `Chart.yaml` / `Chart.lock`.

- **PostgreSQL** (`postgresql.enabled=true`): a CloudNativePG `Cluster`
  (PostgreSQL 18). The CloudNativePG operator must already be installed
  (`helm install cnpg cnpg/cloudnative-pg -n cnpg-system --create-namespace`).
  Both DSNs come from the `<release>-postgresql-app` secret (`uri`), the CA from
  `<release>-postgresql-ca`, and `database.sslMode` stays `verify-full`.
  **Evaluation-grade**: migrations and runtime share one owner role, so the
  least-privilege app role is not in effect. Do not use it for production data.
- **OpenBao** (`openbao.enabled=true`): the server only. Initialise and unseal
  it, enable JWT auth for Custos (workload identity or an OIDC client), then
  point `openbaoClient.address` (https, TLS required) at it and enable one of
  `openbaoClient.auth.workloadIdentity` / `oidcClientCredentials`.
  `openbaoClient` holds the Custos-side settings so they do not collide with the
  subchart's `openbao` values.

## Exposure (Gateway API)

```yaml
gateway:
  enabled: true
  parentRefs: [{name: public, namespace: gateways, sectionName: https}]
  web:
    hostnames: [custos.example.org]
  api:
    enabled: true            # optional API route
    hostnames: [api.custos.example.org]
    backendTLS:
      enabled: true          # BackendTLSPolicy for the HTTPS API backend
      caConfigMap: custos-api-ca
```

`HTTPRoute` is `gateway.networking.k8s.io/v1`. `BackendTLSPolicy` is also `v1`
in the standard channel since Gateway API v1.4 (verified against the v1.6.2
standard install); `gateway.api.backendTLS.apiVersion` can be overridden for
older CRD bundles. The chart renders routes only; the Gateway is yours. There is
no Ingress template.

## Network policy

Enabled by default. Ingress is per port: API 8443 from the web pods plus
`networkPolicy.apiFrom`, metrics 9090 from `metricsFrom`, web 3000 from
`webFrom`. An empty peer list means "from anywhere" for that port (set the lists
to lock them down). The validator port 8481 is never opened. Egress is
unrestricted by default because Custos must reach arbitrary slurmrestd, OpenBao
and IdP endpoints; with `networkPolicy.egress.restrict=true` only DNS, web to
API, the bundled PostgreSQL/OpenBao pods and `networkPolicy.egress.extra` (raw
rules, add your IdP and slurmrestd endpoints) are allowed.

## Upgrades and migrations

`helm upgrade` creates a new migrate Job `<fullname>-migrate-r<revision>`;
serve and worker pods of the new revision stay in Init until `custos migrate
wait` sees the schema current, so a rolling update never serves against an older
schema. Old Jobs are removed with the revision. The pod template carries a
checksum of the rendered config, so a config change rolls the pods.

Argo CD and Flux do not provide a stable `.Release.Revision`: set
`migrations.jobNameSuffix` (for example the image tag) so the Job is recreated
exactly when you intend a migration. Migrations are forward-only in the chart;
roll back the schema with `custos migrate down` by hand if needed.

## Testing the chart

- `bash scripts/helm-check.sh`: `helm lint --strict` and `kubeconform -strict`
  (built-in schemas plus Gateway API, cert-manager, CloudNativePG and
  Prometheus Operator CRD schemas) for every `deploy/helm/custos/ci/*-values.yaml`,
  a config-checksum check and the `helm unittest` suites. Runs in CI. Tool
  versions are pinned in the script (helm v4.3.0, kubeconform v0.8.0,
  helm-unittest v1.2.0). On non-Linux hosts set `HELM_BIN` / `KUBECONFORM_BIN`
  and install the plugin yourself.
- `bash scripts/helm-kind.sh`: local/nightly smoke install into a throwaway
  kind cluster (kind v0.33.0 with Kubernetes v1.35.8) on **rootful** Podman,
  never on pull requests. It builds or reuses `custos:local`,
  `custos-web:local` and `custos-validator:local`, installs the pinned
  CloudNativePG operator and Gateway API CRDs, runs a Keycloak fixture with the
  e2e realm over HTTPS (so Custos runs with `dev_mode: false`), installs the
  chart with the bundled PostgreSQL, web, gateway routes and network policies,
  and asserts the migrate Job, Ready workloads, the validator sidecars, API
  `/health/ready` over TLS, an authenticated `GET /api/v1/me` as alice, the web
  `/api/health`, a `helm upgrade` that creates `-r2` and rolls the pods, and a
  clean `helm uninstall` (only the PVC remains, per CloudNativePG policy). Logs go
  to a temp directory (`HELM_KIND_LOGDIR`); `KEEP=1` keeps the cluster;
  `HELM_KIND_BUILD=1` rebuilds the images. It needs Linux with rootful Podman and
  about 4 GiB of free memory; on Windows run it inside the Podman machine
  (`podman machine ssh`), where the repo is under `/mnt/c/...`.
