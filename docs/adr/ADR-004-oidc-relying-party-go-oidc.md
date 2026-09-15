# ADR-004: OIDC relying party with coreos/go-oidc/v3, bearer-only API, JIT provisioning

Status: Accepted
Date: 2026-09-14

## Context

Custos never issues identities and never sees passwords; it is an OIDC
relying party / resource server (docs/authentication.md). Two client
classes exist: CLI/automation using `Authorization: Bearer` access
tokens, and browsers reaching the API only through the Next.js BFF, which
holds tokens server-side. Token verification must defend against JWT
confusion: `alg=none`, HS/RS key confusion, wrong issuer/audience/typ,
and unknown-`kid` refresh floods (TM-03 in docs/threat-model.md).

## Decision

- Use `github.com/coreos/go-oidc/v3` for discovery, JWKS, and
  verification, wrapped by our own layer that enforces the configured
  allow-lists (algorithms RS256/ES256 only — never `none`/`HS*`; exact
  `iss`; `aud` intersection; `typ` in `accepted_token_types`; required
  scopes; ≤24h lifetime) and a rate-limited, singleflighted unknown-`kid`
  refresh (`jwks_refresh_min_interval`).
- The Go API accepts **only** bearer JWTs; the BFF attaches the access
  token it holds server-side.
- JWKS cache: in-memory per issuer, background refresh at
  `jwks_cache_ttl`, stale-while-error up to a 24h hard cap, then
  verification fails and readiness reports degraded.
- **JIT user provisioning**: first verified (`issuer`, `subject`) creates
  a `User` with no memberships; membership is granted by tenant admins or
  by tenant-configured IdP claim mapping (`source = idp`, revoked when
  the claim disappears, vs `source = manual`).
- Machine clients use client-credentials tokens from the same IdP and
  become `kind = service` users. Custos does not mint API keys in early
  milestones; if it ever does they are opaque, hashed, tenant-scoped —
  never Custos-signed JWTs.
- Startup fails closed on missing issuer, empty audiences, `alg=none`,
  `HS*`, non-HTTPS issuer (outside dev mode), or unreachable discovery.

## Alternatives considered

- **`lestrrat-go/jwx`** — rejected: more flexible but a larger surface;
  go-oidc is mature, small, and widely audited.
- **Stdlib only** — rejected: re-implementing JWS/JWK parsing is risk
  without benefit.
- **Building our own IdP** — rejected: out of scope; Custos is explicitly
  not an identity provider (architecture.md non-goals).
- **Custos-minted API keys** — rejected for v1: IdP client-credentials
  cover automation; a second credential system adds attack surface.

## Consequences

- Auth depends on an external IdP; its outage degrades verification after
  the JWKS stale cap, which is surfaced via readiness.
- All token-validation rules live in the wrapper, so switching libraries
  later is contained.
- A valid token proves identity only — every operation still passes
  `Authorizer.Check`.

Source: docs/authentication.md (all sections); docs/threat-model.md
TM-03/TM-04.
