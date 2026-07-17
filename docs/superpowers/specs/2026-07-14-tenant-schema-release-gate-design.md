# Tenant Schema Release Gate Design

Date: 2026-07-14
Status: approved and implemented
Related issue: #198

## Summary

Health Dashboard will gain an exact-set tenant schema release gate. The target image will expose an explicit, idempotent migration command and a separate read-only audit command. Production deployment will pin one immutable image digest, briefly stop the service to freeze tenant provisioning, migrate every canonical tenant schema, audit the complete fleet, start the same digest, and repeat the audit after readiness succeeds.

The release gate must prove that no registered tenant, physical tenant schema, database role, or unfinished provisioning operation was omitted. It must also prove that every active tenant is at the schema contract compiled into the target image and cannot access any other tenant schema or `health_registry`.

## Safety Invariants

1. The exact active registry set has a one-to-one mapping to canonical schema markers and tenant database roles.
2. Pending, provisioning, and failed registry rows are visible to the audit and cannot be silently excluded from the result.
3. Every active tenant's registry metadata, permanent schema marker, database ownership, privileges, credential version, and target contract version agree.
4. Every tenant role is denied access to every other tenant schema and to `health_registry`.
5. The migration command is mutating and requires explicit confirmation; the audit command is read-only apart from rollback-only denial probes.
6. Migration, pre-start audit, runtime container, and post-start audit use one resolved immutable image digest.
7. A failed migration or audit prevents the new image from starting. There is no silent continuation or automatic destructive rollback.
8. Fresh installations and upgrades execute the same schema-contract implementation and verification path.

## Canonical Identity and Contract Marker

`_health_tenant_provisioning` remains a transient recovery marker for an in-progress provisioning operation. It is not evidence that an active schema is ready.

Every active tenant schema must contain one permanent `__tenant_identity` row with:

- `singleton = true`;
- immutable `tenant_id`;
- the provisioning or migration `operation_id` that last established the marker;
- `schema_contract_version`;
- `schema_contract_checksum`.

The active registry row stores the same contract version and checksum. The target binary contains the expected version and checksum as constants. A migration updates the permanent marker and then advances the registry metadata with an exact compare-and-set only after `VerifyProvisionedSchema` passes. An interrupted migration therefore remains detectable and safely retryable.

The checksum identifies the declared contract manifest: required tables, columns, indexes, and critical definitions. It is not a checksum of tenant data or the full PostgreSQL catalog. Contract changes require an explicit version bump and manifest update.

## Shared Clean-Install and Upgrade Path

Schema creation and startup migration currently call many `Ensure*` methods. These calls will be centralized behind one storage-level schema-contract function used by:

- clean tenant provisioning;
- explicit fleet migration;
- legacy single-tenant startup compatibility;
- verification tests.

The contract function is idempotent. It performs the existing additive `Ensure*` operations, verifies the declared catalog contract, and only then writes the permanent contract marker. Registry metadata is updated by the caller after restricted-pool verification succeeds.

This first iteration does not introduce a general migration-history ledger. The versioned marker is intentionally lightweight. A sequential migration ledger remains a future option if destructive or order-dependent migrations become necessary.

## CLI Contract

The existing `tenant_isolation` binary gains two fleet modes and is included in the runtime image.

### `--mode migrate-contract --all --confirm`

- Loads registry tenants in all provisioning states.
- Refuses to run while nonterminal provisioning operations exist.
- Requires the canonical primary schema argument already used by fleet modes.
- Runs the shared schema-contract migration for every active tenant, primary last.
- Verifies the restricted tenant identity, contract, ownership, privileges, and credential version before advancing registry contract metadata.
- Is idempotent and exits nonzero on the first incomplete tenant without marking later tenants complete.
- Emits sanitized structured progress and a final JSON summary.

### `--mode audit --all`

- Does not run schema DDL or update registry rows.
- Inventories registry rows in every state, nonterminal operations, physical schemas with permanent markers, and marked tenant roles.
- Compares exact sets rather than checking only registry-selected active schemas.
- Re-reads the registry and marker inventory at the end; a changed digest invalidates the result.
- Checks the target contract constants, registry/marker identity, `db_isolation_ready`, credential version, schema/object ownership, grants, default privileges, and role membership.
- Performs all-pairs denial probes through restricted tenant connections. Catalog reads use a read-only transaction; active denial probes run in a transaction that is always rolled back.
- Exits zero only when the fleet exactly matches the target contract.

Default output uses stable pseudonymous tenant references and machine-readable reason codes. It never prints usernames, emails, passwords, master secrets, connection strings, raw DSNs, or payload data. A root-only `--verbose-identifiers` option may print schema and role identifiers for local remediation, but it remains off in CI and issue attachments.

## Deployment Sequence

The deployment wrapper performs the following sequence:

1. Pull the requested image reference and resolve it to an immutable repository digest.
2. Record the currently running image digest and verify that rollback configuration is available.
3. Run a non-mutating current-fleet inventory check while the old service remains available.
4. Stop `health-receiver`, creating a short maintenance window and preventing in-application tenant provisioning.
5. Run `migrate-contract` from the resolved target digest using the root-only migration environment.
6. Run `audit` from the same digest. Do not start the target service unless it succeeds.
7. Set `HEALTH_IMAGE` to the exact digest and start `health-receiver`.
8. Wait for `/readyz`, then run `audit` again from the same digest.
9. Verify restart count and recent logs before declaring success.

The authoritative migration and pre-start audit occur while the service is stopped. This avoids introducing a deployment-lease subsystem for the current small fleet. Direct out-of-band registry modifications remain prohibited during the maintenance window.

If migration or the pre-start audit fails, the service remains stopped and the wrapper prints the previous digest plus a reviewed recovery command. It does not automatically revert database changes. Because contract migrations are additive and idempotent, an operator may either fix and retry the target migration or explicitly restart the previous digest after confirming backward compatibility.

If readiness or the post-start audit fails, the target container is considered unhealthy. Rollback to the recorded digest is a separate explicit operator action.

## Exact-Set Audit Result

The JSON result contains:

- `status`: `pass` or `fail`;
- target contract version and checksum;
- start and end inventory digests;
- counts by registry state, physical marker, and tenant role;
- sanitized findings with `tenant_ref`, `code`, and `scope`;
- all-pairs denial probe totals;
- elapsed time.

Representative failure codes include `registry_nonterminal`, `registry_marker_missing`, `marker_registry_missing`, `role_registry_missing`, `contract_version_mismatch`, `contract_checksum_mismatch`, `credential_version_mismatch`, `isolation_not_ready`, `unexpected_owner`, `unexpected_grant`, `default_acl_mismatch`, `cross_tenant_access_allowed`, `registry_access_allowed`, and `inventory_changed`.

## Testing

The disposable PostgreSQL CI lane will provision three tenants and prove both success and failure cases. Negative fixtures include:

- one registry tenant without a schema marker;
- one orphan marked schema;
- one orphan tenant role;
- one unfinished provisioning operation;
- mismatched tenant identity;
- stale contract version or checksum;
- unexpected object owner/default privilege;
- one tenant granted access to each of the other two schemas;
- registry access accidentally granted;
- inventory changed between the audit snapshots.

Tests must assert nonzero exit status, stable sanitized reason codes, and absence of secrets or raw identifiers in default output. Container smoke tests must prove that both `/app/server` and `/app/tenant_isolation` exist in the emitted runtime image.

## Scope and Limitations

- This gate does not remove the admin DSN or master secret from the main service. Splitting provisioning into a separate service is a future architectural change.
- The brief maintenance window blocks HTTP traffic; eliminating it would require a durable provisioning/deployment fence.
- The contract manifest validates declared objects and critical definitions, not every catalog property PostgreSQL exposes.
- The deployment wrapper is source-controlled in this repository, but production environment values and secrets remain outside Git.
- No GitHub issue is closed merely because CI passes. Issue #198 may close only after production isolation is enabled and the post-deployment exact-set audit passes.

## Rollback

The wrapper records the previous immutable digest before stopping the service. Schema changes in this phase must be additive and compatible with that previous image. No role, schema, marker, registry column, or tenant data is automatically dropped during rollback. Reverting grants or ownership requires the existing reviewed rollback manifest workflow.

## Approved Decision

The user approved the exact-set migration/audit gate and selected a short maintenance window instead of a new online provisioning-fence subsystem. The implementation record is maintained in the accompanying HTML plan.
