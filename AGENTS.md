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

## Environment notes

- This dev machine has **no Docker, no make, no local PostgreSQL**, but
  **Podman 6.0.2 is installed** (`podman compose` delegates to
  docker-compose). Tests that need a database use
  `CUSTOS_TEST_DATABASE_URL` when set, else an embedded-postgres fallback
  (slice B). CI runs a `postgres:16` service.
- Compose smoke recipe:

  ```sh
  bash deploy/compose/init-secrets.sh   # once; generates .secrets/
  podman compose -f deploy/compose/compose.yaml up -d --wait
  curl http://localhost:8080/health/ready
  podman compose -f deploy/compose/compose.yaml down -v
  ```

- `Makefile` targets exist for CI/Linux; run the underlying commands
  directly on Windows.
- DB tests each start an embedded PostgreSQL per package binary; on
  constrained machines bound parallelism with `go test -p 2 ./...`
  rather than serializing tests.
- `go test -race` needs cgo/gcc and cannot run on this machine; CI runs it.
- golangci-lint must be built with go1.27: `go install
  github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2` (the
  2.12.2 binary on PATH panics on go1.27 modules). A 2.13.2 binary built
  this way lives at `%TEMP%/glbin/golangci-lint.exe` on this machine.

## Commits

- Never add `Co-Authored-By` or other trailers to commits.
- Never commit secrets; gitleaks runs in CI.
