# Production frontend canary

This directory is the reviewed source for the Health Dashboard production
Compose definition. At deployment time it is mirrored to
`personal_ai_stack/deploy/health` and then to `/root/health` on the VPS. The
untracked runtime `.env` remains in those deployment locations; never copy it
back into Git.

The stack keeps exactly one Go backend. It adds a static frontend and splits
Traefik ownership into four boundaries:

- `/health*` and `/mcp*` stay on the backend without Authentik redirects.
- `/api/*` stays on the backend. Requests carrying `X-API-Key` preserve the
  machine/mobile boundary; browser requests pass through Authentik so
  ForwardAuth can establish the opaque tenant session.
- all legacy pages remain on the backend, with `/legacy` rewritten to `/`.
- only `/` and `/assets/*` may move to the frontend.

## Prepare a release

Authenticate Docker to GHCR, then resolve the authoritative pair pointer:

```bash
./scripts/resolve-production-pair.sh \
  --mode canary \
  --host health.example.com \
  --output deploy/production/health/release.env

docker compose \
  --env-file deploy/production/health/release.env \
  -f deploy/production/health/docker-compose.yml \
  config
```

`release.env` contains no secrets, is ignored by Git, and records the exact pair
digest, component digests, revisions, contract version, and route mode.

## Route modes

- `canary`: frontend routes require the host-only cookie
  `health_frontend_canary=1`. Set it manually for the configured `HEALTH_HOST`;
  it is
  not an authentication credential.
- `cutover`: `/` and `/assets/*` default to the frontend.
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
4. `/api/session` returns the expected tenant for at least two active tenants.
5. `/health*` and `/mcp*` retain API-key/bearer behavior.
6. `/metrics`, `/settings`, `/admin`, `/static/*`, and `/legacy` reach Go.
7. A rollback-mode rehearsal restores the Go root without a DB change.

Production restart or route changes require a separate explicit approval.
