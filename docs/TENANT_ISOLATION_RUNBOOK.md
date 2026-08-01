# Tenant database isolation runbook
This rollout is additive. Do not enable isolated runtime pools until every
existing tenant has a verified rollback manifest and `apply` has completed.
The CLI never prints derived passwords or master secrets.

PostgreSQL 16 or newer is required. Before either a clean installation or an
existing-install migration, configure `TENANT_DB_BOOTSTRAP_DATABASE_URL`,
`HEALTH_ADMIN_DB_PASSWORD`, and `HEALTH_REGISTRY_DB_PASSWORD` from root-owned
secret storage. The bootstrap DSN is a one-time privileged connection and is
never used by the server or request paths.

```bash
/app/tenant_isolation --mode bootstrap-db-identities --manifest /secure/health-db-identities.json --confirm
```

The command first writes a version-2, SHA-256-protected manifest as a new mode
0600 file, then creates or reconciles the fixed, marked `health_admin` and
`health_registry` roles and transfers the `health_registry` catalog. Keep this
manifest in durable root-owned secret storage for the entire rollback window.
It is bound to the PostgreSQL system identifier plus database OID/name, and
bootstrap, finalize, and rollback reject a different target. Existing files
are never overwritten. The manifest records owners, normalized effective ACLs,
default ACLs, memberships, role attributes, and security-definer configuration;
it deliberately excludes passwords and connection strings. An existing fixed
role without the bootstrap marker is ambiguous and blocks the operation.

Rollback fidelity compares normalized effective ACL entries by object,
grantee, privilege, and grant option after PostgreSQL default-ACL expansion.
Grantor is retained as audit evidence, but may safely differ after replay and
does not by itself make rollback fail. Replay uses supported `ALTER`, `GRANT`,
and `REVOKE` statements only; it never writes PostgreSQL system catalogs.
Role-membership grantors are different: PostgreSQL uses them operationally for
`GRANTED BY` revocation, so rollback must reproduce the exact membership
grantor and options or fail closed before replay.

Verify the fixed identities through their normal restricted connections. While
the documented legacy bridge is intentionally present, declare that state
explicitly; the authoritative post-finalize check omits the flag and rejects
the bridge:

```bash
/app/tenant_isolation --mode verify-db-identities --allow-legacy-bridge
# after finalize:
/app/tenant_isolation --mode verify-db-identities
```

Verification is read-only and uses only `ADMIN_DATABASE_URL` and
`REGISTRY_DATABASE_URL`. It checks PostgreSQL version, exact role attributes,
database grant options, memberships, registry object/type/routine/default-ACL
ownership and grants, and registry-to-every-registered-tenant SQLSTATE 42501
read/write/DDL denials. Output contains stable finding codes, not identifiers.

## Fresh installation

1. Run `bootstrap-db-identities`. It creates the fixed administrator and
   registry identities; it does not create `health_user`. Tenant logins are
   created by the provisioner.
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
Bootstrap temporarily grants the old `health_user` only the registry access
needed by the live legacy process and grants `health_admin` a narrow SET bridge
to transfer `health_user`-owned tenant objects.

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

# 4. After the legacy process is stopped and tenant migration is complete,
# remove the health_user bridge and registry grants before authoritative audit.
/app/tenant_isolation --mode finalize-db-identities --manifest /secure/health-db-identities.json --confirm
```

Finalize is the irreversible isolation cutover. It removes every
`health_user` privilege scoped to the `health_registry` schema, its objects,
and schema-scoped default ACLs, plus every `health_user` → `health_admin`
bridge row. It does not touch `health_user` privileges outside that schema or
unrelated memberships. Pre-bootstrap registry ACLs and bridge details in the
manifest are rollback evidence only; rollback restores them, finalize never
does. Unknown registry objects, schema-contract drift, unsafe provisioning
state, or fixed-identity drift block finalization. After success the dormant
legacy login has no registry access.

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
privileges, role attributes, and the canonical PostgreSQL 16 automatic grant
of each tenant role to `health_admin` (`ADMIN TRUE, INHERIT FALSE, SET FALSE`).
It runs tenant-to-registry, all-pairs tenant, and registry-to-every-tenant
read/write/DDL probes; every denial must return SQLSTATE 42501. Output is one
pseudonymous JSON document and a failure exits nonzero.

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
legacy image. After fixed identities have been finalized, restore the database
identity manifest first: tenant catalog rollback deliberately refuses to
recreate the removed `health_user` → `health_admin` SET bridge itself. Then
restore the tenant manifests. Registry disablement, tenant-marker restoration,
and catalog restoration are separately authorized, idempotent phases; they are
not a cross-role transaction. A failed or interrupted attempt leaves isolated
routing disabled and is safe to retry. Database-identity rollback is itself
transactional and idempotent. On a clean installation, objects and data
created after bootstrap are retained; newly created fixed roles are retained
as dormant `NOLOGIN` roles because passwords are intentionally not captured.
The command emits those retained artifacts as JSON for operator review.

```bash
/app/tenant_isolation --mode rollback-db-identities --manifest /secure/health-db-identities.json --confirm
make tenant-isolation ARGS='--mode rollback --all --primary-schema health --manifest /secure/health-isolation.json --confirm'
```

Deploy the exact immutable image printed by the rollback command. Never revoke
the legacy shared login until the canary, restart, clean-install, rotation, and
rollback rehearsals have all passed on a disposable database.
