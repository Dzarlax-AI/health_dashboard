# Verifying the shared dbt catalog

The local graph exposes 456 catalog identifiers through `ReadCatalogMetricPage`. Of these, 330 bind to named model fields, 123 preserve original observations, and three have explicit non-value dispositions. Each returned sample carries its native key, source, type and missing-value status. Reading a stored decision establishes what was recorded. Reproducing its calculation also requires the inputs available at the time.

## Reproducing the checks

Run these commands from the repository root after setting up the dbt environment described in the pilot README. Catalog assembly uses the companion Health Sync checkout to verify the iOS ingress definitions.

```sh
python3 experiments/dbt_metrics/scripts/run_profile_graph.py
python3 experiments/dbt_metrics/metric_schema/run.py --output /tmp/health-synthetic-catalog.json
PATH="$PWD/experiments/dbt_metrics/.venv/bin:$PATH" experiments/dbt_metrics/.venv/bin/python experiments/dbt_metrics/scripts/check_tenant_aggregate_worker.py
go test ./... -count=1
```

For an authorized archive, compare both readers on the same private snapshot of each profile. The CLI in `scripts/verify_catalog_archive.py` emits model selectors, graph variables and comparison SQL. The family-specific `verify_archive_*` scripts compare projections or rerun formulas at the stated grain. `scripts/catalog_archive_matrix.py` combines the results and distinguishes exact agreement, differences, empty comparisons and absent evidence. Keep profile-level reports local.

## Product scope

The tenant worker publishes hourly aggregates and all 14 modeled daily fields. The opt-in completed-day reader uses the tenant’s existing calendar and requires a ready publication at the current source generation and formula. The selected date retains its live calculation. The all-time steps chart has an independent switch. Reading the remaining catalog families through the shared interface requires an installed profile graph. The v18 schema and decision capture are separate rollout steps.

## Archive evidence

All active definitions have a comparison on the five-profile archive matrix. Positive comparisons include native observations, stored values and independently reconstructed calculations. Ten definitions disagreed with previously materialized values and passed an exact comparison after deterministic aggregation and withdrawal repair. The matrix retains the old discrepancies next to the independent recalculation. Each of the 31 identifiers with empty or NULL archive results has a non-null example in the verified synthetic bundle.

The historical Readiness score and band are checked on reconstructible calendar dates. Final Today fields and normalized illness evidence use the final briefing capture. Retrospective replay uses the data available during replay. Delivered reports retain the body recorded at delivery.
