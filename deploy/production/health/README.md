# Production frontend canary

This directory is the reviewed source for the Health Dashboard production
Compose definition. At deployment time it is mirrored to
`personal_ai_stack/deploy/health` and then to `/root/health` on the VPS. The
untracked runtime `.env` remains in those deployment locations; never copy it
back into Git.

The stack keeps exactly one Go backend. It adds a static frontend and splits
Traefik ownership into four boundaries:

- `/health*`, `/mcp*`, and the exact `/.well-known/oauth-protected-resource/mcp` discovery path stay on the backend without Authentik redirects. Health owns MCP authentication and OAuth metadata.
- `/api/*` stays on the backend without a browser ForwardAuth redirect. The
  backend validates native `X-API-Key` credentials or the selected browser
  mode's credential (local session in existing mode, Access JWT in Cloudflare mode).
- all legacy pages remain on the backend, with `/legacy` rewritten to `/`.
- only `/`, `/sleep`, `/activity`, `/cardio`, `/recovery`, `/energy`, and `/assets/*` may move to the frontend.

## Prepare a release

Authenticate Docker to GHCR, then resolve the authoritative pair pointer:

```bash
./scripts/resolve-production-pair.sh \
  --mode canary \
  --browser-auth existing \
  --host health.example.com \
  --output deploy/production/health/release.env

docker compose \
  --env-file deploy/production/health/release.env \
  -f deploy/production/health/docker-compose.yml \
  config
```

`release.env` contains no secrets, is ignored by Git, and records the exact pair
digest, component digests, revisions, contract version, and route mode.
The tenant schema release gate deliberately overrides the backend selection
through `HEALTH_IMAGE`; the Compose definition gives that audited value
precedence over `HEALTH_BACKEND_IMAGE`.

## Browser authentication profile

Browser auth and frontend route selection are separate settings. The resolver
accepts `--browser-auth existing|cloudflare`:

- `existing` preserves the Authentik middleware used by this production stack;
  standalone installations can instead use the default local login/API keys
  through their own deployment without any IdP.
- `cloudflare` selects the backend Access verifier for document/assets routes
  and sets the backend's `BROWSER_AUTH_MODE=cloudflare`. Native API, ingestion,
  MCP and webhook rules retain their own authentication boundaries.

The release records `HEALTH_BROWSER_AUTH_MODE` and its derived middleware.
Updating the **same** release output without this flag preserves its saved
profile. A new output defaults to `existing`; pass the flag explicitly when
creating a Cloudflare deployment artifact. Invalid or duplicate saved profiles
fail rather than silently changing auth. Runtime `BROWSER_AUTH_MODE` in the
production container is selected by this release profile; the generic root
`.env.example` setting remains useful for standalone deployments.

For an approved Cloudflare migration, configure all four `CLOUDFLARE_ACCESS_*`
settings in the root-only runtime env, then resolve the pair:

```bash
./scripts/resolve-production-pair.sh \
  --mode cutover \
  --browser-auth cloudflare \
  --host health.example.com \
  --output deploy/production/health/release.env
```

Never source the release file as shell code. It contains public release
metadata, and the resolver parses only the saved enum when preserving a mode.
Do not set the Cloudflare profile on an old image without its verifier. Check
both the resolved container mode and browser middleware before deployment.

The middleware lives on the Health containers, calls
`/_internal/cloudflare-access/verify`, and forwards only the assertion header
and cookie. It does not synthesize a username or bootstrap a Health session.
The backend validates browser API JWTs and still accepts native keys without
an IdP. See [the authentication migration guide](../../../docs/CLOUDFLARE_ACCESS.md).

## Route modes

- `canary`: frontend routes require the host-only cookie
  `health_frontend_canary=1`. Set it manually for the configured `HEALTH_HOST`;
  it is
  not an authentication credential.
- `cutover`: `/`, `/sleep`, `/activity`, `/cardio`, `/recovery`, `/energy`, and `/assets/*` default to the frontend.
- `rollback`: Traefik discovery is disabled for the frontend, so the already
  running Go fallback owns `/` again. No database change is involved.

Generate a new `release.env` for every transition. Do not hand-edit router
rules:

```bash
./scripts/resolve-production-pair.sh --mode cutover --host health.example.com --output /secure/release.env
./scripts/resolve-production-pair.sh --mode rollback --host health.example.com --output /secure/release.env
```

## Required evidence before cutover

1. `make test-production-routing-config`,
   `make test-production-traefik-rules`, and the compatibility-pair harness pass.
2. Both containers report healthy and only one `health-receiver` exists.
3. Canary QA succeeds for RU, EN, and SR.
4. Both active users receive their own data/roles. `/api/session` reports capabilities only; it does not expose a tenant ID. Verify request-to-tenant isolation separately, without logging medical records.
5. `/health*` and `/mcp*` retain API-key/bearer behavior.
6. `/metrics`, `/settings`, `/admin`, `/static/*`, and `/legacy` reach Go; `/sleep`, `/activity`, `/cardio`, `/recovery`, and `/energy` reach React.
7. A frontend rollback-mode rehearsal restores the Go root without a DB change. This route rollback retains the selected browser auth profile; auth rollback is separate.
8. For Cloudflare mode, real edge/browser cookie delivery, expiry/recovery, account switching and CSRF denial pass. Keep native keys usable with IdP unavailable. Local tests alone cannot prove edge transport.

Production restart or route changes require a separate explicit approval.
