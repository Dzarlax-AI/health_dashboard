# Release and migration runbook
## Required gates

Image publication depends on unit/build/vet, disposable PostgreSQL tests,
tenant-isolation denial tests, and a container start-ingest-shutdown-restart
smoke test. A failed required lane must not be bypassed by publishing `latest`
manually.

Before deployment:

1. Record the current immutable image digest and verify the normal database
   backup.
2. Review additive DDL. Startup deliberately avoids rewriting legacy
   multi-million-row tables: `source_snapshot_at` remains nullable for old rows
   and falls back to `received_at`.
3. For tenant isolation, follow `docs/TENANT_ISOLATION_RUNBOOK.md`; database
   cutover and the runtime flag are separate steps. Once isolation is active,
   use `scripts/deploy-tenant-schema-gate.sh` for every schema-contract release;
   do not start the new service unless both stopped-service migration and audit
   have passed for the exact pinned image digest.
4. Run the image against a disposable empty database. Complete first-user
   setup, ingest synthetic data, terminate with SIGTERM, restart, and confirm
   the accepted payload is processed exactly once.
5. Run an upgrade rehearsal against an anonymized schema copy. Confirm
   `/healthz` and `/readyz`, import precedence, notification sending disabled,
   and the schema verifier before touching production.

## Data migration contracts

- `health_records` remains the durable raw ingest log. New accepted records are
  `pending` until parsed; pending records replay after restart.
- Apple Health XML is not retained as a second compressed artifact. The
  canonical post-import contract is `health_records` import metadata plus
  `metric_points`/`workouts`, coverage, and immutable `snapshot_at` precedence.
  Operators must retain their original Apple export externally if required.
- XML point and workout replacement compares `source_snapshot_at`; legacy rows
  use `received_at`. Import heartbeat prevents cleanup from reclaiming an
  active session.
- Quality changes invalidate AI/Energy derived rows and rebuild affected daily
  caches. Formula version 3 identifies causal EnergyBank imputation.
- Notification report delivery is at-most-once. A lost provider response is
  persisted as ambiguous and is not retried automatically.

## Rollback

Application rollback uses the recorded digest, never a mutable tag. Additive
columns/tables remain compatible with the previous application. Tenant role
ownership must be restored from its checksum-protected manifest before legacy
runtime startup. Notification schedulers remain disabled during rollback to
avoid resending an ambiguous delivery.
