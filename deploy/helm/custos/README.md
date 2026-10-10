# Custos Helm chart

Deploys Custos on Kubernetes (>= 1.29): the API (`serve`), the `worker`, the web
frontend, and optionally PostgreSQL (CloudNativePG) and OpenBao as dependency
subcharts. See `docs/kubernetes.md` for the operator guide and ADR-033 for the
design decisions.

```sh
helm dependency build deploy/helm/custos
helm install custos deploy/helm/custos -n custos --create-namespace -f my-values.yaml --wait
```

## Design in one paragraph

* Restricted Pod Security Standard on every pod: non-root, `RuntimeDefault`
  seccomp, all capabilities dropped, no privilege escalation, read-only root
  filesystem (`/tmp` is an `emptyDir`), no service-account token mounted.
* Migrations are **not** Helm hooks. A plain Job
  `<fullname>-migrate-r<revision>` runs `custos migrate up`; `serve` and
  `worker` pods have a `migrate wait` init container, so they only start once
  the schema is current. Old revision Jobs disappear from the manifest and are
  pruned by Helm.
* The ShellCheck validator is a native sidecar (init container with
  `restartPolicy: Always`) in `serve` and `worker`, loopback only, never exposed.
* Exposure uses Gateway API (`HTTPRoute`, optional `BackendTLSPolicy`); there is
  no Ingress template.

## Required values

The render fails with a clear message when these are missing:

| Value | Why |
| --- | --- |
| `api.tls.secretName` **or** `api.tls.certManager.enabled` + `issuerRef.name` | The API listener requires TLS. The certificate must cover `<fullname>-api.<namespace>.svc`. |
| `config.auth.oidc.issuer` (unless `config.dev_mode: true`) | OIDC token verification. |
| `database.migrateURLSecret.name` and `database.appURLSecret.name` (unless `postgresql.enabled`) | Database DSNs (`url` key by default). |
| `web.publicOrigin`, `web.client.secret.name`, `web.authSecrets.name` (when `web.enabled`) | Web frontend configuration. |

## Chart-managed configuration keys

`config` is a free-form passthrough rendered into the ConfigMap and deep-merged
**under** these chart-managed keys (the chart wins): `server.listen`,
`server.tls.{mode,cert_file,key_file}`, `metrics.{enabled,listen,tls.mode}`,
`database.ssl_mode` (from `database.sslMode`), `database.tls.root_ca_file`,
`auth.oidc.ca_file`, `validation.shellcheck.{enabled,endpoint}` and
`secrets.openbao.{address,namespace,timeout,ca_file,auth}`.

## Values reference

### Images and common

| Key | Default | Description |
| --- | --- | --- |
| `image.{repository,tag,digest,pullPolicy}` | `exonical/custos`, `""`, `""`, `IfNotPresent` | serve, worker, migrate. Tag defaults to `.Chart.AppVersion`; a digest wins. Prefix the repository with your registry. |
| `validator.enabled` / `validator.image.*` / `validator.resources` | `true` / `exonical/custos-validator` | Native sidecar. |
| `web.image.*` | `exonical/custos-web` | Web image (`runner` target). |
| `global.imagePullSecrets` | `[]` | Applied to every pod. |
| `serviceAccount.{create,name,annotations}` | `true` | The token is never mounted. |
| `topologySpread.enabled` | `true` | Soft per-host spread unless a component sets `topologySpreadConstraints`. |

### Components (`serve`, `worker`, `web`)

`replicas`, `resources`, `nodeSelector`, `tolerations`, `affinity`,
`podAnnotations`, `podLabels`, `topologySpreadConstraints`, `extraEnv`.
`serve` and `web` also have `pdb.{enabled,minAvailable}` (created when more than
one replica is possible) and `autoscaling.{enabled,minReplicas,maxReplicas,targetCPUUtilizationPercentage}`.

### Database and migrations

| Key | Default | Description |
| --- | --- | --- |
| `database.migrateURLSecret` / `appURLSecret` | `{name: "", key: url}` | Secrets holding the owner and runtime DSNs. |
| `database.caSecret` | `{name: "", key: ca.crt}` | Optional CA for `verify-ca`/`verify-full`. |
| `database.appRole` | `""` | Runtime role granted DML by `migrate up`. |
| `database.sslMode` | `verify-full` | `require`, `verify-ca` or `verify-full`. |
| `migrations.jobNameSuffix` | `""` | Replaces `r<revision>`; set for Argo CD / Flux. |
| `migrations.backoffLimit` / `activeDeadlineSeconds` / `ttlSecondsAfterFinished` | `2` / `900` / `null` | Job settings. |
| `migrations.waitTimeout` | `10m` | `migrate wait --timeout` in serve and worker. |

### API TLS, OIDC, web

| Key | Description |
| --- | --- |
| `api.tls.secretName` | Existing `kubernetes.io/tls` Secret (also the cert-manager target). |
| `api.tls.caSecret.{name,key}` | CA the web frontend trusts for the API (defaults to `ca.crt` of `secretName`). |
| `api.tls.certManager.{enabled,issuerRef,duration,renewBefore,dnsNames}` | cert-manager `Certificate` for the API service DNS names. |
| `auth.oidc.clientSecret` / `auth.oidc.caSecret` | Optional OIDC client secret file and issuer CA for the API. |
| `web.publicOrigin` | Browser origin; also the OIDC redirect base. |
| `web.client.{id,secret}` | Web OIDC client; secret mounted as `CUSTOS_WEB_CLIENT_SECRET_FILE`. |
| `web.authSecrets` | `CUSTOS_WEB_AUTH_SECRETS` via `secretKeyRef`. |
| `web.idpCaSecret` / `web.issuer` / `web.apiURL` / `web.trustedProxyHops` | Optional IdP CA, issuer override, API URL override, proxy hops. |

### OpenBao client (Custos side)

`openbaoClient.enabled`, `address` (https), `namespace`, `timeout`, `caSecret`,
`auth.role`, and exactly one of `auth.workloadIdentity.{enabled,audience,expirationSeconds}`
(projected service-account token mounted at `/var/run/secrets/custos/openbao/token`)
or `auth.oidcClientCredentials.{enabled,tokenURL,clientID,clientSecret}`.

### Gateway API, NetworkPolicy, monitoring

| Key | Description |
| --- | --- |
| `gateway.{enabled,parentRefs}` | Required together; parentRefs point at a Gateway you manage. |
| `gateway.web.{hostnames,annotations}` | `HTTPRoute` for the web service (path `/`). |
| `gateway.api.{enabled,hostnames}` | Optional `HTTPRoute` for the API. |
| `gateway.api.backendTLS.{enabled,apiVersion,caConfigMap,hostname}` | `BackendTLSPolicy` (`gateway.networking.k8s.io/v1`, standard channel since Gateway API v1.4) so the gateway verifies the HTTPS backend. |
| `networkPolicy.enabled` | Default `true`. |
| `networkPolicy.{apiFrom,metricsFrom,webFrom}` | NetworkPolicyPeer lists. **Empty means "from anywhere"** for that port; web pods are always allowed on 8443 when `apiFrom` is non-empty. 8481 is never opened. |
| `networkPolicy.egress.restrict` | Default `false` (Custos must reach arbitrary slurmrestd, OpenBao and IdP endpoints). When `true`: DNS, web to api, bundled postgres/openbao and `egress.extra`. |
| `serviceMonitor.{enabled,interval,labels}` | Prometheus Operator `ServiceMonitor` for serve and worker (`/metrics`, HTTP). |

### Dependency subcharts

| Alias | Chart | Condition | Notes |
| --- | --- | --- | --- |
| `postgresql` | CloudNativePG `cluster` 0.9.0 | `postgresql.enabled` | **Evaluation-grade.** Needs the CNPG operator. One owner role, no least-privilege runtime role. Wires both DSNs from `<release>-postgresql-app` (`uri`) and the CA from `<release>-postgresql-ca`. PostgreSQL 18. |
| `openbao` | OpenBao 0.30.2 | `openbao.enabled` | Deploy-only. Initialise, unseal and configure JWT auth manually. |

Dependencies are pinned in `Chart.yaml` and `Chart.lock`; `charts/*.tgz` is
git-ignored (`helm dependency build`).

## Testing

`scripts/helm-check.sh` runs lint, kubeconform and helm-unittest;
`scripts/helm-kind.sh` installs the chart into a throwaway kind cluster (see
`docs/kubernetes.md`).
