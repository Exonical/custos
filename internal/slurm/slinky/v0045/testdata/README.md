# Fixture provenance

`ping.json`, `partitions.json`, `nodes.json`, `reservations.json`,
`diag.json`, `accounts.json`, `qos.json` and `associations.json` are
**recorded responses** from a live Slurm **26.05.4** cluster
(`ghcr.io/slinkyproject/*:26.05-ubuntu26.04` images, data_parser
`v0.0.45`), fetched through the TLS-terminating nginx in the e2e stack
(`deploy/e2e/`, see `docs/e2e.md`) with `X-SLURM-USER-NAME: custos` and a
minted JWT. No secrets are embedded — the JWT is sent as a request
header and never appears in a response body.

`ping_errors.json`, `wrong_version.json` and `nil_heavy.json` are
hand-written adversarial variants (error payloads, version mismatch,
null-heavy nodes) kept from the original schema-derived set.

To re-record, bring the stack up (`scripts/e2e.sh up`) and re-fetch the
GET endpoints listed in `conformance_test.go`'s stub map.
