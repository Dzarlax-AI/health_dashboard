# Optional Cloudflare Access browser authentication

Health is self-hostable without an identity provider. Cloudflare Access is an
opt-in browser authentication mode, not a requirement for using the server,
iOS client, ingestion API, or MCP. Tokens remain a supported independent path.

## Authentication modes

| Path | Default `existing` mode | Opt-in `cloudflare` mode |
| --- | --- | --- |
| Web pages and browser JSON API | Local password/session; optional trusted ForwardAuth | Verified Access application JWT mapped to an existing Health user |
| Native API and ingestion | Per-user `X-API-Key` | The same per-user `X-API-Key`, without a browser challenge or IdP dependency |
| MCP | API key; optional separate OAuth | Unchanged API key and optional separate OAuth configuration |
| Health local browser session | Opaque server-side session | Not created or accepted as browser authentication |

Leave Cloudflare settings empty and use the default mode for an installation
without an IdP. The existing local username/password login and session support
are preserved. An API key is sent in a header; this change does not add a browser
form for pasting an API key. MCP also supports its existing opaque Bearer form;
its JWT-versus-key and mixed-credential rules remain unchanged.

## Configure an existing multi-user installation

Create and verify the local accounts through the normal Health setup before
enabling Access. Do not enable this mode on legacy single-user fallback. Use
an explicit map for every permitted account; do not infer a username from an
email address or map all users to the administrator.

```dotenv
BROWSER_AUTH_MODE=cloudflare
CLOUDFLARE_ACCESS_ISSUER=https://your-team.cloudflareaccess.com
CLOUDFLARE_ACCESS_AUD=your-health-application-audience
CLOUDFLARE_ACCESS_USER_MAP={"alice@example.com":"alice","bob@example.com":"bob"}
CLOUDFLARE_ACCESS_ORIGIN=https://health.example.com
```

These are deployment-specific values. Keep real email mappings and runtime
credentials outside the source repository. The complete configuration is
required when the mode is enabled; an unknown mode or incomplete configuration
fails startup. No Cloudflare settings are needed in the default mode.

Health verifies the signature, issuer, audience, validity period, application
token type, and configured identity mapping. The local registry determines the
user's active tenant and `is_admin`; client headers, query selectors, or token
profile fields do not grant a different tenant or administrator role. A token
for another Access application must fail even when its email is allowed.

For browser requests the preferred token is `Cf-Access-Jwt-Assertion`.
`CF_Authorization` is also an explicit JWT credential for same-origin API calls
whose edge path keeps native authentication. Both receive the same signature
and claim validation. Duplicate or conflicting credentials are rejected; a bad
header does not fall back to a different cookie. The old Health `auth` cookie
never restores a Cloudflare-mode browser login after its Access token expires.

An explicit `X-API-Key` request uses the native credential path. A valid key must
not need a JWKS fetch; an invalid key must not fall through to a different
browser identity. This preserves native access during IdP outages. Do not send
credentials in URL query parameters.

## Browser mutations and sessions

Cloudflare mode does not bootstrap a second long local session. Identity is
resolved again for each request, including when switching from one permitted
account to another. Preserve each user's existing tenant and role.

Unsafe browser requests authenticated with an Access JWT require an exact
same-origin `Origin` matching `CLOUDFLARE_ACCESS_ORIGIN`. Missing, cross-site, or
ambiguous origins must fail. Do not assume Cloudflare's cookie attributes
provide Health's CSRF protection. Valid native API-key requests remain exempt
from the browser Origin requirement; the key must first authenticate.

`/auth/session` is a bounded return/recovery route in this mode, not a local
session issuer. Browser API authentication failures remain JSON 401 rather than
an HTML IdP redirect. Top-level navigation can then recover Access login. Logout
uses the Access logout path; it does not promise immediate revocation of every
issued JWT or revoke MCP refresh tokens. Standalone login/logout is unchanged.

## Edge and origin routing

Configure one self-hosted Access application for the Health web hostname and an
explicit allowlist matching the local map. Keep the existing native boundaries
out of the browser challenge with narrowly scoped, more-specific exceptions:

- `/api*` — native API key or verified browser JWT at the backend;
- `/health*` — ingestion/checkpoint credentials and health checks;
- `/mcp*` — MCP API-key/OAuth authentication;
- `/.well-known/oauth-protected-resource/mcp` — public discovery metadata.

These exceptions do not make personal data public. The backend must continue
to authenticate each protected API/MCP/ingestion request itself. Keep the
Telegram webhook's existing secret-path and header validation.

The static frontend uses Traefik ForwardAuth against
`http://health-receiver:8080/_internal/cloudflare-access/verify`. That endpoint
accepts only an Access JWT for an active, mapped user, returns no medical data,
and never creates a local session. It is disabled in the default mode. API keys
cannot authorize this browser gate. Keep identity-sanitizing middleware before
the verifier; no client-supplied identity header is sufficient authentication.

The backend validates browser API JWTs itself. Do not put an unconditional
browser ForwardAuth gate in front of shared native API paths. Application-owned
router labels preserve enforcement when a temporary helper or verifier is
unavailable; failed verification never enables an Authentik/local-cookie fallback.

Cloudflare [recommends the assertion header and does not guarantee cookie
forwarding](https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/authorization-cookie/validating-json/).
**A real edge/browser test of same-origin API cookie delivery is a cutover gate.**
Test the supported browsers, fresh login, expiry/recovery, two accounts and
account switching. Synthetic JWT tests do not establish this edge behavior. If
the token is absent, fail closed and stop/roll back the cutover rather than
silently issuing a long Health session.

## Migration from Authentik

### Bulk-import request limit

Cloudflare's Free plan has a
[100 MB maximum upload size per request](https://developers.cloudflare.com/support/troubleshooting/http-status-codes/4xx-client-error/error-413/).
The Health bulk-import handler itself accepts up to 2 GiB. Proxying this
hostname therefore reduces the effective browser/API bulk-upload limit;
an Access bypass policy does not bypass Cloudflare's network limit. Native
metric and workout ingestion already have smaller 16 MiB and 32 MiB limits.

Agree on the large-import path before production cutover: use authenticated
private access such as an SSH tunnel to the existing API with its normal token
and tenant checks, or implement application-level multipart/chunked upload in
a separate change. Do not expose an unprotected public origin or assume that
HTTP transfer encoding removes the request-size limit. An authentication
cutover must not silently promise the previous 2 GiB web-upload behavior.

The maintainer is migrating the personal infrastructure from Authentik browser
ForwardAuth to Cloudflare Access. Treat implementation support, a published
image, and a verified production cutover as separate states. This guide does
not claim that the Health production cutover has already occurred.

1. Record the current compatible image pair, runtime configuration, route rules,
   local account mappings and roles. Back up and verify the registry/tenant
   database using the existing release runbook. Do not export medical payloads
   or credentials into test fixtures.
2. Keep the same public hostname and the two existing local user accounts.
   Configure an exact identity map and matching Access allow policy. Do not
   rename users, merge tenants, or derive admin access from an IdP group.
3. Configure the opt-in browser mode and resolve the compatible image pair with
   `--browser-auth cloudflare`; see the [production routing runbook](../deploy/production/health/README.md).
   Preserve native routes and all existing secret values.
4. Deploy the backend through the mandatory tenant schema release gate. Do not
   hotpatch a new authentication binary or bypass audits/isolation probes.
5. Verify the origin rejects forged identities, wrong/expired JWTs and old
   Health cookies. Verify valid native keys with IdP disabled/unreachable.
6. Switch the existing DNS proxy only after origin verification. Proxying the
   hostname places the whole Health HTTP flow, including medical API responses
   and sync uploads, through Cloudflare TLS termination. An Access exception
   does not remove traffic from Cloudflare.
7. Verify the browser token-delivery gate, both users' own data/roles, account
   switching, CSRF denial, native read access, MCP metadata and existing OAuth.
   Do not trigger ingestion, imports, backfills, report delivery, or AI
   generation just to test authentication. Record actual deployment acceptance.

The separate `MCP_OAUTH_*` Authentik issuer/subject configuration remains in
place unless explicitly migrated in a later change. Its API-key alternative
remains available even if OAuth is disabled. Do not retire Authentik while
other clients still depend on its issuer or refresh tokens.

For a separately approved rollback, restore the captured mode/configuration,
compatible images and DNS state through the same release gate, then verify the
previous Authentik/local login and native paths. Do not delete users, sessions,
keys, medical data, or shared services as an authentication rollback.

## Local verification

Run focused authentication and routing checks from the repository root:

```bash
go test -race ./internal/accessauth ./internal/ui ./cmd/server ./internal/mcpserver ./internal/oauth
go vet ./internal/accessauth ./internal/ui ./cmd/server ./internal/mcpserver ./internal/oauth
make contract-check test-production-routing-config test-production-traefik-rules
```

Signed JWT fixtures cover invalid signatures/claims, expiration, key rotation
and shared retry backoff during JWKS outages. Handler tests cover native-key
precedence, ambiguous credentials, CSRF and the absence of local-session
fallback. No real identity-provider account or medical record is needed.

`TestCloudflareGuardUsesActiveRegistryTenantRoleAndAPIKey` additionally uses
the real registry and tenant manager with two synthetic users. It verifies
account switching, member admin denial, inactive-user denial and unchanged
registry/tenant session rows. It requires `HEALTH_DB_TESTS=1`,
`READINESS_TEST_DSN` and `REGISTRY_TEST_DSN` pointing to an explicitly disposable
PostgreSQL database with the repository's `health_test_metadata` marker. Never
point these integration tests at a real installation. The existing
`make test-db-security` lane separately exercises fixed-role database isolation;
prepare its `ADMIN_TEST_DSN` and `REGISTRY_TEST_DSN` as shown in the CI workflow.

These are local code/configuration checks. They do not replace the real
Cloudflare/browser token-delivery and two-account production acceptance gates.
