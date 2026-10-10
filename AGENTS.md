# Working with this repository

Custos is a multi-tenant Slurm gateway / HPC control plane. See `README.md`
and `docs/` for the architecture; **read `docs/*.md` before changing
design-level behaviour** — the docs are the contract.

- Module: `github.com/Exonical/custos`, Go 1.27.
- No application code before Milestone 1; design decisions live in
  `docs/adr/`. New architectural decisions require an ADR.

## Verify before reporting done

- `gofmt -l .` (must print nothing)
- `go vet ./...`
- `golangci-lint run ./...`
- `go test -race ./...`
- `go build ./cmd/custos`
- Docs changes: `npx markdownlint docs/**/*.md README.md CONTRIBUTING.md SECURITY.md AGENTS.md --disable MD013 MD033 MD024`
- `.harness/` holds the Harness hardened container and Go/Rust software
  pipeline templates, consumer pipelines and `scripts/hardening_manifest.py`. Validate the manifests with
  `python3 .harness/scripts/hardening_manifest.py validate --manifest <m> --dockerfile <f> --digest-pinning warn`;
  validate the YAML against `harness/harness-schema` v0 (`template.json`,
  `pipeline.json`).

Helm chart (`deploy/helm/custos`, ADR-033): `bash scripts/helm-check.sh` runs `helm dependency build`, `helm lint --strict`, kubeconform (incl. CRD schemas) and `helm unittest` with pinned tools (helm v4.3.0, kubeconform v0.8.0, helm-unittest v1.2.0; CI job `helm`). On Windows point `HELM_BIN` / `KUBECONFORM_BIN` at the binaries in `%TEMP%\helmbin` and run it from Git Bash. `scripts/helm-kind.sh` is the local/nightly kind smoke install on rootful Podman; on this machine run it inside the Podman machine: `podman machine ssh "cd /mnt/c/Users/bryce/Documents/custos && bash scripts/helm-kind.sh"` (see `docs/kubernetes.md`).

E2E (optional; local + nightly CI, never on PRs): `scripts/e2e.sh up`
brings up real Slurm 26.05 + Keycloak 26.7 under Podman, then
`CUSTOS_E2E=1 go test ./test/e2e/... -count=1 -v` — see `docs/e2e.md`.

## Environment notes

- This dev machine has **no Docker, no make, no local PostgreSQL**, but
  **Podman 6.0.2 is installed** (`podman compose` delegates to
  docker-compose). Tests that need a database use
  `CUSTOS_TEST_DATABASE_URL` — start the throwaway stack first (there is
  no embedded fallback; without the URL, DB tests skip, and
  `CUSTOS_TEST_REQUIRE_DB=1` makes them fail). CI runs a `postgres:18`
  service.

  ```sh
  bash scripts/testdb.sh up     # postgres:18 on 127.0.0.1:5433, tmpfs
  export CUSTOS_TEST_DATABASE_URL="$(bash scripts/testdb.sh url)"
  # PowerShell: $env:CUSTOS_TEST_DATABASE_URL = (bash scripts/testdb.sh url)
  ```

- Compose smoke recipe:

  ```sh
  bash deploy/compose/init-secrets.sh   # once; generates .secrets/
  podman compose -f deploy/compose/compose.yaml up -d --wait
  curl http://localhost:8080/health/ready
  podman compose -f deploy/compose/compose.yaml down -v
  ```

- `Makefile` targets exist for CI/Linux; run the underlying commands
  directly on Windows.
- DB tests clone a per-binary migrated template database on the shared
  test server — plain `go test ./...` at default parallelism is fine.
- `go test -race` needs cgo/gcc and cannot run on this machine; CI runs it.
- golangci-lint must be built with go1.27: `go install
  github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2` (the
  2.12.2 binary on PATH panics on go1.27 modules). A 2.13.2 binary built
  this way lives at `%TEMP%/glbin/golangci-lint.exe` on this machine.

## Web frontend

`web/` uses Node.js 26.7+ and pnpm 12.6.0. Work from that directory; the App
Router is rooted at `web/app/` (there is no `src/` directory). Plain `pnpm dev`
uses the real IdP/API and requires `web/.env.local`; `pnpm dev:mock` starts the
backend-free UI with mock data.

```sh
cd web
pnpm install --frozen-lockfile
pnpm dev
pnpm dev:mock
pnpm gen:api
pnpm lint
pnpm typecheck
pnpm test
pnpm build
pnpm e2e:smoke
```

The live browser test runs against the existing e2e stack with
`CUSTOS_E2E=1 pnpm e2e:live`. Do not reset e2e volumes without explicit
authorization.

## Commits

- Use Conventional Commits: `feat:`, `fix:`, `chore:`, `docs:`, `refactor:`,
  `test:`, `ci:`, `build:`, `perf:`, `security:` — with an optional scope,
  e.g. `feat(authn): ...`, `fix(workqueue): ...`. Subject in the
  imperative, ≤ 72 chars; body explains why.
- Never add `Co-Authored-By` or other trailers to commits.
- Never commit secrets; gitleaks runs in CI.
