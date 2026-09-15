# Frontend (Next.js)

## Architecture

```text
Browser ──(session cookie)──► Next.js server (BFF) ──(Bearer JWT)──► custos API
                                    │
                                    └─ OIDC Authorization Code + PKCE with the IdP
```

The Next.js server is a **Backend-for-Frontend**:

- Runs the OIDC Authorization Code flow with PKCE (`openid-client` or
  Auth.js/`next-auth` with the generic OIDC provider — decision in
  Milestone 8; requirements below are provider-independent).
- Stores `access_token`, `refresh_token`, `id_token` server-side in an
  encrypted session (sealed cookie, AES-GCM, ≤4KB; or a server session store
  if tokens are large — Keycloak tokens often are, so plan for a
  PostgreSQL-backed session table in the same database, `web_sessions`).
- Sets an `HttpOnly; Secure; SameSite=Lax; Path=/` session cookie. Nothing
  token-like ever reaches browser JavaScript.
- Route Handlers under `/api/bff/*` proxy to the Go API, attaching the
  bearer token, `X-Request-ID`, and forwarding the tenant path. Server
  Components call the Go API directly with the session's token.
- Refreshes tokens server-side before expiry; on refresh failure the user
  is redirected to login.
- CSRF: all state-changing BFF routes require a double-submit token
  (`X-CSRF-Token` header matching a non-HttpOnly cookie bound to the
  session) **in addition to** `SameSite=Lax`, plus `Origin`/`Sec-Fetch-Site`
  checks. Lax alone allows top-level GET navigations, which is fine since
  mutations are non-GET; the token covers browsers/edge cases.

### Tradeoffs

| Approach | Security | Complexity | Verdict |
| --- | --- | --- | --- |
| SPA holds tokens in `localStorage` | XSS → token theft; no HttpOnly protection | low | rejected |
| SPA with tokens in memory + refresh via IdP iframe | fragile with 3rd-party-cookie blocking | medium | rejected |
| BFF with server-side session (chosen) | tokens never in browser; CSRF must be handled; server holds refresh tokens (encrypt at rest, short session TTL) | medium | chosen |
| Go API issues its own session cookie | duplicates OIDC logic in the API; CLI and browser paths diverge | medium | rejected (API stays bearer-only) |

Cost of BFF: the Next.js server becomes a trusted component holding
refresh tokens; it must be deployed with the same care as the API (TLS,
sealed cookie key from OpenBao, no debug endpoints).

## Stack

- Next.js (current stable at Milestone 8), TypeScript `strict`, App Router,
  React Server Components for read-heavy pages.
- UI library: **shadcn/ui on Radix primitives + Tailwind**. Accessible,
  unopinionated, desktop-friendly dense tables; components are vendored so
  no runtime dependency lock-in. Alternative: MUI (heavier, more opinionated),
  Mantine (good, but smaller ecosystem for data tables).
- Tables: TanStack Table (virtualized for jobs list). Charts: Recharts,
  used sparingly. Forms: react-hook-form + zod, with zod schemas generated
  from the OpenAPI document (`openapi-typescript` + `openapi-zod-client`).
- Workflow editor: **React Flow (@xyflow/react)** for the canvas; Monaco
  editor for YAML with the JSON Schema from `pkg/workflowspec` for
  completion and inline validation.

## Visual ⇄ YAML consistency

Single source of truth in the editor state is the **canonical spec
object** (TypeScript type generated from the JSON Schema). Both views are
projections:

- Visual view renders nodes from `spec.tasks` and edges from `dependsOn`;
  node positions come from a separate `layout` object.
- YAML view is `yaml.stringify(spec)` with a stable key order; edits are
  parsed back and schema-validated before replacing the spec (invalid YAML
  keeps the last valid spec and shows errors inline; the visual tab is
  disabled until valid).
- Backend `validate` endpoint is called (debounced) and its path-addressed
  errors are mapped onto nodes/fields.
- Saving sends `{spec, layout}`; the backend hashes only `spec`.

## Pages (Milestone 8)

Dashboard, Jobs (list + detail), Workflows (list, versions, editor,
executions), Executions (DAG progress view reusing the canvas read-only),
Clusters (health, nodes, partitions, queues), Usage, Admin (tenants,
members, clusters, policies, secret references, audit log, dead work items).
Tenant switcher in the header; the current tenant is part of the URL
(`/t/{tenant}/...`) mirroring the API.

## Security headers (BFF and API)

`Content-Security-Policy` (nonce-based scripts, `frame-ancestors 'none'`,
`connect-src 'self'`), `Strict-Transport-Security`, `X-Content-Type-Options:
nosniff`, `Referrer-Policy: strict-origin-when-cross-origin`,
`Permissions-Policy` minimal. API CORS: deny by default; allow-list of
origins from config only for bearer-token clients (browser never calls the
API directly).

## Tooling

eslint (next/core-web-vitals + typescript-eslint strict), prettier,
Vitest + Testing Library for units, Playwright for a small e2e smoke set
against fake OIDC + fake API, `npm audit`/Dependabot for dependency
scanning.
