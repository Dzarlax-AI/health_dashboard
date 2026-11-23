# Tenant dbt aggregate worker

Schema contract v17 adds an independent dirty-date queue and a published hourly/daily aggregate set inside each tenant schema. The optional worker builds the dbt models in a fixed workspace in the same schema. The Go ingestion transaction advances its existing calculation generation and records changed dates for enrolled workers. Go and dbt each acknowledge their own work. An accepted sync does not wait for dbt. The first reader is the all-time daily steps chart through the existing `/fragments/step-daily-series?to=YYYY-MM-DD` request (open lower bound). Explicit date ranges on this endpoint, other charts, Today, Readiness, Energy and sent reports retain their existing readers.

## Install and enroll one profile

1. Use the normal stopped-service fleet schema migration and audit with the candidate backend. Both worker flags default to false. The Go aggregate contract retains its v6 identity.
2. Take the worker digest from the successful main release job for this backend revision. CI exercises the worker image against a disposable PostgreSQL tenant before publishing its full-SHA tag. Backend/frontend compatibility continues to use the existing pair pointer.
3. On the trusted operator host, use the matching backend binary and the installation's normal isolated database environment. In a private directory run `/app/dbt_worker_secret --schema SCHEMA --tenant-id UUID --out /secure/profile-dbt.dsn`. The command verifies the active registry and tenant contract and creates a new mode-0400 file. Give the worker UID 65532 read access, e.g. by setting that file's owner on the host. During credential rotation generate a new file and recreate the container with the new mount. If using a custom TLS trust root, mount the CA at the absolute path used in the DSN.
4. Provide `HEALTH_DBT_WORKER_IMAGE` as the immutable digest reference, `HEALTH_DBT_TENANT_ID`, `HEALTH_DBT_TENANT_SCHEMA` and `HEALTH_DBT_TENANT_SECRET_PATH` as deployment inputs. The Compose supplement joins the base project's network. For an external Postgres attach the service to its normal private database network. Each enrolled profile needs its own service instance and secret.
5. Start the worker with `docker compose -f docker-compose.yml -f compose.dbt-worker.yml --profile dbt up -d dbt-worker`. Its only database credential belongs to the named tenant. It has owner access inside that schema. The worker checks the UUID-derived login, private schema ownership, tenant marker and isolation before calculating.
6. In a session authenticated to the selected tenant, set the tenant schema as the search path and run `UPDATE hdbt_state SET shadow_enabled=true WHERE singleton`. The first pass inventories dates present in source and aggregate history, then processes batches (default 16, configurable from 1 to 366). Subsequent passes start with changed dates and include associated step inputs when a UTC hour spans local dates.

## Verify and switch

Inspect status with the following tenant-local queries. Queue size can initially grow during incoming syncs. A caught-up worker reports ready with the current source generation.

```sql
SELECT shadow_enabled, read_enabled, initialized, ready, last_status,
       source_generation, published_at, heartbeat_at FROM hdbt_state;
SELECT (SELECT count(*) FROM hdbt_backfill) AS bootstrap_remaining,
       (SELECT count(*) FROM hdbt_dirty) AS changed_dates;
```

Compare the complete daily series returned by the normal all-time steps request before and after enabling reads on a rehearsal copy. Repeat the comparison after a late step correction and a duplicate sync. Verify an independently authenticated sibling profile keeps its original result. The candidate sums REAL calorie cells in chronological order. The deployed Go v6 aggregate can differ in its last floating-point bits. The step chart requires exact equality.

After verification, run `UPDATE hdbt_state SET read_enabled=true WHERE singleton AND shadow_enabled AND ready` and check the affected row count. On a new source generation the reader can serve the existing Go path until dbt publishes. A worker that stops after publishing can continue to serve its complete generation while the source stays at that generation.

## Roll back

Set `read_enabled=false` in the tenant's singleton row to switch the chart back immediately while shadow computation continues. To end enrollment, set `shadow_enabled=false` and stop the worker. Re-enrollment bootstraps anew. Use the deployment schema gate and its rollback procedure for any backend rollback. The worker never writes sent reports.

## Local checks

Build with `docker build -f Dockerfile.dbt-worker -t health-dbt-worker:review .`. Run `python3 experiments/dbt_metrics/scripts/check_tenant_aggregate_worker.py --worker-image health-dbt-worker:review` for the disposable-tenant rehearsal. The worker and its image smoke test require PostgreSQL. All patient data in the image smoke test is synthetic.
