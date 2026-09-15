# Authentication

Custos is an OIDC **relying party / resource server**. It never issues
identities and never sees passwords.

## Two client classes, two credential channels

| Client | Credential | Why |
| --- | --- | --- |
| CLI, automation, service-to-service | `Authorization: Bearer <JWT>` access token issued by the IdP | Stateless; standard for APIs |
| Browser (Next.js UI) | HttpOnly, Secure, SameSite=Lax session cookie issued by the Next.js server (BFF) | Tokens never reach browser JS; no localStorage |

The Go API accepts **only** bearer JWTs. The BFF holds the user's tokens
server-side and attaches the access token when it calls the API. See
`docs/frontend.md` for the BFF design and tradeoffs.

## Configuration

```yaml
auth:
  oidc:
    issuer: https://idp.example.org/realms/hpc
    discovery: true                # use /.well-known/openid-configuration
    jwks_uri: ""                   # optional override
    client_id: custos-api          # used by BFF; API validates audience below
    audiences: ["custos-api"]      # at least one required; token aud must intersect
    allowed_algorithms: [RS256, ES256]   # allow-list; never none/HS*
    required_scopes: ["openid"]
    clock_skew: 30s
    jwks_cache_ttl: 1h
    jwks_refresh_min_interval: 30s # rate-limit for unknown-kid refresh
    claims:
      subject: sub
      email: email
      name: name
      groups: groups               # optional; used for tenant/group mapping
    accepted_token_types: ["at+jwt", "JWT", ""]   # RFC 9068 typ if present
```

Startup validation **fails closed**: missing issuer, empty audiences,
`alg=none`, any `HS*` algorithm, non-HTTPS issuer (outside explicit dev mode),
or unreachable discovery (when `discovery: true`) all abort startup.

## Token verification (per request)

1. Parse header only; reject if `alg` not in allow-list, or `alg` is `none`.
2. Resolve key by `kid` from the JWKS cache. Unknown `kid` → trigger a
   refresh, subject to `jwks_refresh_min_interval` (a shared singleflight,
   so a flood of bad tokens causes at most one upstream request per interval).
   Still unknown → reject.
3. Verify signature with the resolved key's algorithm, **not** the token's
   header (prevents key-confusion; the `alg` in the header must match the JWK).
4. Verify `iss` equals configured issuer exactly (string compare, after
   discovery-time normalization).
5. Verify `aud` intersects configured audiences (`aud` may be string or array).
6. Verify `exp`, `nbf`, `iat` with configured skew. Reject tokens with
   lifetime > 24h (defensive).
7. Verify `typ` if present is in `accepted_token_types`.
8. Verify required scopes (`scope` claim, space-delimited, or `scp` array).
9. Build `Principal{Issuer, Subject, UserID, Kind, Email, Name, Scopes, Groups, PlatformRoles}`.
   `UserID` and `PlatformRoles` are filled from the database after JIT
   provisioning (step below); platform roles are loaded here (not in
   `authz`) because they are per-principal, not per-tenant, and are needed
   before any tenant context exists.

Library choice: `github.com/coreos/go-oidc/v3` for discovery/JWKS/verification
(mature, small, widely audited) with our own wrapper enforcing the allow-list
and the rate-limited refresh. Alternative considered: `lestrrat-go/jwx` (more
flexible, larger surface). The standard library alone would require
re-implementing JWS/JWK parsing — rejected as risk without benefit.
(ADR-004.)

## JWKS cache and rotation

- In-memory cache keyed by issuer, storing the full key set + fetch time.
- Background refresh at `jwks_cache_ttl`; on-demand refresh on unknown `kid`
  with the min-interval limiter.
- Old keys remain valid until they disappear from the published JWKS;
  tokens signed with a retired key fail naturally.
- Fetch errors keep the previous key set (stale-while-error) up to a hard
  cap of 24h, after which verification fails and readiness reports degraded.

## User provisioning (JIT)

On first successful verification of a (`issuer`, `subject`) pair, a `User`
row is created (email/name cached from claims, refreshed when they change).
Provisioning creates a user with **no tenant memberships**. Membership is
granted by:

- a tenant admin via API, or
- tenant-configured claim mapping rules (e.g. IdP group `hpc-chem` →
  tenant `chem`, role `researcher`) evaluated at login and re-evaluated on
  each request from cached claims.

Claim-derived memberships are marked `source = idp` and are revoked when the
claim disappears; API-granted ones are `source = manual`.

## Machine clients

Automation uses client-credentials tokens from the same IdP. They are
provisioned as `User` rows with `kind = service` and receive memberships like
any user. Custos does not mint its own API keys in early milestones; if it
does later, they will be opaque, hashed at rest, tenant-scoped, and exchanged
for an internal `Principal` — never JWTs signed by Custos.

## What authentication does *not* do

A valid token proves *who*; it grants nothing. Every operation still goes
through `Authorizer.Check`. A valid token for a user with no memberships can
call `/api/v1/me` and nothing else.

## Audit

Emitted events: `auth.login` (BFF), `auth.token_rejected` (reason category
only, never the token), `user.provisioned`, `membership.granted`,
`membership.revoked`.
