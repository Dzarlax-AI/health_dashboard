# Remote MCP OAuth and sleep balance

Health is an OAuth resource server. It verifies access tokens from an external authorization server; it does not issue tokens, register clients, or share Personal Memory permissions. MCP tools only read the authenticated user's Health tenant. Existing `X-API-Key` clients remain supported.

## Configure the authorization server

These are operator actions requiring approval before changing live access or creating credentials.

1. Choose a separate Health OAuth application/provider and the public canonical HTTPS resource URL ending in `/mcp`. Do not reuse Personal Memory's audience or grant its permissions to Health users.
2. Configure authorization-code flow with PKCE `S256`, published OAuth/OIDC discovery metadata, and a read scope such as `health:read`. The authorization server must support the client-registration method used by the ChatGPT connection: CIMD, DCR, or a manually configured client. Health itself does not implement registration.
3. Bind the OAuth `resource` parameter to the Health token audience. Issue signed RS256 JWT access tokens with `kid`, exact `iss`, Health `aud`, `exp`, nonempty `sub`, and the required `scope` (or `scopes` array). Opaque access tokens and other signing algorithms are unsupported by this implementation.
4. Allow only the intended Health users. Obtain each immutable issuer subject and map it explicitly to that user's existing Health username. Email and username claims inside tokens never choose the tenant. The Health application's audience, scope, allowlist, and subject mapping are independent of Personal Memory.
5. Copy the exact redirect URI displayed by the ChatGPT MCP management page. The stable `https://chatgpt.com/connector_platform_oauth_redirect` requires the authorization server's RFC 9207 issuer identification support; otherwise ChatGPT uses a callback-ID URI. Do not allow arbitrary redirect URIs. Refresh tokens, if supported, remain the client's and authorization server's responsibility; Health does not store them.

The current requirements are documented by [OpenAI authentication](https://developers.openai.com/plugins/build/auth) and [MCP Authorization](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization). PKCE, consent, token lifetime, registration, and refresh behavior must be verified against the chosen external authorization server before claiming an end-to-end connection works.

## Configure Health

### Cloudflare Managed OAuth

Cloudflare Managed OAuth returns an opaque access token to its client and
forwards a signed `Cf-Access-Jwt-Assertion` to the origin. Its opaque token cannot
be used as the external RS256 JWT configured through `MCP_OAUTH_*` below. See
[Cloudflare's token format documentation](https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/managed-oauth/#token-format).

The optional profile reserves the exact `/mcp/oauth` path on the existing
hostname. It returns `404` when disabled; encoded, trailing-slash, case and
subtree variants must not fall through to native API-key authentication.
When enabled, a separate, always stateless MCP transport serves the same
read-only tools. Each request verifies RS256 signature, the pinned team issuer,
the configured MCP application audience, `type=app`, and required `iat`, `nbf`
and `exp` claims. Cloudflare can include multiple audiences: the MCP verifier
requires its exact configured audience to be present. The browser verifier
retains its stricter single-audience policy, and a browser-only or foreign
application token does not authorize MCP.

The verified email must map explicitly to an existing Health username; the
registry and restricted tenant pool are resolved again on every HTTP request.
Disabled or isolation-unready tenants are denied even if their pool was cached.
Unsigned identity headers, cookies, tool arguments and MCP session IDs cannot
replace that identity. Duplicate assertions or credentials, `X-API-Key`, query
credentials and malformed assertions are rejected. Optional Cloudflare opaque
`Bearer oauth:...` values are transport credentials only and are never looked
up as local API keys. No local browser session or refresh token is created.
This server-to-server endpoint accepts requests without `Origin`; any supplied
`Origin` header is rejected with `403`. Canonical non-POST requests receive
`405` with `Allow: POST`, while invalid path variants remain `404`.

```dotenv
MCP_ACCESS_ENABLED=true
MCP_ACCESS_ISSUER=https://team.example.test
MCP_ACCESS_AUD=separate-mcp-application-audience
MCP_ACCESS_USER_MAP={"alice@example.com":"alice"}
```

Leave the profile disabled and its other values empty for standalone operation.
Its configuration is independent of `BROWSER_AUTH_MODE` and `MCP_OAUTH_*`.
Existing `/mcp` API keys and optional external OAuth remain independent.
Do not disable the old authorization server until real ChatGPT tool discovery,
a scoped read-only call and a repeated call after access-token expiry have
passed. A successful connection does not prove the maximum grant lifetime.

Use a separate Managed OAuth Access app for the new path and an exact callback
obtained from the client's actual setup UI. Do not allow broad callback domains
or automatically reuse a browser app audience, user allowlist or local role.
For an unauthenticated `/mcp/oauth` request, follow the `resource_metadata` URL
from Cloudflare's `WWW-Authenticate` header rather than hard-coding a discovery
path. That advertised document must be reachable by an ordinary non-browser
OAuth client. Register the actual client's exact redirect URI, use PKCE S256, and keep the Access policy limited to the
intended MCP users. The old `/.well-known/oauth-protected-resource/mcp` remains independent.

### Maintainer deployment

On 17 October 2026, the independent Cloudflare MCP transport was enabled at
`/mcp/oauth` on the maintainer's existing Health hostname. Its Google
connection has a separate owner-only Access policy; the browser user allowlist
does not automatically grant MCP access. Native API keys continue at `/mcp`,
and external JWT OAuth remains an optional self-hosting configuration.

A cutover from an older issuer must preserve its five `MCP_OAUTH_*` runtime
fields until the new client's tool discovery, scoped read-only call and a
repeated call after access-token expiry pass. Remove only that Health runtime
profile; do not delete a shared Authentik provider or other services' clients.
The configured five-minute access-token lifetime and 720-hour grant session
are provider settings, not a guarantee of the maximum grant lifetime.

### External JWT OAuth configuration

All five environment values are required together. Leaving all unset disables OAuth; partial configuration fails startup. The following example contains only synthetic configuration:

```dotenv
MCP_OAUTH_ISSUER=https://auth.example.test/health/
MCP_OAUTH_JWKS_URL=https://auth.example.test/health/jwks
MCP_OAUTH_RESOURCE=https://health.example.test/mcp
MCP_OAUTH_READ_SCOPE=health:read
MCP_OAUTH_SUBJECT_MAP={"synthetic-subject-alice":"alice","synthetic-subject-bob":"bob"}
```

Issuer, JWKS, and resource URLs require HTTPS with no credentials, query, or fragment. The resource path must be `/mcp`. Subject-map usernames must already exist in Health; in legacy single-user mode the supported username is `admin`. Isolated tenants still pass the existing registry and database-isolation gates.

Deploy only after approval, using the repository's release and tenant-schema gates. Publish `/.well-known/oauth-protected-resource/mcp` through the same public origin as `/mcp`, including through any reverse proxy. Its metadata identifies the resource, issuer, and required scope. Unauthenticated MCP requests return a `WWW-Authenticate` challenge linking to this document.

## Connect ChatGPT

For Cloudflare Managed OAuth, connect to the exact `/mcp/oauth` URL and let the
client discover Cloudflare's OAuth metadata. Copy that connection's actual
redirect URI into the separate Access app, sign in with an allowed Google
account, and verify tool discovery plus a scoped read-only call. Repeat after
the configured access-token lifetime without a new sign-in before accepting
the migration. The API-key `/mcp` endpoint is unchanged.

For the optional external JWT OAuth profile on `/mcp`:

1. In ChatGPT's available MCP/developer settings, create a connection to the canonical public `/mcp` URL and select OAuth. Availability and exact labels depend on the account and workspace.
2. Complete the chosen client-registration configuration and copy the connection's actual redirect URI into the authorization server allowlist, as described above. Do not create a production client without approval.
3. Sign in to the Health-specific authorization application and consent to read access. Verify that its `sub` maps to your Health username and its audience is the Health resource.
4. Refresh the tool list. Call `get_sleep_balance` with a tenant-local wake date, or omit `date` for today. Check that the response belongs to the signed-in Health account.
5. Before accepting the rollout, verify a second synthetic user's isolation, an expired token, a token for Personal Memory's audience, and a legacy API-key request. No successful local unit test proves the live OAuth handshake or public proxy route.

A dot client must support the same remote MCP/OAuth resource-server flow. Its product-specific setup has not been verified here.

## API-key compatibility and token limits

Use one credential header per request: either `X-API-Key` or `Authorization: Bearer`. Mixed or repeated credential headers are rejected. A Bearer value with exactly two dots is treated as a JWT when OAuth is enabled and never falls back to API-key authentication. Send an opaque API key containing two dots with `X-API-Key`. API-key-only installations retain legacy Bearer behavior. Tokens in URL query parameters are rejected.

OAuth-enabled transport is stateless: each request is authenticated and its tenant context is resolved independently, including when a client reuses a session ID. The verifier fetches bounded JWKS responses without redirects and caches keys for up to ten minutes; unknown-key refreshes are throttled. JWT validation does not perform token introspection or immediate server-side token revocation. Use appropriately short token lifetimes, and account for the JWKS cache during signing-key rotation/revocation. Removing a subject mapping and restarting Health prevents that subject's subsequent access; changing live mappings requires approval.

## `get_sleep_balance`

Input: optional `date`, strictly `YYYY-MM-DD`. Other arguments, including user, schema, and tenant selectors, are rejected. The default date comes from the authenticated tenant's timezone.

The typed output is `{wake_date, balance}`. A missing snapshot returns `balance: null`. An incomplete snapshot returns an object with `balance_hours: null`, its existing state, confidence, incomplete reason, and available periods. Unknown values are never replaced with zero or averaged into gaps.

The tool reads the existing saved `SleepDurationBalance` snapshot and reuses the existing API DTO. It does not reconcile, recalculate, write, or duplicate accounting formulas. Periods preserve their effective manual goal, recorded duration, delta, coverage and quality state, and tenant-local noon-to-noon bounds, including DST changes. `calculated_through` is the existing accounting boundary; the snapshot read contract does not expose a separate `calculated_at` timestamp.

This is sleep duration relative to a manual goal, not a diagnosis, physiological sleep-debt estimate, or instruction to sleep back a stated number of hours. Readiness and EnergyBank are model/heuristic observations, not established clinical measurements. `get_health_briefing` already includes the existing balance DTO, so its shape is unchanged.

## Verification and operator participation

Synthetic tests cover JWT validity and expiry, issuer/audience/scope/subject failures, unsafe credential combinations, key compatibility, per-request tenant context, and sleep output completeness/nulls/goals/local periods. No production credentials, health data, or external notifications are used.

The remaining operator steps are: approve the Health authorization application/client and grants, supply verified issuer/resource/JWKS/subject bindings, approve deployment, complete browser consent, and verify the public end-to-end connection. The code alone cannot establish those external outcomes.
