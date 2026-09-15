# ADR-010: Frontend auth — Next.js BFF with server-side session, no browser tokens

Status: Accepted
Date: 2026-09-14

## Context

Browsers need to use the bearer-only Go API without exposing tokens to
browser JavaScript (XSS → token theft is TM-20-adjacent; CSRF is TM-19).
OIDC tokens (especially Keycloak's) can exceed cookie size limits. The
browser path and CLI path must converge on the same API auth model.

## Decision

The Next.js server is a **Backend-for-Frontend**:

- Runs OIDC Authorization Code + PKCE (`openid-client` or Auth.js
  generic OIDC — provider decision deferred to Milestone 8).
- Holds `access_token`/`refresh_token`/`id_token` **server-side** in an
  encrypted session: sealed cookie (AES-GCM, ≤4KB) or, when tokens are
  large, a PostgreSQL `web_sessions` table in the same database.
- Sets `HttpOnly; Secure; SameSite=Lax; Path=/` session cookie; nothing
  token-like ever reaches browser JS.
- `/api/bff/*` Route Handlers proxy to the Go API attaching the bearer
  token + `X-Request-ID`; Server Components call the API directly with
  the session token. Tokens refresh server-side; refresh failure → login.
- **CSRF**: state-changing BFF routes require a double-submit token
  (`X-CSRF-Token` header matching a non-HttpOnly session-bound cookie)
  in addition to `SameSite=Lax`, plus `Origin`/`Sec-Fetch-Site` checks.
- API stays bearer-only and is never called directly by the browser;
  CORS is deny-by-default with an origin allow-list for non-browser
  clients only.

## Alternatives considered

- **SPA holding tokens in localStorage** — rejected: XSS steals tokens;
  no HttpOnly protection.
- **In-memory tokens + silent refresh via IdP iframe** — rejected:
  fragile under third-party-cookie blocking.
- **API-issued session cookies (Go API mints its own)** — rejected:
  duplicates OIDC logic in the API and makes CLI/browser paths diverge;
  the API stays bearer-only.

## Consequences

- The Next.js server is a **trusted component holding refresh tokens**:
  deploy it with API-level care (TLS, sealed-cookie key from OpenBao, no
  debug endpoints, short session TTL).
- CSRF handling is mandatory and layered (token + SameSite + Origin);
  mutations are non-GET only.
- Session storage must handle large tokens — plan for the `web_sessions`
  table rather than assuming sealed cookies suffice.

Source: docs/frontend.md (Architecture, Tradeoffs); docs/threat-model.md
TM-19/TM-20/TM-21.
