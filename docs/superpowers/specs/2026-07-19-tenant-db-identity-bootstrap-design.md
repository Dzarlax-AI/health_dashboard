# Tenant Database Identity Bootstrap Design

Date: 2026-07-19
Status: implemented locally; verification and PR review in progress
Related issue: #198
Related designs: `2026-07-12-tenant-database-isolation-design.md`, `2026-07-14-tenant-schema-release-gate-design.md`

## Summary

Before production tenant isolation can be enabled, Health Dashboard needs one reviewed and repeatable way to create the database identities assumed by the existing isolation code. The same bootstrap contract must cover an existing shared-login installation, a clean installation, and disposable PostgreSQL tests.

The bootstrap introduces a dedicated provisioning login (`health_admin`) and a dedicated registry login (`health_registry`). It does not make either identity a PostgreSQL superuser. Tenant logins continue to be created dynamically from immutable tenant IDs by the existing provisioner.

Production ownership and environment changes remain blocked until the bootstrap implementation passes its disposable-database test suite and the generated production plan has been reviewed.

## Current State and Gap

- Production PostgreSQL is 17.9. The running service still authenticates as the shared `health_user` role and tenant isolation is disabled.
- `health`, `health_mariia`, `health_review`, and `health_registry` are owned by `health_user`; no dedicated isolation roles exist yet.
- The service already requires `ADMIN_DATABASE_URL`, `REGISTRY_DATABASE_URL`, a credential-free `TENANT_DATABASE_URL_BASE`, and versioned tenant master-secret configuration when isolation is enabled.
- The isolation migrator can create tenant roles, transfer schema objects, write rollback manifests, and audit tenant-to-registry and tenant-to-tenant denial.
- The deployment stack's `init.sql` still creates only `health_user` and assigns both `health` and `health_registry` to it. It cannot produce a clean isolated installation.
- The runbook says to bootstrap administrator and registry identities but does not provide an executable, tested contract for their attributes, ownership, or grants.

This gap is operationally significant: using `health_user` for registry access defeats the isolation boundary, while improvising a superuser `health_admin` would place cluster-wide authority inside the application container.

## Role Contract

### `health_admin`

`health_admin` is a `LOGIN CREATEROLE` role with `NOSUPERUSER`, `NOCREATEDB`, `NOREPLICATION`, and `NOBYPASSRLS`. Its credential is root-only deployment configuration. The service uses it only through the one-connection provisioning pool; it is never attached to tenant request context.

The database owner grants `health_admin` `CONNECT` plus `CREATE` on the application database, with grant option only for `CREATE`, so it can create schemas and perform controlled ownership transitions. Tenant roles receive database `CREATE` only inside the same transaction as an ownership transition, and that privilege is revoked before commit. The bootstrap does not alter database-wide `PUBLIC` privileges used by unrelated services in the shared database.

On PostgreSQL 16 and newer, a non-superuser `CREATEROLE` creator receives a constrained administrative membership in each role it creates. The audit will allow exactly one documented relationship per tenant role: tenant role granted to `health_admin` with `ADMIN TRUE`, `INHERIT FALSE`, and `SET FALSE`. Any other membership or altered option is a blocker. Provisioning may temporarily enable the set-role access required for an ownership operation, but must restore and verify the constrained final state before commit.

For an existing installation only, the database bootstrap grants `health_admin` a reviewed migration bridge to objects owned by `health_user`. The bridge remains only through the rollback rehearsal window and is explicitly removed in final cleanup. It is never used as tenant request routing.

### `health_registry`

`health_registry` is `LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`. It owns the `health_registry` schema and its tables, indexes, sequences, and future objects because `Registry.EnsureSchema` performs additive DDL at startup.

It has `CONNECT` to the application database and no `USAGE`, object privilege, ownership, or role membership involving any tenant schema. A direct registry-login probe against every tenant schema must fail with SQLSTATE `42501`.

While registry ownership is prepared under the still-running legacy service, `health_user` temporarily retains only the registry DML and schema-usage privileges needed by that process. These grants are recorded in the bootstrap rollback artifact and revoked before the authoritative isolation audit. A rollback restores registry ownership before the previous image starts; the isolated steady state does not leave `health_user` access to registry data.

### Tenant roles

Tenant roles remain `LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`, own only their canonical tenant schema and objects, and have no access to the registry or other tenant schemas. Their passwords remain derived from the versioned master secret and are never written to manifests, logs, registry rows, or command output.

## Bootstrap Components

1. A source-controlled bootstrap command or SQL generator in the Health Dashboard repository creates or reconciles `health_admin` and `health_registry`, validates every role attribute and grant, and refuses ambiguous pre-existing identities. It never accepts identifiers from tenant-facing input.
2. An existing-install mode inventories `health_user` ownership, creates the migration bridge, transfers registry ownership, grants narrowly scoped legacy compatibility access, and emits a root-only rollback artifact before committing.
3. A clean-install mode creates the two fixed service identities and an empty registry, then lets the existing provisioner create the first tenant through the normal schema-contract path. It must not create a shared `health_user` tenant owner.
4. The Personal AI Stack initialization and compose configuration invoke or mirror the same declared contract. Static `changeme` credentials are not accepted as a production path.
5. The tenant fleet audit adds the missing registry-to-tenant denial probes and validates the one allowed constrained provisioning relationship. Default output remains pseudonymous and secret-free.

The executable contract lives in one implementation unit in Health Dashboard. Stack initialization calls that contract or is mechanically verified against it, avoiding two independently drifting copies of the privilege model.

## Existing-Installation Cutover

1. Verify the full database, roles-only, and environment backups and record hashes and modes.
2. Create/reconcile `health_admin` and `health_registry` without changing tenant routing.
3. Transfer the registry schema and all contained objects to `health_registry`; retain manifest-backed DML compatibility only while the already-running legacy process remains online.
4. Run inventory and dry-run for all canonical tenants while the legacy service remains running.
5. Stop only `health-receiver` immediately before the first tenant ownership mutation.
6. Apply the reviewed per-schema canary manifest, verify both directions of registry denial and all tenant-pair denial, then apply the remainder with `health` last.
7. Revoke the legacy process's temporary registry access and run the authoritative fleet audit while the service is stopped.
8. Update the root-only runtime environment with the same master secret/version and distinct DSNs, enable isolation, and start the exact already-tested image digest.
9. Verify readiness, restart count, logs, normal user workflows, and repeat the fleet audit.
10. Retain only the administrative `health_admin`-to-`health_user` migration bridge, manifests, backups, and previous image until all rehearsals pass; cleanup is a separate explicit operation. The dormant `health_user` has no tenant or registry data privileges in the isolated steady state.

The manifest basename from fleet dry-run is reused for canary and fleet apply so that per-schema manifest resolution cannot silently diverge.

## Failure and Rollback Semantics

- Bootstrap and migration steps are idempotent and fail closed on unexpected owners, grants, role attributes, memberships, identifiers, or partially applied state.
- The service is not automatically restarted after a failed ownership migration or pre-start audit.
- Before tenant ownership changes, rollback restores the original registry ownership/grants and removes only newly created bootstrap identities that own nothing.
- After tenant ownership changes, rollback uses the reviewed per-schema manifests, disables isolation, restores the previous root-only environment, and starts the recorded immutable image only after compatibility is confirmed.
- No automatic rollback drops a tenant role, tenant schema, registry row, marker, or health data.

## Test Contract

Disposable PostgreSQL tests must prove:

- bootstrap succeeds twice without drift;
- `health_admin` is not superuser and has only the declared attributes and grants;
- `health_registry` can run `Registry.EnsureSchema` twice and normal registry CRUD;
- `health_registry` receives SQLSTATE `42501` for qualified reads, writes, and DDL in every tenant schema;
- every tenant role receives SQLSTATE `42501` for registry and other-tenant operations;
- the provisioning relationship returns to its exact constrained state after success and after injected failure;
- an existing `health_user` installation migrates and rolls back without data or object loss;
- a clean database reaches the same schema contract without creating a shared tenant login;
- secrets, passwords, DSNs, usernames, emails, and raw schema identifiers do not appear in default audit output;
- container smoke includes the bootstrap, migration, and audit entrypoints used by deployment.

After these focused tests, run the full Go suite, database integration suite, container smoke tests, and a clean-stack installation rehearsal before touching production ownership.

## Repository and Deployment Impact

Likely Health Dashboard changes include the tenant bootstrap/provisioning package, isolation audit, CLI, integration tests, `.env.example`, the tenant isolation runbook, and the deployment preflight wrapper. The existing HTML release plan will be updated with actual evidence after implementation.

The Personal AI Stack repository also requires a small coordinated configuration change so a new PostgreSQL volume creates the reviewed identities and starts Health Dashboard with distinct DSNs. Because it is a separate Git repository, this cannot be part of the same GitHub pull request; both changes must nevertheless be tested together before the production cutover.

## Risks and Boundaries

- `health_admin` remains highly privileged inside the application database. Its DSN must stay root-readable and must never reach handlers, tenant pools, logs, or diagnostics.
- The legacy migration bridge deliberately permits rollback compatibility and weakens isolation until final cleanup. Its presence must be visible in evidence and must not be described as completed hardening.
- PostgreSQL role-membership semantics differ before version 16. The implementation must either enforce a documented minimum PostgreSQL version or provide tested version-specific behavior; it must not assume PostgreSQL 17 silently.
- Registry ownership permits additive DDL because current startup behavior requires it. Splitting registry migration-owner and runtime-DML roles is outside this cutover.
- This work does not add a general ordered migration ledger or remove startup DDL from tenant roles.

## Implementation Record

The approved dedicated-admin/dedicated-registry direction is implemented on the isolated `codex/tenant-db-identity-bootstrap` worktree. No production database, deployment environment, or running production service was changed.

The implementation adds:

- bootstrap, authoritative verify, finalize, and rollback database-identity CLI modes;
- a versioned, target-bound, checksummed rollback manifest with strict file validation and atomic no-overwrite publication;
- exact fixed-role, database-grant, registry ownership, ACL, default-ACL, and membership reconciliation using supported PostgreSQL DDL only;
- distinct admin, registry, and tenant connection paths, including exact connected-identity checks and hardened connection parameters;
- registry-to-tenant denial probes in the authoritative fleet audit;
- retry-safe multi-identity migration, credential rotation, and rollback phases that fail closed when the legacy rollback bridge is unavailable;
- PostgreSQL 17 `MAINTAIN` privilege coverage while retaining PostgreSQL 16 compatibility;
- a PostgreSQL 17 clean-install Compose path that bootstraps identities once, stores the rollback artifact in a dedicated volume, and starts the runtime without the bootstrap DSN;
- CI lifecycle and clean-container smoke coverage for bootstrap, runtime start, contract migration, audit, finalize, ingest, and restart.

During strict integration testing, the rollback path exposed a real fixed-identity defect: `health_admin` could not replay tenant-owned catalog state directly. Rollback now temporarily expands only the existing tenant-administrator relationship inside the catalog transaction, performs ownership restoration, and removes the grantor-specific temporary membership before commit. PostgreSQL 16 integration tests assert that the canonical `ADMIN TRUE, INHERIT FALSE, SET FALSE` creator relationship remains after both the first rollback and an idempotent retry.

## Verification Record

The following checks passed on the uncommitted implementation worktree:

- full Go test suite;
- `go vet ./...`;
- whitespace/diff validation, workflow YAML parsing, and `docker compose config`;
- strict PostgreSQL 16 security lane using exact `health_admin` and `health_registry` DSNs: registry tests, all tenant tests, selected storage security-contract tests, and final authoritative fixed-identity verification;
- complete database-identity lifecycle on disposable PostgreSQL 16 and PostgreSQL 17 instances;
- focused migration/apply/credential-rotation/rollback coverage, including legacy fleet-lock compatibility, wrong registry-identity rejection, same-target rotation retry, and canonical membership restoration;
- disposable PostgreSQL 17 clean-install rehearsal through bootstrap, first-tenant provisioning, readiness, contract migration, fleet audit, finalize, fixed-identity verification, ingest, and runtime restart.

The final clean-container smoke was repeated from the final worktree with fresh database and manifest volumes. Independent code review reported no blocker or important finding. CI remains required; local evidence does not authorize production rollout.

## Known Limitations and Follow-up

- The checked-in Compose defaults are development-only. Production must provide an immutable `HEALTH_IMAGE`, distinct URL-encoded credentials, a root-readable environment, and a fresh master secret; default strings are not production credentials.
- The repository-level clean-install path is covered here. Coordinated changes to the separately managed production stack remain a deployment task and cannot be included in this repository's pull request.
- Rollback after database-identity finalization must restore the identity manifest/legacy bridge before tenant catalog rollback. The CLI fails closed if that ordering is violated.
- `health_admin` remains intentionally powerful within the application database. Splitting schema migration from runtime provisioning is outside this change.
- This change does not introduce a general ordered migration ledger or remove all additive startup DDL.

## Remaining Gate

Before production work, the final diff must pass independent review and CI, be committed and opened as a pull request only with explicit user approval, and then be merged into an immutable image. Production cutover still requires a fresh backup, reviewed root-only environment, recorded rollback artifacts, exact image digest, stopped-service ownership migration, authoritative pre-start audit, and post-start health/audit verification.
