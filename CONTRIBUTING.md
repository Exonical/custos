# Contributing

## Current phase: Milestone 0 (docs-first)

The repo is design-only right now. Changes to `docs/*.md` are the work
product; keep them consistent with each other and with the ADRs in
`docs/adr/`. Do not commit Go/TS source until Milestone 1 begins per
`docs/milestone-1.md`.

## ADR process

- Any new architectural decision — new external dependency category, new
  trust boundary, or a change to a recorded decision — requires an ADR in
  `docs/adr/` before or alongside the implementing change.
- Copy the template from an existing ADR: `Context` / `Decision` /
  `Alternatives considered` / `Consequences`.
- Accepted ADRs are immutable; supersede with a new ADR that references
  the old one.

## Development standards (planned, from docs/milestone-1.md)

When code lands, every change must be green on:

- `gofmt` (no diff), `go vet`
- `golangci-lint` — `govet`, `staticcheck`, `errcheck`, `gosec`, `revive`,
  `forbidigo` (no `fmt.Print*`/`log.Print*`, no logging `http.Header`),
  `depguard` (domain packages may not import `net/http`, `pgx`, Slinky,
  OpenBao)
- `go test -race ./...` against a real PostgreSQL (testcontainers)
- `govulncheck`
- `make openapi-check` — regenerated OpenAPI artifacts must not drift
- Frontend (Milestone 8+): eslint (`next/core-web-vitals` +
  typescript-eslint strict), TypeScript `strict`, prettier, Vitest,
  `npm audit`

A permanent CI test scans registered Prometheus descriptors for
forbidden label names — keep the label allow-list in
`docs/observability.md`.

## Commits and pull requests

- Small, focused commits; one logical change per PR.
- Each step of the milestone plan ends green: build, lint,
  `go test -race`.
- PRs describe the design-doc section they implement; deviations from the
  docs need an explicit note (and, if architectural, an ADR first).
- Do not commit secrets; secret scanning (gitleaks) runs in CI.
