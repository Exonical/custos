# ADR-012: Script validation tooling — mvdan/sh in-process, external linters in a sidecar

Status: Accepted
Date: 2026-09-14

## Context

The validation pipeline (docs/script-validation.md) must parse and lint
untrusted shell (and later Python/YAML/JSON) scripts without executing
them, without letting the tooling itself become an attack surface
(TM-33), and while respecting licenses — Custos is Apache-2.0 and
ShellCheck, the de-facto shell linter, is GPLv3. Validators must be
deterministic so persisted results are reproducible and diffable.

## Decision

- **In-process, pure Go**: parsing uses `mvdan.cc/sh/v3/syntax` (MIT) for
  bash/POSIX; `sbatchscan` reuses its comment nodes and word-mode parser
  for directive detection and tokenization — never regex-only. Go
  validators (`shsyntax`, `sbatchscan`, `envcheck`, `softwareenv`) run
  in-process with fuzz tests, bounded input, and context timeouts.
- **Out-of-process external tools**: ShellCheck (GPLv3) — and later `ruff`
  and `python -m ast` — run in a **validator sidecar**: a separate
  container/systemd service with no credentials, no ServiceAccount token,
  no network, read-only rootfs, CPU/memory/pids/time limits, listening
  only on localhost. The worker maps its JSON output to `Diagnostic`s.
- **Small port**: validators implement `ScriptValidator` (`Name`,
  `Languages`, `Validate`) and are run by a deterministic pipeline that
  selects by language, applies per-validator timeouts, merges
  diagnostics, sorts them, and applies `ValidationPolicy` severity
  mapping.
- **Fail closed**: a validator that errors/times out yields synthetic
  `ERROR CUSTOS900 validator <name> unavailable` (configurable to WARNING
  for dev only).
- **Persistence**: `ScriptValidation` records the script digest, merged
  diagnostics, **tool versions**, and `policy_version`, with a 30-day
  expiry; admission re-validates when stale.

## Alternatives considered

- **`bash -n`** — rejected: executes the bash binary on untrusted input
  in the control plane; still a full parser surface but with worse
  isolation and no positions for diagnostics.
- **Regex-only directive detection** — rejected: aliases, quoting,
  whitespace, CRLF, and heredoc placement defeat regexes; the bypass
  corpus exists precisely to pin tokenizer behavior.
- **Linking ShellCheck** — rejected: GPLv3 license incompatibility with
  Apache-2.0 distribution and a Haskell toolchain dependency; the process
  boundary is a standard compliant integration.
- **Running external validators inside the API/worker process** —
  rejected: a crash or exploit in the linter would share the process's
  DB/OpenBao credentials; the sidecar has none.
- **No persistence (re-validate every time)** — rejected: re-validation
  per execution is wasted work and produces non-reproducible results when
  tool versions drift; persisted results with versions make currency
  checkable.

## Consequences

- A new validator = implement the interface, register it, add it to the
  pipeline; language support is additive (`Languages()`).
- Sidecar availability is a hard dependency for ShellCheck diagnostics in
  production; its absence fails closed via `CUSTOS900`.
- Tool upgrades change `tool_versions`, which can invalidate cached
  validations — an intentional correctness property.
- License compliance is structural: GPLv3 code is never linked, only
  invoked across a process boundary (risk row in architecture.md §10).

Source: docs/script-validation.md (Validator port, Sandboxing, Testing,
Milestone placement); docs/threat-model.md TM-33.
