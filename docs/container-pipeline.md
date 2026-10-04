# Container pipeline

Custos images are built by a reusable Harness pipeline template that follows
the stage layout of the Iron Bank pipeline using only free and open-source
tooling. Operator instructions, the step mapping and the variable reference
are in [`.harness/README.md`](../.harness/README.md); the decision is in
[ADR-027](adr/ADR-027-harness-container-pipeline.md).

## Stages

1. **Setup**: validate `hardening_manifest.yaml` and the Containerfile,
   scan the repository with Gitleaks and lint the Containerfile with
   hadolint (warn-only).
2. **Build, scan and publish**: import checksum-verified resources, build
   and push, generate SLSA provenance and an SBOM (run sequentially so the
   two cosign attestations do not race), scan with Trivy and Grype in
   parallel, scan the unpacked image with ClamAV, ingest the ClamAV SARIF
   into STO, sign the image, optionally promote it and always print a findings
   summary.

Findings gate on `fail_on_severity`. Exemptions are managed in STO.

Promotion runs only when the `promote` variable is true. GitHub triggers set
it: PR builds scan only, pushes to `main` sign and promote, and `v*` tags
sign and promote with the Git tag added to the release tags. See the Triggers
section of the README.

## Design notes

- Native Harness steps are used wherever one exists. Custom `Run` steps are
  limited to manifest validation and import, hadolint, the ClamAV scan with
  its SARIF conversion, skopeo promotion and the summary.
- All connectors and secrets are runtime inputs. The template is registered
  at account scope; consumer pipelines pass concrete image coordinates and
  leave credentials as `<+input>`.
- Downstream SCS steps address the pushed image by digest, never by tag.
- The scripts live in the repository so a manifest and the tool that checks it
  change together.

## Hardening manifest (version 1)

| Field | Required | Description |
| --- | --- | --- |
| `version` | yes | Must be `"1"` |
| `name` | yes | Lowercase repository path, e.g. `exonical/custos` |
| `labels` | yes | String map; `org.opencontainers.image.title` is used for reports |
| `maintainers` | yes | List of `{name, email}` |
| `tags` | no | Extra tags applied on promotion |
| `args` | no | Build argument documentation (scalar values) |
| `resources` | no | External inputs, see below |
| `container.user` | no | Runtime user; `0` and `root` are rejected |
| `container.expose` | no | List of ports |

Resources:

| Form | Requirements |
| --- | --- |
| `https://` or `http://` URL | `filename` (plain name), `validation: {type: sha256, value: <64 hex>}`; optional `auth: {type: basic, id: <ID>}` |
| `docker://` URL | Must end in `@sha256:<64 hex>`; pulled at build time, not downloaded |

Quote all-digit sha256 values so YAML does not read them as numbers.

`FROM` lines are checked for digest pins (aliases of earlier stages and
`scratch` are ignored, `--platform` is handled). Unpinned bases warn under
`digest_pinning: warn` and fail under `strict`.

## Validation

Templates and pipelines are validated against the `harness/harness-schema`
v0 JSON schemas (`template.json`, `pipeline.json`). The manifest tool is
exercised with `hardening_manifest.py validate` and `import`.

## Software pipeline

Go and Rust source is checked by a separate set of stage templates that
build, test and scan without publishing artifacts; see the Secure Software
Pipeline section of [`.harness/README.md`](../.harness/README.md) and
[ADR-028](adr/ADR-028-harness-software-pipeline.md).
