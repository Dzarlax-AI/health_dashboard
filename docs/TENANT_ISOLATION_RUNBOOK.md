# Tenant database isolation runbook
This rollout is additive. Do not enable isolated runtime pools until every
existing tenant has a verified rollback manifest and `apply` has completed.
The CLI never prints derived passwords or master secrets.

## Fresh installation

1. Bootstrap three PostgreSQL identities outside the application:
   - an administrator allowed to create roles, schemas, and grants;
   - a registry login limited to `health_registry`;
   - no shared tenant login. Tenant logins are created by the provisioner.
2. Configure `ADMIN_DATABASE_URL`, `REGISTRY_DATABASE_URL`, a credential-free
   `TENANT_DATABASE_URL_BASE`, `TENANT_DB_MASTER_SECRET`, and its positive
   version. Store the real values in deployment secret storage.
3. Set `TENANT_DB_ISOLATION_ENABLED=true`, start the service, and create the
   first user with `SETUP_TOKEN`. Provisioning creates the role/schema, runs
   every idempotent table migration twice, verifies the complete schema, then
   activates the user. A restart reconciles pending operations before workers
   or notification schedulers start.

## Existing installation

Keep the running server on `TENANT_DB_ISOLATION_ENABLED=false` while preparing
the database. Use an immutable currently deployed image digest for `--image`.

```bash
# 1. Read-only inventory and reviewed plan.
make tenant-isolation ARGS='--mode inventory --all --primary-schema health'
make tenant-isolation ARGS='--mode dry-run --all --primary-schema health --image ghcr.io/example/health@sha256:<64-hex> --manifest /secure/health-isolation.json'

# 2. Apply canary first. It validates/creates the manifest before mutation,
# transfers ownership transactionally, verifies own access and SQLSTATE 42501
# denial for registry/another tenant, then sets db_isolation_ready=true.
make tenant-isolation ARGS='--mode apply --schema health_canary --credential-version 1 --image ghcr.io/example/health@sha256:<64-hex> --manifest /secure/health-canary.json --confirm'
make tenant-isolation ARGS='--mode verify --schema health_canary'

# 3. Apply the remainder with the primary schema last, then enable isolated
# runtime configuration and deploy the new immutable image.
make tenant-isolation ARGS='--mode apply --all --primary-schema health --credential-version 1 --image ghcr.io/example/health@sha256:<64-hex> --manifest /secure/health-isolation.json --confirm'
```

The new application may be deployed with isolation disabled before the DB
cutover. Enabling the flag before `db_isolation_ready=true` fails closed rather
than falling back to the shared tenant credential.

## Schema-contract release gate

After the initial isolation cutover, all tenant schema upgrades use the CLI
embedded in the same immutable image as the server. The authoritative modes
are fleet-only:

```bash
/app/tenant_isolation --mode audit --all --primary-schema health
/app/tenant_isolation --mode migrate-contract --all --primary-schema health --confirm
```

The audit compares the exact registry, permanent marker, and tenant-role sets;
checks the deterministic schema version/checksum, owners, grants, default
privileges, and role attributes; then runs registry-denial and all-pairs tenant
probes. Output is one pseudonymous JSON document and a failure exits nonzero.

For production, run `scripts/deploy-tenant-schema-gate.sh` as root with a
root-owned mode-0600 environment file. The wrapper pins one image digest,
audits before downtime, stops only the application service, migrates and audits
while stopped, starts the pinned image, checks stability, audits again, and
rechecks stability/logs. It never performs an automatic database rollback or
old-image restart; on failure follow only the printed immutable recovery
instructions after verifying backward compatibility.

## Rotation

Configure the new secret/version as current and the old pair as previous. Then:

```bash
make tenant-isolation ARGS='--mode rotate --all --primary-schema health --expected-old-version 1 --credential-version 2 --confirm'
```

Each role password is changed, authenticated, and denial-tested before the
registry version advances. The running service fails closed on metadata drift
and deliberately retains its existing pool and callbacks; it does not perform
a hot pool cutover. Remove the previous secret only after all tenants report
the new version and the service has restarted successfully.

## Rollback

Disable isolated runtime mode and restore each manifest before starting the
legacy image. The command restores prior owners/effective grants and marks the
tenant not isolation-ready in one transaction. It deliberately retains dormant
tenant roles so rollback is idempotent and cannot race open connections.

```bash
make tenant-isolation ARGS='--mode rollback --all --primary-schema health --manifest /secure/health-isolation.json --confirm'
```

Deploy the exact immutable image printed by the rollback command. Never revoke
the legacy shared login until the canary, restart, clean-install, rotation, and
rollback rehearsals have all passed on a disposable database.
