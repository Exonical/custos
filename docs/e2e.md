# End-to-end stack

`deploy/e2e/` + `scripts/e2e.sh` stand up a real Podman (or Docker)
environment and the Go suite in `test/e2e/` drives Custos against it.
Everything runs locally or in the nightly CI job — never on PRs.

## What's in the stack

| Service | Image | Notes |
| --- | --- | --- |
| mariadb | `mariadb:11.8` | accounting DB, tmpfs, `innodb_snapshot_isolation=OFF` |
| slurmdbd | `slinkyproject/slurmdbd:26.05-ubuntu26.04` | `AuthAltTypes=auth/jwt` so slurmrestd's pass-through works |
| slurmctld | `slinkyproject/slurmctld:26.05-ubuntu26.04` | cluster `e2e`, `auth/slurm` + `auth/jwt` |
| slurmd | `slinkyproject/slurmd:26.05-ubuntu26.04` | one node `c1` (`CPUs=2`), **unprivileged**: `proctrack/pgid`, `task/none`, cgroup plugin disabled |
| slurmrestd | `slinkyproject/slurmrestd:26.05-ubuntu26.04` | `-a rest_auth/jwt`, data_parser `v0.0.45`, 26.05.4 |
| nginx | `nginx:1.29-alpine` | TLS terminator for slurmrestd (`slurmrestd.e2e:6820`), Keycloak (`keycloak.e2e:8443`), BYO OpenBao (`openbao-byo.e2e:8250`), and the web UI (`https://127.0.0.1:3000`); upstreams resolve per request via embedded DNS |
| keycloak | `keycloak/keycloak:26.7` | realm `custos`; bearer client `custos-e2e`, confidential browser client `custos-web` with PKCE S256, and service-account client `custos-openbao` |
| openbao | `openbao/openbao:2.6.2` | platform provider, TLS + file storage, initialized and made ready by one-shot bootstrap; root token revoked |
| openbao-byo | `openbao/openbao:2.6.2` | customer-manager stand-in; dev server is e2e-only and Custos reaches it through nginx TLS |
| postgres | `postgres:18` | Custos metadata (same as the runtime stack) |
| custos / worker | `custos:local` | the hardened runtime services |
| web | `custos-web:local` | Next.js stateless BFF; host access is TLS-only through nginx at `https://127.0.0.1:3000` |
| validator-api / validator-worker | `custos-validator:local` | ShellCheck 0.11.0 sidecars on `127.0.0.1:8481` inside each parent's network namespace |

## Running it

```sh
scripts/e2e.sh up      # secrets → compose up --wait → bootstrap → prints env
scripts/e2e.sh down    # compose down -v
scripts/e2e.sh logs    # pass-through to compose logs
```

`up` does, in order: generate `.secrets/` (CA, certs, `slurm.key`,
`jwt_hs256.key`, slurmdbd password, Keycloak client secrets, the `custos-web`
client secret, and BFF Auth.js secrets); render the Keycloak realm
with the generated browser-client secret;
bring the whole stack up healthy; initialize/unseal platform OpenBao and
configure its Keycloak JWT role; create a renewable, orphaned 768-hour
`e2e-slurm-credential` token scoped only to `kv/data/clusters/e2e`; wait for
the ShellCheck sidecars on `127.0.0.1:8481` **inside each parent netns** (see
below); wait for slurmctld and node `c1`; create the `custos` Slurm user
(uid 2000) in slurmctld/slurmd/slurmrestd/**slurmdbd**; wait for `sacctmgr` and
add cluster `e2e`, account `e2e-acct`, QoS `normal`+`high`, and the `custos`
association idempotently (existence-checked, no swallowed errors); it also
sets the `custos` Slurm user's verified `AdminLevel=Operator` with
`sacctmgr -i` (tested against live Slurm 26.05.4); **restart slurmctld** so it
registers the cluster's control port with slurmdbd. Its `StateSaveLocation` is
backed by the named `slurmctld_state` volume, preserving job IDs across restarts;
mint a long-lived JWT and
write it to the file-provider fixture, then refresh and renew the platform
OpenBao Slurm credential with the scoped token. The one-shot bootstrap token is
used only to seed Alice's tenant fixture value; after it is revoked and removed,
subsequent `up` runs skip that seed because the value persists in the OpenBao
volume. A missing scoped token fails with the reset hint. Finally, seed the
TLS-fronted BYO OpenBao; wait for Custos readiness; grant `platform-admin`;
and seed tenant `acme` and its claim rules.
`TestE2E` exercises both cluster providers, the automatic default connector,
a BYO connector, owner isolation, path/namespace validation, SSRF denial,
workflow env delivery, platform-OpenBao wrapped-token delivery, value-free
execution metadata/audit, execute-time denial for another user, real slurmdbd
accounting collection, per-job resource usage, daily usage aggregation,
hard-allocation denial and recovery, slurmdbd policy creation, allocation `GrpTRESMins` set/clear, full reconciliation, report mode, and safe unbinding cleanup.

Then, with the env it prints:

```sh
export CUSTOS_E2E=1 CUSTOS_E2E_API=https://127.0.0.1:8080 \
  CUSTOS_E2E_CA=$PWD/deploy/e2e/.secrets/e2e-ca.crt \
  CUSTOS_E2E_KEYCLOAK=https://keycloak.e2e:8443/realms/custos \
  CUSTOS_E2E_CLIENT_ID=custos-e2e CUSTOS_E2E_CLIENT_SECRET=e2e-client-secret
go test ./test/e2e/... -count=1 -v
```

The web service is served through the same e2e CA at `https://127.0.0.1:3000`.
After the Go suite, run the live browser check (it signs in as Alice and
requires at least one visible Acme job):

```sh
cd web
CUSTOS_E2E=1 pnpm e2e:live
```

`CONTAINER_ENGINE=docker scripts/e2e.sh up` works too (used in CI).

## Test-only identities

Realm `custos` (`deploy/e2e/keycloak/realm-custos.json`):

- `alice` / `alice-e2e-password` — group `hpc-a` (→ `researcher` in `acme`)
- `bob` / `bob-e2e-password` — no group (not a tenant member)
- `platform-admin` / `platform-admin-e2e-password` — group `hpc-admins`

Client `custos-e2e` / `e2e-client-secret` is used by the Go suite and local web
development. It allows the Auth.js callbacks
`http://localhost:3000/api/auth/callback/custos` and
`http://localhost:3001/api/auth/callback/custos`, with both localhost web origins.
The confidential e2e browser client `custos-web` allows
`https://127.0.0.1:3000/api/auth/callback/custos` and gets a generated secret
in `.secrets/custos-web-client-secret`; the Auth.js secret array is generated
in `.secrets/custos-web-auth-secrets`. User passwords in the realm are
test-only. The e2e CA (`.secrets/e2e-ca.crt`) and generated credentials are
gitignored.

## Issuer and CA wiring

`KC_HOSTNAME=https://keycloak.e2e:8443` keeps the token issuer stable
inside and outside the cluster. Custos verifies Keycloak TLS with
`auth.oidc.ca_file=/etc/custos/secrets/e2e-ca.crt` (the same CA that
signs the nginx certs); cluster registration points `ca_file` at the
same CA for `https://slurmrestd.e2e:6820`. On the host, the test
harness pins `*.e2e` names to loopback in-process; `e2e.sh` prints the
`/etc/hosts` line if you want `keycloak.e2e` resolvable for your own
curls. On Windows, curl's schannel backend needs `--ssl-no-revoke`
against the test CA (no CRL) — the script sets it.

## Sidecar lifecycle caveat

`validator-api` and `validator-worker` use
`network_mode: service:custos|worker`. If a parent is recreated while a
sidecar is alive, the sidecar stays bound to the **dead** netns and
`127.0.0.1:8481` goes silent inside the new parent. `e2e.sh up`
therefore removes the two sidecars and the two parents (in dependency
order — the engine refuses to remove a parent with dependents) before
`up --wait` recreates all four, and probes `8481/healthz` from a
throwaway curl container joined to each parent's netns before
continuing.

## Re-run safety

The suite re-runs against a live stack: tenant `acme`, cluster `e2e` and
project `p1` tolerate `409` and reuse the existing resource; workflow
names get a per-run base36 suffix (`time.Now().UnixNano()`); idempotency
keys are timestamped.

## Conformance fixtures

`internal/slurm/slinky/v0045/testdata/*.json` are recorded from this
stack — see `testdata/README.md` in that directory for provenance and
how to re-record.

## CI

`.github/workflows/e2e.yml` runs nightly and on `workflow_dispatch`:
docker engine, hosts entry for `keycloak.e2e`, image builds,
`scripts/e2e.sh up`, `go test ./test/e2e/... -count=1 -timeout 30m -v`,
log dump on failure, `down -v` always.
