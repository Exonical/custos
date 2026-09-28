# ADR-021: Auth.js stateless BFF sessions

Status: Accepted
Date: 2026-09-27

## Context

The browser needs to use Custos's bearer-only API without exposing access or
refresh tokens to JavaScript. The BFF must remain stateless and communicate
only with the Custos API and the OIDC provider; it must not connect to the
Custos database or depend on Redis.

## Decision

Use Auth.js v5, pinned exactly to `next-auth@5.0.0-beta.32`, with the JWT session
strategy and no adapter. The one generic OIDC provider is named `custos` and
uses Authorization Code + PKCE S256, state, and nonce. It supports Keycloak,
Authentik, and other conforming providers. Provider traffic uses the configured
CA through Auth.js's `customFetch` hook.

Auth.js stores the access token, refresh token, access-token expiry in
milliseconds, subject, and refresh error in its encrypted JWT cookie. The ID
token is not persisted. The session callback returns only `user.sub`, `name`,
`email`, the session expiry, and an optional error; access/refresh tokens never
appear in `/api/auth/session` and no `SessionProvider` is mounted. The Auth.js
cookie implementation chunks large values. `CUSTOS_WEB_AUTH_SECRETS` is a
comma-separated array of at-least-32-byte secrets: the first encrypts new JWTs,
and all entries can decrypt for rotation. Production sources the array from
OpenBao. The last-tenant preference is a separate non-HttpOnly cookie holding
only a validated tenant slug; it contains no credentials or session data.

The `jwt` callback refreshes access tokens within 60 seconds of expiry. Refresh
calls use a process-local single-flight keyed by the SHA-256 digest of the
refresh token. `proxy.ts` runs this refresh before protected Server Components;
if Auth.js only writes refreshed cookies on the response, the proxy also forwards
the refreshed Auth.js cookie chunks in the request headers so the current
render sees the new access token. Route Handlers refresh and persist their
updated Auth.js cookie directly. Refresh failure removes the tokens, sets
`RefreshTokenError`, and is treated as signed out.

CSRF on unsafe BFF and logout requests is a signed double-submit token. The
non-HttpOnly CSRF cookie contains `<random>.<HMAC-SHA256>`; the HMAC key is
derived from the first Auth.js secret using HKDF with info `custos-csrf`, and the
message binds the MAC to the current subject. The request also requires an exact
Origin and, when present, `Sec-Fetch-Site: same-origin`. Logout best-effort
revokes the refresh token, calls `signOut({ redirect: false })`, clears session,
CSRF, last-tenant, and OIDC cookies, then redirects to the IdP end-session
endpoint with `client_id` and `post_logout_redirect_uri` (no `id_token_hint`).

## Alternatives considered

- **Hand-rolled encrypted-cookie format and refresh lifecycle** — rejected in
  favor of Auth.js's maintained OAuth state, nonce, PKCE, encrypted JWT, and
  cookie-chunking implementation.
- **PostgreSQL-backed session table** — rejected because it would add a database
  connection, migration, role, and availability dependency to the web tier.
- **Browser-held access/refresh tokens** — rejected because XSS could steal
  bearer credentials and JavaScript-readable storage is not acceptable.
- **Better Auth** — not selected for this milestone; Auth.js already supports
  the generic OIDC flow and the required Next.js integration. Re-evaluate after
  Auth.js v5 exits beta.

## Consequences

- Auth.js v5 is still beta and is in maintenance mode under Better Auth. Pin
  `5.0.0-beta.32` exactly, review security advisories on every upgrade, and
  revisit the library choice when its maintenance status changes.
- Auth.js requires `trustHost: true`. The BFF pins `AUTH_URL` to
  `CUSTOS_WEB_PUBLIC_ORIGIN`, which Auth.js checks before consulting
  `X-Forwarded-Host` or `Host`, and `proxy.ts` returns 421 unless `Host` and any
  `X-Forwarded-Host` match the normalized origin host, including its port.
- Fail closed: protected access requires both a valid Auth.js session and a
  decrypted JWT with matching subject, access token, and no error. This avoids
  treating a truthy session result as proof of a valid token, including the
  behavior covered by CVE-2026-73421.
- There is no server-side session list to kill. Logout ends the IdP session and
  revokes the refresh token best-effort; a stolen access token remains valid
  until its short expiry.
- Auth.js chunks oversized JWT cookies, but browser cookie and request-header
  limits still apply. Keep OIDC token claims small and test the IdP's tokens.
- Single-flight is per instance only. With multiple replicas and Keycloak
  **Revoke Refresh Token** enabled with maximum reuse zero, simultaneous refresh
  attempts across replicas can terminate a session. Allow a small reuse count
  or leave that setting disabled.
- Secret rotation is additive: put the new secret first and retain the old
  secrets for decryption. Remove old entries after the maximum session lifetime
  has elapsed.
- The BFF remains trusted with refresh tokens in process memory and encrypted
  cookies. TLS, CSP, CSRF checks, no-token logging, and OpenBao-backed secret
  management remain required.

Source: `docs/frontend.md`, `docs/authentication.md`, and
`docs/threat-model.md` (TM-19–TM-21, TM-37).
