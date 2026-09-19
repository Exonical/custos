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
| nginx | `nginx:1.29-alpine` | TLS terminator for slurmrestd (`slurmrestd.e2e:6820`), Keycloak (`keycloak.e2e:8443`), and BYO OpenBao (`openbao-byo.e2e:8250`); upstreams resolve per request via embedded DNS |
| keycloak | `keycloak/keycloak:26.7` | realm `custos`; user client `custos-e2e`; service-account client `custos-openbao` for workload JWT auth |
| openbao | `openbao/openbao:2.6.2` | platform provider, TLS + file storage, initialized/unsealed by one-shot bootstrap; root token revoked |
| openbao-byo | `openbao/openbao:2.6.2` | customer-manager stand-in; dev server is e2e-only and Custos reaches it through nginx TLS |
| postgres | `postgres:18` | Custos metadata (same as the runtime stack) |
| custos / worker | `custos:local` | the hardened runtime services |
| validator-api / validator-worker | `custos-validator:local` | ShellCheck 0.11.0 sidecars on `127.0.0.1:8481` inside each parent's network namespace |

## Running it

```sh
scripts/e2e.sh up      # secrets → compose up --wait → bootstrap → prints env
scripts/e2e.sh down    # compose down -v
scripts/e2e.sh logs    # pass-through to compose logs
```

`up` does, in order: generate `.secrets/` (CA, certs, `slurm.key`,
`jwt_hs256.key`, slurmdbd password, Keycloak client secrets, and TLS material);
bring the whole stack up healthy; initialize/unseal platform OpenBao, configure
its Keycloak JWT role, and revoke its bootstrap root token; wait for the ShellCheck sidecars on
`127.0.0.1:8481` **inside each parent netns** (see below); wait for
slurmctld and node `c1`; create the `custos` Slurm user (uid 2000) in
slurmctld/slurmd/slurmrestd/**slurmdbd**; wait for `sacctmgr` and add
cluster `e2e`, account `e2e-acct`, QoS `normal`+`high`, and the `custos`
association idempotently (existence-checked, no swallowed errors);
**restart slurmctld** so it registers the cluster's control port with
slurmdbd (job accounting records require it — verified via
`sacctmgr show cluster`); mint a long-lived JWT and write it both to the
file-provider fixture and platform OpenBao; seed the TLS-fronted BYO OpenBao;
wait for Custos readiness; grant `platform-admin`; seed tenant `acme` and its
claim rules; then write Alice's fixture value in her new tenant namespace.
`TestE2E` exercises both cluster providers, the automatic default connector,
a BYO connector, owner isolation, path/namespace validation, and SSRF denial.

Then, with the env it prints:

```sh
export CUSTOS_E2E=1 CUSTOS_E2E_API=https://127.0.0.1:8080 \
  CUSTOS_E2E_CA=$PWD/deploy/e2e/.secrets/e2e-ca.crt \
  CUSTOS_E2E_KEYCLOAK=https://keycloak.e2e:8443/realms/custos \
  CUSTOS_E2E_CLIENT_ID=custos-e2e CUSTOS_E2E_CLIENT_SECRET=e2e-client-secret
go test ./test/e2e/... -count=1 -v
```

`CONTAINER_ENGINE=docker scripts/e2e.sh up` works too (used in CI).

## Test-only identities

Realm `custos` (`deploy/e2e/keycloak/realm-custos.json`):

- `alice` / `alice-e2e-password` — group `hpc-a` (→ `researcher` in `acme`)
- `bob` / `bob-e2e-password` — no group (not a tenant member)
- `platform-admin` / `platform-admin-e2e-password` — group `hpc-admins`

Client `custos-e2e` / `e2e-client-secret`. All passwords live in the
realm file and are **test-only** — nothing here is a real secret. The
e2e CA (`.secrets/e2e-ca.crt`) is generated per machine and gitignored.

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
