# Tenant Database Isolation Design

Date: 2026-07-12
Status: proposed; user review required before implementation planning
Related issue: #198
Related plan: `docs/ai-plans/2026-07-12-full-service-audit-remediation.html`

## Summary

Health Dashboard will replace shared-login, `search_path`-only tenant routing with database-enforced isolation. Each tenant will use a dedicated PostgreSQL `LOGIN` role that can access only its own schema. Registry access and schema/role provisioning will use separate credentials that are never placed in a tenant request context.

The immediate security invariant is:

> SQL executed through tenant A's runtime pool cannot read, write, execute, or create objects in tenant B's schema or `health_registry`, even if the SQL explicitly qualifies those schemas.

This design supplements the removal of the arbitrary MCP `sql_query` tool. Application validation remains necessary, but PostgreSQL privileges become the final tenant boundary.

## Why the Current Model Is Unsafe

Today all tenant pools authenticate with the same database login. `search_path` selects the default schema but does not restrict explicit references. A shared role that owns or can use all tenant schemas can run `SELECT`, `INSERT`, DDL, or functions across them.

`SET ROLE` is not an adequate replacement. If the shared session user is a member of all tenant roles, injected SQL can use `RESET ROLE` or `SET ROLE` to regain or switch privileges.

## Role Architecture

The deployment will have three role classes.

### Provisioning role

- Configured through `ADMIN_DATABASE_URL`.
- May create and alter tenant roles and schemas.
- May run schema migrations and ownership/grant changes.
- Uses a short-lived pool with `MaxConns=1`, `MinConns=0`.
- Is never returned by `tenants.Manager`, stored in request context, or exposed to MCP/UI handlers.
- Is not required to be PostgreSQL superuser. It needs only the narrowly documented database and role-management privileges.

### Registry runtime role

- Configured through `REGISTRY_DATABASE_URL`.
- Has `CONNECT` on the database and the minimum DML/sequence privileges required in `health_registry`.
- Has no `USAGE` or object privileges on tenant schemas.
- Cannot create roles or schemas.

### Tenant runtime roles

- One `LOGIN` role per immutable tenant ID.
- Role name derives from immutable identity, not mutable username, for example `health_t_<encoded-id>`.
- Has `CONNECT` on the database.
- Owns or has the required privileges only on its tenant schema and objects.
- Has no `CREATEDB`, `CREATEROLE`, `BYPASSRLS`, superuser, or membership in other tenant roles.
- Has no access to `health_registry`, other tenant schemas, or unrelated application schemas.

Initially the tenant role may own its schema so existing startup `Ensure*` operations continue to work. This protects cross-tenant confidentiality and integrity but does not protect a tenant from arbitrary SQL executed through its own pool. A later hardening phase may split each tenant into migration-owner and DML-only runtime roles after startup DDL is centralized.

## Immutable Tenant Identity and Credentials

`health_registry.users` will gain:

- `tenant_id UUID NOT NULL UNIQUE`
- `db_role TEXT NOT NULL UNIQUE`
- `db_credential_version INTEGER NOT NULL`

Existing users receive stable IDs in an idempotent migration. Username changes never change `tenant_id` or `db_role`.

Tenant database passwords are not stored in the registry. The application derives them from a deployment secret:

```text
password = base64url(HMAC-SHA-256(master_secret_version, tenant_id || db_role))
```

The encoded result must satisfy PostgreSQL password and DSN requirements without additional escaping. API keys, UI passwords, session tokens, and tenant database credentials remain independent.

Required configuration:

- `ADMIN_DATABASE_URL`
- `REGISTRY_DATABASE_URL`
- `TENANT_DB_MASTER_SECRET`
- `TENANT_DB_MASTER_SECRET_VERSION`

The service fails closed in multi-tenant mode when required isolation configuration is absent after the rollout flag is enabled. Secrets are never logged or returned by APIs.

## Credential Rotation

Rotation uses a dual-secret window:

1. Configure current and next master secret/version.
2. Provisioning role derives the next password and runs `ALTER ROLE ... PASSWORD` tenant by tenant.
3. Open an ephemeral pool with the next credential and verify `current_user`, `current_schema`, normal reads/writes, and cross-tenant denial.
4. Persist the new credential version in the registry only after the ephemeral verification pool passes.
5. The running service fails closed when it observes credential-version drift; it does not hot-replace pools held by tenant workers.
6. Restart the service successfully and verify that it opened new-version runtime pools for every tenant.
7. Remove the previous master secret only after the restart verification succeeds for every tenant and the rollback window closes.

The registry stores only the credential version, never the derived password.

## Provisioning State Machine

First-user and later-user provisioning must not rely on best-effort cross-connection cleanup alone. PostgreSQL DDL is transactional, but existing storage initialization uses separate pools, so the workflow will use durable provisioning state and reconciliation.

Registry provisioning states:

- `pending`: identity and intended role/schema are reserved.
- `provisioning`: role/schema/table work is in progress under a provisioning operation ID.
- `active`: tenant pool verification passed; authentication and routing are allowed.
- `failed`: provisioning did not complete; setup may safely retry or an administrator may reconcile.

Rules:

- Only `active` users are accepted by login, API-key lookup, ForwardAuth, or tenant routing.
- First-admin reservation is serialized by a PostgreSQL advisory transaction lock.
- All user inputs, password hashes, email constraints, immutable IDs, role names, and derived credentials are prepared before external provisioning begins.
- Provisioning uses plain `CREATE ROLE` and `CREATE SCHEMA`; duplicate-object results trigger reconciliation rather than assumed ownership.
- Every created schema contains a marker tied to tenant ID and provisioning operation ID. Cleanup may remove only objects whose marker matches the failed operation.
- Ambiguous commit/network outcomes are reconciled by reading durable registry state and catalog ownership. The service never drops a schema merely because `Commit` returned an indeterminate error.
- Cleanup failures are persisted and surfaced; they are not silently discarded.
- Startup reconciles stale `pending`/`provisioning` operations before exposing setup or starting tenant workers.

This state machine replaces the work-in-progress compensating cleanup currently present in the Phase 0 branch before that branch is considered complete.

## Tenant Pool Construction

`tenants.Manager` will receive separate dependencies:

- registry access for identity metadata;
- a tenant credential derivation component;
- a provisioning component using the admin DSN;
- tenant DSN base parameters without shared login credentials.

For an active tenant, Manager:

1. Loads immutable tenant identity, schema, role, and credential version.
2. Derives the tenant password.
3. Builds a DSN using that tenant's login.
4. Opens a pool with the existing per-tenant connection budget.
5. Verifies `current_user` equals the expected tenant role and `current_schema()` equals the expected schema.
6. Refuses to cache or return the pool if either assertion fails.

Admin and registry pools are never reachable from `ctxdb.WithDB`.

## PostgreSQL Grants

The migration will explicitly audit and enforce:

- `REVOKE CREATE ON DATABASE <db> FROM PUBLIC` after compatibility checks.
- `REVOKE CREATE ON SCHEMA public FROM PUBLIC`.
- `REVOKE ALL ON SCHEMA health_registry FROM PUBLIC`.
- `REVOKE ALL ON every tenant schema FROM PUBLIC`.
- Tenant role receives privileges only on its schema, tables, sequences, and required functions.
- Registry role receives only required registry table/sequence privileges.
- Tenant roles receive no membership in each other or in provisioning/registry roles.
- Any `SECURITY DEFINER` function is inventoried, given a fixed safe `search_path`, and tested; unexpected functions block rollout.

Existing object ownership must be transferred explicitly. Changing schema ownership alone does not transfer tables, sequences, or functions.

## Existing-Tenant Migration

Migration is additive and reversible until final revocation.

1. Inventory schemas, objects, owners, grants, default privileges, and functions. Abort on unknown ownership or unsafe `SECURITY DEFINER` functions.
2. Add registry identity/role/version columns without changing runtime routing.
3. Backfill immutable IDs and role names idempotently.
4. Create tenant roles and set derived passwords while the legacy shared role remains unchanged.
5. Transfer or grant ownership/privileges for one non-primary canary tenant.
6. Open a restricted pool and run the full tenant verification suite.
7. Route the canary tenant through the restricted pool; observe ingestion, dashboard, import, backfill, AI, notifications, and MCP.
8. Migrate remaining tenants one at a time. Migrate the primary/legacy tenant last.
9. After the soak window, revoke the shared runtime role's tenant and registry access and remove legacy routing.
10. Retain migration metadata and an immutable pre-change image reference for rollback until closure is explicitly approved.

No migration step invents tenant/schema names; it reads canonical values from the registry.

## Rollback

Before final privilege revocation, rollback switches routing to the legacy shared DSN and previous immutable image. Newly created roles remain dormant; no schema rollback is required.

After final revocation, rollback requires a reviewed grant-restoration script generated from the pre-migration privilege inventory. The script restores only previous grants and ownership; it never restores the removed MCP `sql_query` tool as a containment measure.

Tenant roles and identity columns are not dropped during rollback. Destructive cleanup is a separate post-soak operation.

## Testing

All database security tests use a dedicated disposable PostgreSQL database, never a populated personal or production registry.

Required integration tests:

- Tenant A can read/write and run required schema initialization in A.
- Tenant A receives SQLSTATE `42501` for qualified reads/writes/DDL against tenant B and `health_registry`.
- Changing `search_path`, multi-statements, functions, and direct qualification cannot cross boundaries.
- Registry role cannot read tenant data; tenant roles cannot read registry data.
- Provisioning role is never returned by Manager or inserted into request/MCP context.
- Provisioning failure/restart reconciliation is idempotent and marker-scoped.
- Ambiguous state is reconciled without dropping an active tenant.
- Existing-schema migration transfers every table, sequence, function, and default privilege correctly.
- Password rotation is verified before registry cutover; the running service
  then fails closed on metadata drift until a successful restart opens only
  new-version pools. Hot pool replacement is intentionally unsupported.
- `current_user` and `current_schema()` assertions fail closed.
- Normal ingestion, import, backfill, UI, AI, notification, and typed MCP tests pass through restricted pools.
- Connection usage remains within the existing registry plus per-tenant pool budget.

CI will gain a dedicated disposable-PostgreSQL isolation lane. Tests must refuse to run if the target registry is non-empty unless an explicit disposable-database marker is present.

## Deployment and Observability

- Log tenant ID/schema/role identifiers where operationally useful, but never passwords, master secrets, API keys, or DSNs containing credentials.
- Emit structured events for provisioning state transitions, pool identity verification, migration progress, reconciliation, rotation, and denial-test results.
- Add readiness failure when an active tenant cannot open its restricted pool or fails identity assertions.
- Keep notification sending disabled for migration test tenants until invite/notification safety is verified.

## Rejected Alternatives

### Shared login plus `SET ROLE`

Rejected because membership in all tenant roles lets arbitrary SQL switch or reset roles. It is routing, not an injection-resistant boundary.

### Shared schema with RLS

Rejected for Phase 0 because every table, key, index, query, cache, import stage, and conflict target would require tenant IDs and policy review. Robust RLS identity would still require distinct database roles, so it adds migration risk without removing the credential/role work.

### Random passwords stored in the registry

Not selected because it stores another class of plaintext/reversibly encrypted secrets in application data and complicates backup/restore. A future external secret-manager integration may replace deterministic derivation if operational needs justify it.

## Known Limitations and Follow-Ups

- Tenant schema ownership permits destructive SQL within the same tenant. Splitting migration-owner and DML-only runtime roles is a later hardening phase.
- Throttling remains per-process until a shared limiter is introduced.
- Typed MCP tools still require normal query bounding and input validation.
- The provisioning role remains highly privileged and requires independent secret rotation and access monitoring.

## Approval Gate

Implementation planning and code changes for database-role isolation must not begin until this design is reviewed and explicitly approved. The current Phase 0 branch remains incomplete until database-enforced cross-tenant and registry denial tests pass.
