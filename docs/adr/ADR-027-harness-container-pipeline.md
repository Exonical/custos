# ADR-027: Harness hardened container pipeline

Status: Draft  
Date: 2026-10-03

## Context

Custos ships three container images (API, web frontend and script validator).
Releases should carry the supply-chain evidence expected of hardened images:
validated inputs, an SBOM, provenance, vulnerability and malware scan results
and a signature. Iron Bank's pipeline is a well-known reference for these
stages, but it is GitLab-specific. The team uses a Harness SaaS account with
the CI, STO and SCS modules, and only free and open-source scanners are
acceptable.

## Decision

- A single account-scope Harness Pipeline template,
  `account.hardened_container_pipeline` version `1`, implements the Iron Bank
  stage order (setup, import, build, scan, findings, publish). Consumer
  pipelines in the `default` org and `default_project` project reference it
  and supply image coordinates.
- Native steps are preferred: `BuildAndPushDockerRegistry`, `Gitleaks`,
  `AquaTrivy`, `Grype`, `CustomIngest`, `provenance`, `SscaOrchestration`
  (Syft) and `SscaArtifactSigning`. Custom `Run` steps cover only manifest
  validation and import, hadolint, ClamAV, skopeo promotion and the summary.
- Each image has a `hardening_manifest.yaml` (version 1) validated by
  `.harness/scripts/hardening_manifest.py`. Imported resources must carry a
  sha256 and are deleted on mismatch.
- Connectors, registry credentials and cosign key references are runtime
  inputs; the registry token is a Secret-type variable.
- Per-image GitHub webhook triggers decide behaviour through the `enable_signing`
  and `promote` variables: PRs build and scan, `main` pushes and `v*` tags
  sign and promote.
- Provenance and SBOM attestations run sequentially after the build to avoid
  concurrent cosign pushes, and downstream steps use the image digest.

## Alternatives considered

- **Port the Iron Bank GitLab pipeline directly:** rejected; it depends on
  GitLab CI, Iron Bank infrastructure and its offline build model.
- **GitHub Actions workflows:** rejected; the organisation standardises on
  Harness for CI, STO and SCS.
- **Per-image pipelines without a template:** rejected; the stages would
  drift between images.
- **Commercial scanners:** rejected by the FOSS-only constraint.
- **Keyless signing:** deferred; key-based signing is simpler to verify
  offline and works without OIDC trust configuration.

## Consequences

- Improvements to the pipeline are made once, in a new template version.
- The pipeline is not hermetic: builds use the network and base images are
  only digest-pinned when the Containerfile pins them.
- ClamAV scanning is best effort and promotion does not copy cosign
  signatures to the release repository.
- The template and pipelines are validated against the Harness v0 JSON
  schemas but must be registered and run in Harness to be fully proven.
- Consumer repositories must carry the manifest script.
