# Web frontend operations

The browser application lives in `web/` and is a Next.js 16 App Router BFF. It
uses Auth.js v5 with the JWT session strategy and no adapter. The frontend has
no database connection or Redis dependency; it communicates only with Custos's
API and the configured OIDC provider.

## Environment

| Variable | Required | Purpose |
| --- | --- | --- |
| `CUSTOS_WEB_ISSUER` | yes | OIDC issuer URL. Plain HTTP is accepted only with `CUSTOS_WEB_DEV=1`. |
| `CUSTOS_WEB_CLIENT_ID` | yes | Confidential OIDC client ID. |
| `CUSTOS_WEB_CLIENT_SECRET_FILE` | yes | Path to the client-secret file; its value is never logged. |
| `CUSTOS_WEB_PUBLIC_ORIGIN` | yes | Exact browser origin used for callbacks, logout, and Origin checks. This also sets Auth.js `AUTH_URL`. |
| `CUSTOS_API_URL` | yes | Custos API origin. Plain HTTP is accepted only in dev mode. |
| `CUSTOS_WEB_TRUSTED_PROXY_HOPS` | no | Number of trusted reverse-proxy hops appending `X-Forwarded-For`; defaults to `0`, which omits the forwarded client address. Set to `1` only behind one trusted proxy (the e2e web service uses nginx). |
| `CUSTOS_WEB_AUTH_SECRETS` | yes | Comma-separated Auth.js secret array; every entry must contain at least 32 bytes. The first encrypts new JWTs and all entries can decrypt. |
| `CUSTOS_API_CA_FILE` | no | CA bundle used for API TLS verification. |
| `CUSTOS_WEB_CA_FILE` | no | CA bundle used by Auth.js and `openid-client` for OIDC TLS verification. |
| `CUSTOS_WEB_DEV` | no | Set to `1` to permit local plaintext issuer/API/origin URLs. |
| `CUSTOS_WEB_INSECURE_COOKIES` | no | Set to `1` only outside production to omit Secure and the `__Host-` prefix for local HTTP. |

Configuration is validated with Zod during Node.js startup. Missing or invalid
variables are reported by name. Production sources `CUSTOS_WEB_AUTH_SECRETS`
from OpenBao; do not put secret values in source control or image build
arguments. To rotate, prepend a new secret and retain the old values for
unsealing until all sessions using them have expired.

## Session and security behavior

Auth.js encrypts the JWT session cookie and chunks it when needed. The JWT
contains the subject, access token, refresh token, access-token expiry in
milliseconds, and an optional refresh error. It does not contain the ID token.
The browser-readable `/api/auth/session` response exposes only the subject,
name, email, session expiry, and optional error; no token is returned to browser
JavaScript. The Auth.js session lifetime is 8 hours. The separate
`custos_last_tenant` cookie contains only a validated slug and is not HttpOnly.

The `__Host-custos_csrf` cookie is intentionally readable by browser JavaScript.
Unsafe BFF and logout requests must echo it in `X-CSRF-Token`; its HMAC is bound
to the current subject and keyed with HKDF from the first Auth.js secret. Origin
must match `CUSTOS_WEB_PUBLIC_ORIGIN`, and `Sec-Fetch-Site`, when present, must
be `same-origin`. In local HTTP development,
`CUSTOS_WEB_INSECURE_COOKIES=1` changes cookie names to the unprefixed `custos_*`
forms; the flag is rejected when `NODE_ENV=production`.

Access tokens are refreshed server-side within 60 seconds of expiry. A
process-local single-flight keyed by the SHA-256 digest of the refresh token
coalesces concurrent requests in one instance. The protected-page proxy
refreshes before Server Components render and forwards refreshed Auth.js cookie
chunks into the same request; BFF Route Handlers refresh and persist cookies on
their responses. Refresh failure removes tokens and fails closed. Logout
best-effort revokes the refresh token and redirects through the IdP end-session
endpoint using `client_id` and `post_logout_redirect_uri`.

## Local development against e2e Keycloak

1. Start the e2e stack with `scripts/e2e.sh up` so the Keycloak realm and CA
   exist; do not reset its volumes.
2. Copy `web/.env.example` to `web/.env.local` and keep the placeholder
   `CUSTOS_WEB_AUTH_SECRETS` local. The example uses the Keycloak e2e issuer,
   `custos-e2e`, and the generated client-secret and CA files under
   `deploy/e2e/.secrets/`.
3. The imported `custos-e2e` client allows
   `http://localhost:3000/api/auth/callback/custos` and the `http://localhost:3000`
   web origin for local development. Auth.js adds PKCE S256, state, and nonce.
4. From `web/`, run `pnpm dev` and browse to `http://localhost:3000`. If the
   e2e TLS frontend already occupies port 3000, set
   `CUSTOS_WEB_PUBLIC_ORIGIN=http://localhost:3001` and run
   `pnpm dev --port 3001`; that callback/origin is also allowed in the e2e realm.

## Commands

```sh
cd web
pnpm install --frozen-lockfile
pnpm dev
pnpm dev:mock
pnpm gen:api
pnpm lint
pnpm typecheck
pnpm test
pnpm build
pnpm e2e:smoke
```

The fake OIDC and fake API for `e2e:smoke` are shared node scripts under
`web/mock/`; `pnpm dev:mock` starts the same backend-free development mode. The live browser test is `web/e2e/live.spec.ts`; it uses the
existing e2e Keycloak and API stack:

```sh
cd web
CUSTOS_E2E=1 pnpm e2e:live
```

See `docs/frontend.md`, ADR-021, and `docs/e2e.md` for the design and live-stack
details.
