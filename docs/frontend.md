# Frontend (Next.js)

## Architecture

```text
Browser ──(session cookie)──► Next.js server (BFF) ──(Bearer JWT)──► custos API
                                    │
                                    └─ OIDC Authorization Code + PKCE with the IdP
```

The Next.js server is a **Backend-for-Frontend**:

- Uses `next-auth@5.0.0-beta.32` with one generic OIDC provider, Authorization
  Code with PKCE S256, state, and nonce. Auth.js `customFetch` uses the IdP CA;
  `openid-client` is retained for refresh, revocation, and end-session URLs.
- Uses Auth.js's encrypted JWT strategy with no adapter or database. The JWT
  contains access/refresh tokens, `expires_at` in milliseconds, subject, and an
  optional refresh error; it never stores the ID token. Auth.js handles cookie
  chunking. `/api/auth/session` exposes only subject, name, email, expiry, and
  error; no `SessionProvider` is used.
- The Auth.js session cookie is HttpOnly, Secure, SameSite=Lax, Path=/. The
  non-HttpOnly CSRF token is `<random>.<HMAC-SHA256>` and is bound to the subject
  using an HKDF key derived from `CUSTOS_WEB_AUTH_SECRETS`.
- Route Handlers under `/api/bff/*` proxy to the Go API, attaching the bearer
  token, request ID, and forwarding the validated tenant path. Server
  Components use the same server-side API client directly, not HTTP to the BFF.
- The `jwt` callback refreshes access tokens within 60 seconds of expiry. A
  process-local single-flight coalesces refreshes. The protected-page proxy
  forwards the refreshed Auth.js cookie chunks into the same request so Server
  Components see the new token; refresh failure fails closed.
- Every unsafe BFF and logout request requires the CSRF header to match the
  signed cookie, plus an exact Origin and, when present,
  `Sec-Fetch-Site: same-origin` check. Protected server access verifies both
  `auth()` and a valid `getToken()` result.

## Local development with e2e Keycloak

Copy `web/.env.example` to `web/.env.local`, run `scripts/e2e.sh up`, and
use the e2e CA files for the IdP and API. The example uses issuer
`https://keycloak.e2e:8443/realms/custos` and client `custos-e2e`; its imported
realm allows `http://localhost:3000/api/auth/callback/custos` and the
`http://localhost:3000` web origin. Add `keycloak.e2e` to the hosts file as
shown by the script. If port 3000 is occupied by the TLS e2e web service, set
`CUSTOS_WEB_PUBLIC_ORIGIN=http://localhost:3001` and run `pnpm dev --port 3001`;
the additional localhost:3001 callback and web origin are allowed in the realm.

## Mock mode (development only)

For UI work without Keycloak or the Go API, run `cd web && pnpm dev:mock`.
The mock IdP presents Alice Researcher, Platform Admin, and Bob Newcomer; the
mock API serves deterministic tenant, cluster, project, and job data. Use
`MOCK_OIDC_AUTO_LOGIN=alice` to skip the picker, `MOCK_OIDC_TOKEN_TTL` to
exercise refresh, `MOCK_API_LATENCY_MS` to add latency, and `mock_fail=<status>`
on an API request to exercise friendly error pages. Data resets when the mock
API process restarts. These fixtures use no real secrets and are for local
and container development only, never production.

Build and run the dev container from the repository root:

```sh
podman build -f web/Containerfile --target dev -t custos-web-mock .
podman run --rm -p 127.0.0.1:3000:3000 -p 127.0.0.1:4300:4300 custos-web-mock
```

Open `http://localhost:3000`; port 4300 is published for the browser's mock
OIDC redirects. To use a different local UI port, pass it to `pnpm dev:mock`
(for example, `pnpm dev:mock --port 3001`). Plain `pnpm dev` remains the
real-Keycloak/API mode configured by `web/.env.local`.

### Tradeoffs

| Approach | Security | Complexity | Verdict |
| --- | --- | --- | --- |
| SPA holds tokens in `localStorage` | XSS → token theft; no HttpOnly protection | low | rejected |
| SPA with tokens in memory + refresh via IdP iframe | fragile with 3rd-party-cookie blocking | medium | rejected |
| BFF with Auth.js encrypted JWT session (chosen) | tokens never in browser; CSRF is required; no session database; cookie size and key rotation need care | medium | chosen |
| Go API issues its own session cookie | duplicates OIDC logic in the API; CLI and browser paths diverge | medium | rejected (API stays bearer-only) |

Cost of BFF: the Next.js server remains trusted with refresh tokens in process
memory and encrypted Auth.js cookies. Deploy it with API-level care (TLS, the
`CUSTOS_WEB_AUTH_SECRETS` array sourced from OpenBao in production, no token
logging, and no debug endpoints).

## Stack

- Next.js 16 App Router at the `web/` project root, TypeScript `strict`, and
  standalone production output. Server Components handle read-heavy pages.
- UI library: **shadcn/ui on Radix primitives + Tailwind 4**. Accessible,
  desktop-friendly dense tables; components are vendored.
- Jobs use TanStack Table with API keyset cursors and state/project filters.
  Forms use react-hook-form + zod; API types are generated from OpenAPI.
- M8-A implements Dashboard and Jobs list/detail. Workflows, executions,
  clusters, usage, and admin pages are future sections; Monaco, React Flow,
  and charts are out of scope for this slice.

## Future workflow-editor contract (not implemented in M8-A)

The following consistency rules are retained for a future workflow editor;
M8-A does not include the editor.

### Visual ⇄ YAML consistency

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

## Pages (M8-A)

Implemented: tenant selection, Dashboard, and Jobs (keyset-paginated list,
detail, read-only ExecutionSpec/resource usage, and CSRF-protected cancel).
The shell includes tenant switching, logout, and disabled coming-soon entries
for Workflows, Executions, Clusters, Usage, and Admin. Tenant pages live under
`/t/{tenant}/...`, mirroring the API. The browser UI never calls the API
directly; Server Components use the server API client and mutations use the BFF.

## Security headers (BFF and API)

`Content-Security-Policy` (nonce-based scripts, `frame-ancestors 'none'`,
`connect-src 'self'`; `unsafe-eval` is added only for Next development),
`Strict-Transport-Security`, `X-Content-Type-Options:
nosniff`, `Referrer-Policy: strict-origin-when-cross-origin`,
`Permissions-Policy` minimal. API CORS: deny by default; allow-list of
origins from config only for bearer-token clients (browser never calls the
API directly).

## Tooling

eslint (next/core-web-vitals + typescript-eslint strict), prettier,
Vitest + Testing Library, Playwright smoke against fake OIDC/API, and a live
Playwright check against the e2e stack. Use pnpm 12.6.0 with frozen lockfile
installs; production dependencies are audited non-blockingly for now.
