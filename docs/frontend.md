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
- UI library: **shadcn/ui `base-nova` on Base UI primitives + Tailwind 4**.
  Accessible, desktop-friendly dense tables; components are vendored.
- Jobs and executions use full-width, horizontally contained tables with API
  cursors and URL-backed filters. Forms use react-hook-form + zod; API types are
  generated from OpenAPI and the workflow document type is generated from
  `internal/workflowspec/schema/v1alpha1.json` with `pnpm gen:spec`.
- Workflow version graphs use React Flow with dagre fallback layout. Read-only
  YAML and frozen ExecutionSpecs use Monaco from the bundled `monaco-editor`
  package through `@monaco-editor/react`; the loader is configured with the
  local Monaco instance and its worker is a same-origin module worker. YAML
  serialization uses `yaml`.
- Workflow UI batch 2a implements read-only workflow/version and execution
  views plus Run and Cancel. The editor (graph/YAML editing, validation, draft
  save, publish, and deprecate) is batch 2b, not part of 2a.

## Design system

The UI uses shadcn/ui `base-nova` on Base UI primitives, Tailwind 4, and a
single dark theme. `app/globals.css` defines the OKLCH tokens `--background`,
`--foreground`, `--card`, `--popover`, `--secondary`, `--muted`,
`--muted-foreground`, `--border`, `--input`, `--primary`, `--accent`, `--ring`,
`--destructive`, chart colors, and `--status-*`. IBM Plex Sans is used for body
copy; IBM Plex Mono is used for IDs, timestamps, numbers, table headings, and
badges. Both use `next/font/local` with the latin WOFF2 assets from exact-pinned
`@fontsource/ibm-plex-sans@5.3.0` and `@fontsource/ibm-plex-mono@5.3.0`, so builds
need no Google Fonts network access. Status tokens distinguish running/active green, queued/draining amber,
failed red, completed steel, and canceled gray. New UI must use these semantic
tokens rather than raw Tailwind palette colors. There is no light theme or
switcher for now.

## Workflow editor (batch 2b)

The draft editor provides a React Flow canvas and Properties/YAML workspace.
YAML is the source of truth; document edits preserve comments and key order,
validation is debounced, and spec/layout saves use optimistic version checks.
The editor supports Task and Service nodes, service `after start` edges,
single-node image pull-secret handles, per-node/per-CPU memory, workflow secret
handles, and draft test runs. Test runs require a clean, valid draft and lock
its spec until the execution is terminal; layout edits remain independent.
Executions display a TEST badge and expose a test-run filter.

### Visual ⇄ YAML consistency

The canonical workflow document (typed from the JSON Schema) is the shared
source for both views:

- Visual view renders nodes from `spec.tasks` and edges from `dependsOn`;
  node positions come from a separate `layout` object.
- YAML editing uses the YAML Document API so comments and key order survive
  visual and text edits; invalid YAML remains visible with line diagnostics.
- Backend `validate` endpoint is called (debounced) and its path-addressed
  errors are mapped onto nodes/fields.
- Saving sends the spec and layout separately; only spec changes affect the
  version hash. A `DRAFT_LOCKED` response keeps the edited YAML and links to
  the active test execution.

Test execution creation uses the normal execution BFF route and its
`Idempotency-Key`; the Run dialog for published versions retains its existing
replay behavior.

## Pages (M8-A and workflow batch 2a)

Implemented: tenant selection, Dashboard, Jobs, Projects, Clusters, Secrets,
Usage, Workflows, and Executions. Workflow pages provide version graphs,
execution/task details, parameterized Run, CSRF-protected Cancel, and the
draft editor described above. Templates,
Interactive, and Policies remain coming-soon entries. Tenant pages live under
`/t/{tenant}/...`, mirroring the API. The browser UI never calls the API
directly; Server Components use the server API client and mutations use the BFF.
Workflow sbatch import at `/t/{tenant}/workflows/import` returns a read-only
draft proposal; creating the workflow is a separate explicit action. Version
pages download YAML and selected executable tasks offer standalone sbatch
downloads through the BFF, which forwards `Accept` and `Content-Disposition`.
Platform admins can edit per-cluster container-runtime and software-module
settings from the cluster detail Settings tab using optimistic version checks.
They also manage node hooks (ADR-032) at
`/t/{tenant}/clusters/{cluster}/node-hooks`, linked from the cluster header only
for platform admins; other users get the usual 403/404 states. The page edits the
isolation mode and tenant-exclusive mechanism, shared and per-tenant NFS mounts,
and custom Prolog/Epilog hooks (lazy-loaded Monaco, shell), and shows API
warnings, the revision, content hash and update time. Saves send the loaded
`version`; a 409 shows a banner whose Reload asks before discarding edits, and
422 `details[].field` paths such as `tenant_mounts[2].target` are mapped onto the
row and field. The bundle downloads through the BFF as a binary
(`Content-Disposition` is passed through), node tokens are shown once in a dialog
and never kept in state after it closes, and a node-status table shows which
nodes hold the current bundle.
Draft workflow versions open a split React Flow canvas and Properties/YAML editor at
`/t/{tenant}/workflows/{workflow}/versions/{version}/edit`. YAML remains the
source of truth; graph edits preserve document comments, validation is debounced,
and draft save, publish, deprecate, and new-draft actions follow the version
lifecycle with optimistic checks.
The Job API currently exposes job reads, cancellation, and ExecutionSpec only;
there are no Events or Logs endpoints, so the detail view shows a Details tab.

### Future: interactive jobs

The job detail header reserves space for a future Connect action and the side
panel can host a Terminal tab; neither is implemented. Interactive sessions
must be proxied through Custos, not connected directly from the browser to a
node. A separate ADR must specify the WebSocket/stream path through the BFF,
authorization, and audit requirements before implementation.

## Security headers (BFF and API)

`Content-Security-Policy` (nonce-based scripts, `worker-src 'self'`,
`frame-ancestors 'none'`, `connect-src 'self'`; `unsafe-eval` is added only for
Next development),
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
