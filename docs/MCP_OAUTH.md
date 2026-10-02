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
