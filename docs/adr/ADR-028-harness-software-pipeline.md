# ADR-028: Harness secure software pipeline for Go and Rust

Status: Draft  
Date: 2026-10-03

## Context

Custos and future first-party components are written in Go and Rust. Source
quality and supply-chain checks (lint, tests, dependency vulnerabilities,
licenses, secrets and static analysis) should run on every change without
building a container image or publishing an artifact, which
[ADR-027](ADR-027-harness-container-pipeline.md) already covers. The
tooling must be free and open source.

## Decision

- Three account-scope Harness stage templates (`source_security`,
  `go_build_test`, `rust_build_test`) are composed by one pipeline template,
  `account.secure_software_pipeline`, in a `parallel:` block. Stage skipping
  uses a `when` condition exposed as a runtime input of the Go and Rust stage
  templates and set from the `build_go` and `build_rust` pipeline variables.
- Native steps are used for Gitleaks, OSV-Scanner, `CustomIngest` and the
  repository SBOM (Syft through `SscaOrchestration`, without attestation).
  Other tools run in `Run` steps with pinned images.
- SAST uses Opengrep instead of the Harness Semgrep step. The pinned
  Opengrep release binary (sha256 verified) scans with Semgrep CE rules from
  `semgrep/semgrep-rules` pinned to a commit, plus optional in-repo rules in
  `.opengrep/`. SARIF output is ingested into STO with `CustomIngest`, so STO
  applies severity gating and exemptions.
- The Semgrep Rules License is not OSI open source (free for internal use, no
  redistribution). The rules are fetched at run time and never copied into
  this repository.
- Go uses gofmt, `go vet`, golangci-lint, gotestsum, govulncheck and a build.
  Rust uses rustfmt, clippy, cargo-nextest, cargo-deny, cargo-audit and a
  release build; cargo-audit is kept alongside cargo-deny at the user's
  request. Downloaded binaries are pinned and checksum-verified.

## Alternatives considered

- **Harness Semgrep step:** rejected by the requirement to use Opengrep.
- **Opengrep with the `p/` Semgrep registry or `--config auto`:** rejected;
  it needs network access to the registry and is not reproducible.
- **Writing and maintaining our own rules only:** rejected as insufficient
  coverage for the first release; `.opengrep/` supports additions.
- **One monolithic pipeline per language:** rejected; stage templates are
  reusable by other pipelines.
- **Pipeline-level conditional on a stage reference:** the template stage
  schema has no `when` field, so the condition lives in the stage template.

## Consequences

- Security gating is centralised in STO; the Opengrep step itself never fails
  on findings.
- Rule updates require changing the pinned commit in the template and
  releasing a new template version.
- Consumers must retain the Semgrep Rules License restrictions.
- The templates are validated against the Harness v0 schemas and the Run
  commands were smoke-tested in containers, but STO ingestion behaviour
  (`detection: auto` targets) is proven only by running in Harness.
