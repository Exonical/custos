# Harness hardened container pipeline

A reusable Harness NextGen pipeline template modelled on the
[Iron Bank pipeline](https://repo1.dso.mil/ironbank-tools/ironbank-pipeline).
It validates a `hardening_manifest.yaml`, imports checksum-verified
resources, builds and pushes the image, attests it (SLSA provenance and SBOM),
scans it with free/open-source scanners, signs it with cosign and optionally
promotes it to a release repository.

Layout:

- `templates/hardened_container_pipeline.yaml`: account-scope Pipeline
  template (`account.hardened_container_pipeline`, version `1`).
- `pipelines/*.yaml`: consumer pipelines for the three Custos images.
- `scripts/hardening_manifest.py`: manifest validation, artifact import,
  report and ClamAV-to-SARIF conversion (Python 3.11+, PyYAML).

Design notes and the manifest field reference are in
[`docs/container-pipeline.md`](../docs/container-pipeline.md); the decision is
recorded in [ADR-027](../docs/adr/ADR-027-harness-container-pipeline.md).

## Iron Bank job to Harness step mapping

| Iron Bank job | Harness step | Notes |
| --- | --- | --- |
| `setup` secret scan | `Gitleaks` (STO) | Fails on critical findings |
| `lint` (semgrep Dockerfile rules) | `Run` hadolint `v2.12.0-alpine` | Warn-only (`--no-fail`) |
| `hardening_manifest` validation | `Run` `hardening_manifest.py validate` | Exports `IMAGE_NAME`, `IMAGE_TITLE`, `IMAGE_TAGS` |
| `import-artifacts` | `Run` `hardening_manifest.py import` | sha256 verified, mismatching files deleted |
| `build` | `BuildAndPushDockerRegistry` (CI) | Layer caching, OCI labels, digest output |
| `provenance` | `provenance` (SCS) | Cosign key-based SLSA attestation |
| `sbom` | `SscaOrchestration` (SCS, Syft) | SPDX JSON, attested |
| `twistlock` / `anchore` | `AquaTrivy` and `Grype` (STO) | Run in parallel, gate on `fail_on_severity` |
| `clamav` | `Run` skopeo, `Run` ClamAV, `CustomIngest` (SARIF) | Best effort, see limitations |
| `findings` / VAT | STO issues and exemptions | Plus the `Findings Summary` step |
| `publish` signing | `SscaArtifactSigning` (SCS) | Cosign key-based, signature uploaded |
| `publish` promotion | `Run` `skopeo copy --all` | Only when `promote == "true"` (set by the main and tag triggers) |

## Prerequisites

- Harness SaaS account with the CI, STO and SCS (SSCA) modules.
- Harness Cloud build infrastructure (Linux, Amd64). The template uses
  `runtime: {type: Cloud}`.
- A Docker Registry connector for GHCR (`https://ghcr.io`) using a GitHub
  PAT with `write:packages` (and `read:packages`).
- A GitHub code repository connector for the codebase.
- A cosign key pair: `cosign generate-key-pair`. Store `cosign.key` as a
  Harness file secret and the password as a text secret.
- The pipeline template registered inline at account scope with identifier
  `hardened_container_pipeline` and version label `1`.

All connectors and secrets are runtime inputs; nothing in this directory
contains a literal secret.

## Onboarding a new image

1. Add a `Containerfile` and a `hardening_manifest.yaml` next to it (see the
   field table in `docs/container-pipeline.md`). Run the user as non-root.
2. Run the validator locally:

   ```sh
   python3 .harness/scripts/hardening_manifest.py validate \
     --manifest path/hardening_manifest.yaml --dockerfile path/Containerfile \
     --digest-pinning warn
   ```

3. Copy one of the files in `pipelines/`, change `name`, `identifier`,
   `manifest_path`, `dockerfile`, `context`, `image_repo`, `image_path` and
   `release_repo`.
4. Register the pipeline in the Harness project and supply the connector,
   secret and codebase inputs at run time.

## Variable reference

| Variable | Default | Purpose |
| --- | --- | --- |
| `manifest_path` | `hardening_manifest.yaml` | Manifest, relative to the repo root |
| `dockerfile` | `Containerfile` | Containerfile path |
| `context` | `.` | Build context |
| `registry_connector` | runtime | Docker Registry connector reference |
| `registry_domain` | `ghcr.io` | Registry host used by the scanners |
| `image_repo` | runtime | Full image repository, e.g. `ghcr.io/exonical/custos` |
| `image_path` | runtime | Repository path without host, e.g. `exonical/custos` |
| `image_tag` | `<+codebase.shortCommitSha>` | Fixed in the template; also tagged with the sequence ID |
| `release_repo` | runtime | Promotion target repository, used only when `promote` is true |
| `registry_username` | runtime | Registry user for scanners and skopeo |
| `registry_token` | runtime | Secret-type variable; select a Harness secret holding the registry token |
| `cosign_private_key` | runtime | Secret identifier of the cosign private key, e.g. `account.cosign_key` |
| `cosign_password` | runtime | Secret identifier of the cosign password, e.g. `account.cosign_password` |
| `fail_on_severity` | `critical` | `critical`, `high`, `medium`, `low` or `none` |
| `digest_pinning` | `warn` | `off`, `warn` or `strict` for Containerfile `FROM` lines |
| `enable_sbom` | `true` | Syft SBOM and attestation |
| `enable_slsa` | `true` | SLSA provenance attestation |
| `enable_signing` | `true` | Cosign image signature |
| `enable_clamav` | `true` | ClamAV scan and SARIF ingestion |
| `enable_dockerfile_lint` | `true` | hadolint |
| `promote` | `false` | Copy to `release_repo`, also tagging the Git tag on tag builds; set by the triggers, manual runs default to false |

Notes:

- The SCS steps take Harness secret **identifiers** (for example
  `account.cosign_key`), not secret values, in `cosign_private_key` and
  `cosign_password`.
- `registry_token` is a Secret-type pipeline variable. Harness resolves it
  to the secret value inside steps, so no nested `secrets.getValue()`
  expression is needed for the Trivy/Grype `access_token` or the skopeo
  environment.
- The Build And Push step publishes the image digest, which the SLSA, SBOM,
  signing, ClamAV and promotion steps use. The template assumes the digest
  expression yields `sha256:<hex>` and passes `image_repo@digest` (full
  registry host included) to the SCS steps. Skopeo steps normalise a missing
  `sha256:` prefix.
- `repoName` is required because the codebase connector is an account-level
  connector. Use `owner/repo` for a host-level GitHub connector or `repo` for
  an organisation-level one.
- The build date build-arg is `<+pipeline.startTs>` (epoch milliseconds).
- The scripts are read from the cloned repository, so every consuming
  repository must carry `.harness/scripts/hardening_manifest.py`.

## Triggers

Each image pipeline has three GitHub webhook triggers in
`triggers/<pipeline_id>/`, using connector `org.gh_personal` and repo
`custos`. The `changedFiles` filters are regular expressions on the
changed paths; the tag trigger has none, so every image is released on every
`v*` tag.

| Trigger | Event | Conditions | Signing / promote | Build |
| --- | --- | --- | --- | --- |
| `pr` | Pull request (open, reopen, synchronize, ready for review) | Target `main`, changed files match | off / off | PR number |
| `main_push` | Push | Branch `main`, changed files match | on / on | Branch |
| `tag_release` | Push | `refs/tags/v*` and ref created | on / on | Tag |

Changed-files regex per pipeline:

| Pipeline | Regex |
| --- | --- |
| `custos_container` | `^(cmd/\|internal/\|pkg/\|api/\|go\.(mod\|sum)$\|Containerfile$\|hardening_manifest\.yaml$\|\.harness/).*` |
| `custos_validator_container` | `^(cmd/custos-validator/\|internal/\|pkg/\|go\.(mod\|sum)$\|deploy/Containerfile\.validator$\|deploy/hardening_manifest\.validator\.yaml$\|\.harness/).*` |
| `custos_web_container` | `^(web/\|api/openapi/\|internal/workflowtemplates/catalog/\|\.harness/).*` |

The triggers pass these fixed identifiers, which must be created before the
first run:

- `account.ghcr`: Docker Registry connector at account scope, URL
  `https://ghcr.io`, username `bryce`, password secret `ghcr_token`.
- `account.ghcr_token`: text secret holding a GitHub PAT with
  `write:packages` and `read:packages`.
- `account.cosign_private_key`: file secret with `cosign.key`.
- `account.cosign_password`: text secret with the cosign key password.

Notes:

- `org.gh_personal` needs `admin:repo_hook` to register the webhooks.
- PR builds from forks would run with these secrets. Restrict the PR trigger
  to same-repo branches (or enable GitHub's fork-approval setting) before the
  repository becomes public.
- Promotion is gated only by the `promote` variable. The `main_push` and
  `tag_release` triggers set it to true; manual runs default to false.

## Limitations

- This is not an offline or hermetic build like Iron Bank: the build pulls
  base images and packages over the network, and base images are only
  digest-pinned when the Containerfile does so (`digest_pinning`).
- ClamAV is best effort: the image is exported with skopeo, layers are
  unpacked naively (whiteouts are not applied) and signatures are fetched
  with `freshclam` at run time.
- STO exemptions play the role of Iron Bank's VAT; there is no separate
  approval workflow.
- Promotion with `skopeo copy` copies the image only. Cosign signatures and
  attestations are stored in the source repository and are not copied.
- `resources` with `auth` in the manifest read `RESOURCE_AUTH_<ID>_USERNAME`
  and `RESOURCE_AUTH_<ID>_PASSWORD`; the template does not define those
  variables, so such resources need a template extension.
- Harness Cloud with the SCS provenance step provides SLSA L3 only when the
  platform requirements for non-forgeable provenance are met; treat the result
  as L2 unless verified.

## Secure Software Pipeline (Go/Rust)

`templates/secure_software_pipeline.yaml` (`account.secure_software_pipeline`,
version `1`) builds, tests and scans first-party Go and Rust code. It
publishes no artifacts. It composes three account-scope stage templates in a
single `parallel:` block; `pipelines/custos_software.yaml` is the consumer for
this repository (Go only, Opengrep rules `go`).

| Stage template | Step | Tool | Kind | Gates |
| --- | --- | --- | --- | --- |
| `source_security` | Secret Scan | Gitleaks | Native STO | `fail_on_severity` |
| | Opengrep | Opengrep `v1.30.0` + Semgrep CE rules | Run | None (exit 0; STO gates) |
| | Ingest Opengrep | `CustomIngest` (SARIF) | Native STO | `fail_on_severity` |
| | OSV Scan | OSV-Scanner (go.mod, go.sum, Cargo.lock) | Native STO | `fail_on_severity` |
| | Repository SBOM | Syft, CycloneDX JSON via `SscaOrchestration` | Native SCS | Not gating, no attestation |
| `go_build_test` | Format and Vet | gofmt, `go vet` | Run | Unformatted files, vet errors |
| | golangci-lint | golangci-lint `v2.13.2` | Run | Lint findings |
| | Test | gotestsum `v1.13.0`, JUnit report, coverage profile | Run | Test failures |
| | govulncheck | govulncheck (`golang.org/x/vuln` `v1.8.0`) | Run | Reachable vulnerabilities |
| | Build | `go build ./...` | Run | Build errors |
| `rust_build_test` | Format | `cargo fmt --check` | Run | Formatting |
| | Clippy | `cargo clippy -D warnings` | Run | Lints |
| | Test | cargo-nextest `0.9.145` (JUnit), plus doc tests for library crates | Run | Test failures |
| | cargo-deny | cargo-deny `0.20.2` | Run | Advisories, bans, sources, licenses with `deny.toml` |
| | cargo-audit | cargo-audit `0.22.2` | Run | RustSec advisories |
| | Build | `cargo build --locked --release` | Run | Build errors |

Downloaded binaries (Opengrep, nextest, cargo-deny, cargo-audit) are pinned
and verified with sha256 before use. Go and Rust stages use Cache Intelligence
with the module, build and registry caches under `/harness/.cache`.

### Variables

| Variable | Default | Purpose |
| --- | --- | --- |
| `build_go` | `true` | Run the `go_build_test` stage |
| `build_rust` | `false` | Run the `rust_build_test` stage |
| `go_dir` | `.` | Directory of the Go module |
| `go_image` | `golang:1.27` | Image for format, test, vuln and build steps |
| `golangci_lint_image` | `golangci/golangci-lint:v2.13.2` | Image for golangci-lint |
| `go_test_flags` | `-race -count=1` | Flags before `-coverprofile` |
| `rust_dir` | `.` | Directory of the Cargo project |
| `rust_image` | `rust:1.98.1-bookworm` | Image for all Rust steps |
| `cargo_features` | `--all-features` | Feature flags for clippy, test and build |
| `fail_on_severity` | `high` | `critical`, `high`, `medium`, `low` or `none` for STO steps |
| `opengrep_rule_paths` | `go rust` | Space-separated directories of the pinned `semgrep-rules` checkout |
| `opengrep_exclude` | `vendor,testdata,target,node_modules` | Comma-separated Opengrep `--exclude` patterns |

The Go and Rust stages are skipped through a `when` condition that the
pipeline template feeds from `build_go` and `build_rust`; the stage templates
expose it as a runtime input because a template stage reference has no `when`
field of its own.

### Onboarding a Go or Rust repository

1. Copy `pipelines/custos_software.yaml`, change `name`, `identifier`,
   `repoName`, and set `build_go` / `build_rust`, the directories and the
   Opengrep rule paths.
2. Rust projects need a committed `Cargo.lock` (steps use `--locked`).
3. Register the three stage templates, then the pipeline template, then the
   pipeline.

Notes for this repository: `.golangci.yml` is honoured by golangci-lint, and
the database tests skip when `CUSTOS_TEST_DATABASE_URL` is unset, so the Test
step passes without a database.

### Opengrep rules and the Semgrep Rules License

The rules come from `https://github.com/semgrep/semgrep-rules` pinned to
commit `a84ff9cc2453ca91d581380de4b8b3f272f6f4be`. The
[Semgrep Rules License](https://github.com/semgrep/semgrep-rules/blob/develop/LICENSE)
is **not** an OSI open-source license: the rules are free for internal use,
and redistribution is not allowed. Do not copy them into other repositories
or publish them; the pipeline fetches them at run time. Opengrep itself is
LGPL-licensed.

Add organisation rules by committing Semgrep-syntax rule files under
`.opengrep/` in the scanned repository; the step adds the directory
automatically when it exists. Add a `deny.toml` at `rust_dir` to enable
cargo-deny license policy checks; without it the step prints a warning and
runs only `advisories bans sources`.

Opengrep scans generated code too (for example `*.gen.go`); add patterns to
`opengrep_exclude` to skip it.
