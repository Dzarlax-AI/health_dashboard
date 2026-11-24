# Tenant dbt aggregate worker

Schema contract v17 adds an independent dirty-date queue and a published hourly/daily aggregate set inside each tenant schema. The optional worker builds the dbt models in a fixed workspace in the same schema. The Go ingestion transaction advances its existing calculation generation and records changed dates for enrolled workers. Go and dbt each acknowledge their own work. An accepted sync does not wait for dbt. The first reader is the all-time daily steps chart through the existing `/fragments/step-daily-series?to=YYYY-MM-DD` request (open lower bound). Explicit date ranges on this endpoint, other charts, Today, Readiness, Energy and sent reports retain their existing readers.

## Install and enroll one profile

1. Use the normal stopped-service fleet schema migration and audit with the candidate backend. Both worker flags default to false. The Go aggregate contract retains its v6 identity.
2. Take the worker digest from the successful main release job for this backend revision. CI exercises the worker image against a disposable PostgreSQL tenant before publishing its full-SHA tag. Backend/frontend compatibility continues to use the existing pair pointer.
3. On the trusted operator host, use the matching backend binary and the installation's normal isolated database environment. In a private directory run `/app/dbt_worker_secret --schema SCHEMA --tenant-id UUID --out /secure/profile-dbt.dsn`. The command verifies the active registry and tenant contract and creates a new mode-0400 file. Give the worker UID 65532 read access, e.g. by setting that file's owner on the host. During credential rotation generate a new file and recreate the container with the new mount. For `sslmode=verify-full`, the worker uses the system CA bundle unless the DSN specifies `sslrootcert`. Mount a custom CA at the absolute path specified in the DSN.
4. Provide `HEALTH_DBT_WORKER_IMAGE` as the immutable digest reference, `HEALTH_DBT_TENANT_ID`, `HEALTH_DBT_TENANT_SCHEMA` and `HEALTH_DBT_TENANT_SECRET_PATH` as deployment inputs. The Compose supplement joins the base project's network. For an external Postgres attach the service to its normal private database network. Each enrolled profile needs its own service instance and secret.
5. Start the worker with `docker compose -f docker-compose.yml -f compose.dbt-worker.yml --profile dbt up -d dbt-worker`. Its only database credential belongs to the named tenant. It has owner access inside that schema. The worker checks the UUID-derived login, private schema ownership, tenant marker and isolation before calculating.
6. In a session authenticated to the selected tenant, set the tenant schema as the search path and run `UPDATE hdbt_state SET shadow_enabled=true WHERE singleton`. The first pass inventories dates present in source and aggregate history, then processes batches (default 16, configurable from 1 to 366). Subsequent passes start with changed dates. For each requested date the workspace includes occupied dates within two days and step inputs within four days to reconcile offset aliases. If any sync advances the journal during a build, the worker discards that attempt. Under continuous ingestion, reduce the batch size so each batch fits between changes. Repeated failures back off up to five minutes. A source date outside the supported calendar (years 0001 through 9999) yields `unsupported_calendar_date` and stays in the queue for correction.

## Verify and switch

Inspect status with the following tenant-local queries. Queue size can initially grow during incoming syncs. A caught-up worker reports ready with the current source generation.

```sql
SELECT shadow_enabled, read_enabled, initialized, ready, last_status,
       source_generation, published_at, heartbeat_at FROM hdbt_state;
SELECT (SELECT count(*) FROM hdbt_backfill) AS bootstrap_remaining,
       (SELECT count(*) FROM hdbt_dirty) AS changed_dates;
```

On a rehearsal copy, record the complete chart with reads disabled. Compare every point of the enabled chart to the independent step oracle in the archive harness, including missing days and measured zeroes. Repeat after a late correction and its duplicate. For fractional source data, older Go v6 reads can vary with the PostgreSQL aggregation plan: a one-bit difference in an hourly sum can change the decision to retain an entire legacy hour. The serving formula sums minute evidence in time order and uses exact checks to decide which hours can be reconstructed. Keep the pre-cutover delta as a separate measurement. Verify an independently authenticated sibling profile keeps its original result. The candidate sums REAL calorie cells in chronological order. The deployed Go v6 aggregate can differ in its last floating-point bits. The step chart requires exact equality.

After verification, run `UPDATE hdbt_state SET read_enabled=true WHERE singleton AND shadow_enabled AND ready` and check the affected row count. While an update is pending, a reader with both flags enabled combines previously published clean dates with a current, chronological calculation for the dirty dates and their neighbors. When there is no compatible complete baseline, it uses the full chronological calculation. Turning off reads restores the v6 reader. A worker that stops after publishing can continue to serve its complete generation while the source stays at that generation.

## Roll back

Set `read_enabled=false` in the tenant's singleton row to switch the chart back immediately while shadow computation continues. To end enrollment, set `shadow_enabled=false` and stop the worker. Re-enrollment bootstraps anew. Use the deployment schema gate and its rollback procedure for any backend rollback. The worker never writes sent reports.

## Local checks

Build with `docker build -f Dockerfile.dbt-worker -t health-dbt-worker:review .`. Run `python3 experiments/dbt_metrics/scripts/check_tenant_aggregate_worker.py --worker-image health-dbt-worker:review` for the disposable-tenant rehearsal. The worker and its image smoke test require PostgreSQL. The image smoke test uses synthetic data. To rehearse on a trusted, local custom-format profile dump, use `bash experiments/dbt_metrics/scripts/with_serving_archive.sh /path/to/archive.dump` with the pinned PostgreSQL image cached, a locked dbt environment and local PostgreSQL client tools. Set `HEALTH_ARCHIVE_WORKER_IMAGE` to the local worker image ID to include the containerized correction. The harness requires the complete Git tree of the pinned v16 baseline, `863ea99884cdbd4d02f05b1bda9c55af877bfb48`, in the local clone. It checks the tree before creating the database or restoring the dump. For a shallow checkout, fetch the baseline and its tree first.

The archive run copies personal health data into an auto-removed PostgreSQL container and its anonymous data volume. The private temporary directory under `TMPDIR` holds a database password, tenant DSNs, dated chart responses, an accepted health payload, and the restore log. Use a trusted local Docker engine and a temporary filesystem appropriate for that data. Stdout contains aggregate results. Keep the original dump and the temporary files out of CI logs and uploads. On exit the harness stops PostgreSQL, attempts forced container and volume removal if stopping fails, and removes the temporary directory. After a machine crash or SIGKILL, check the disposable container, anonymous volume, and temporary directory before treating the copy as removed.

### Decision publication in contract v20

The tenant settings `dbt_decision_capture_v1` and `dbt_decision_result_reads_v1` control different operations. Capture records the inputs available when the application evaluates a decision, its formula version and the reference result. The enrolled worker evaluates supported Energy, Readiness and Today receipts. It compares each candidate to the reference before publishing the batch in one transaction. A new receipt for an old date is eligible after the daily aggregates have caught up.

The serving adapter checks the current inputs and formula version on each lookup. A missing publication or a late correction uses the ordinary solver. Disable the decision-result read setting for a per-profile rollback. Previously sent report bodies come from report history. Enabling capture today provides evidence for future decisions. It cannot recover the original inputs for older reports.

For a rehearsal, compare supported receipts, pending revisions where `blocked_reason IS NULL`, quarantined revisions and usable published results within the same read-only snapshot. A quarantined receipt keeps a fixed reason and is excluded from serving. A transient replay failure reports `decision_retry` while aggregation keeps its ordinary schedule. Investigate quarantined reasons before enabling result reads. A profile with zero receipts provides no evidence of decision parity.

### Confirmed daily withdrawals

The source mutation and its cache generation record confirmed withdrawals together. The worker captures the metric and day in the same snapshot as the inputs and acknowledges the receipt only after publishing that day. A fresh valid value takes priority. If the affected metric has no fresh value, publication clears its retained value. Sleep stages follow the retained-night selection together. Cache-only history without a withdrawal receipt is still eligible for retention. Receipts are prospective, so use a separate verified repair procedure for stale values published before this contract.

### Native catalog installation

On the rehearsal tenant, run the existing worker image with `--install-native-catalog` and its scoped connection settings. The installer verifies tenant identity, the schema contract and the worker lock before running dbt. Installation and read activation are separate operations. Enable `dbt_native_catalog_reads_v1` on the rehearsal profile to exercise the shared reader. Disable the setting to roll back. Use `dbt_hourly_chart_reads_v1` to test published compatible chart hours independently.

`catalog_producer_inventory.py` in `experiments/dbt_metrics/scripts/` lists each ID with its producer and publication path. Native views can project observations, expose an archived record, or run a formula from retained inputs. The indexed decision queue materializes Readiness, EnergyBank and Today. An empty queue confirms completion for those eligible receipts. Archive acceptance also needs coverage counts for each producer and the independently verified historical inputs.


The catalog installer publishes `tenant_native_metric_read_v1`, `tenant_native_sleep_goal_v1`, `tenant_native_energy_snapshot_v1` and `tenant_native_workout_v1` in one transaction. A failed typed projection rolls back the installation. Setting `dbt_native_catalog_reads_v1=false` returns subsequent product reads to their ordinary tenant tables. Installation alone leaves the flag unchanged.

The typed reads serve the effective-date lookup for `GET /api/sleep/goal`, the latest observed Energy snapshot in Today and historical insights, observed points for `/api/energy-history?granularity=hour`, and MCP `list_workouts` and `get_workout`. Energy retains its separate retrospective overlay. MCP `get_sleep_balance` reads the saved balance directly. Stored report text, delivery timestamps and explanations remain on the original report revision. Workout type/statistics summaries and research queries have separate result grains. The workout/sleep research endpoint is absent from this branch. The local worker harness exercises the sleep goal HTTP handler, the empty and effective-date responses, read rollback and index plans on synthetic archives.

On the disposable PostgreSQL fixture with 9,001 goal rows, 20,000 workouts and 105,409 five-minute Energy snapshots, the effective goal and exact workout queries visited three shared buffers each. The historical Energy lookup visited six buffers and 288 rows for the requested date. Before the day restriction, the timestamp-index walk filtered out 61,345 newer rows and visited 1,095 shared buffers. These figures describe the SQL reads on a warm synthetic archive.

The authenticated catalog operation is `/api/metrics/data?view=catalog&metric=<id>`. Its optional filters include `from`, `to`, `date_field`, `limit` (default 50, maximum 256) and `cursor`. Expect a 503 while the tenant catalog is unavailable.
