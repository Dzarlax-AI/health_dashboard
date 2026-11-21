# Health metric catalog

The [native dbt model properties](../models/health_metric_catalog.yml) hold the authoritative registry in `config.meta.health_catalog`. The checker reads those same properties. Run the checker from the repository root with a checkout of the Health Sync client alongside the server.

```bash
python3 experiments/dbt_metrics/scripts/check_semantic_catalog.py
python3 experiments/dbt_metrics/scripts/check_semantic_catalog.py --expand
python3 experiments/dbt_metrics/scripts/check_semantic_catalog.py --swift /path/to/HealthKitManager.swift
```

The observation registry covers explicit XML identities, sleep categories and special emissions, Health Sync quantity, event and sleep declarations, UI category lists, daily column mappings, SUM rules and named inclusive quality bounds. Refer to `healthkit_ingress.quantities` for explicit XML default and required-unit policies. The iOS field records the actual outgoing `serverUnits`, along with the quantity/event type and time resolution. Generic payloads can add keys at runtime. `--inventory /path/to/aggregate.json` compares an offline array of `{"metric_name", "units", "rows"}` objects to those declarations. A unit absent from the declarations needs review even when its spelling resembles a known unit. The checker does not open a database.

Each `family.field` resolves to one definition. Shared properties come from `derived_defaults` and the family, then field overrides apply. The owner and the Go type point to the canonical source declaration. `ignored_fields` records metadata, presentation or nested objects omitted at that level. Nested evidence has its own grain where listed. Feature families carry their sub-score and formula-specific clock. The unit labels `component_unit`, `channel_unit` and `target_unit` resolve from the component, channel or target kind. `unknown` flags missing documentation. Version 1 identifies this catalog definition. Producer versions and input revisions remain properties of individual facts.

Named observation keys and derived values have different identities. In particular, a stored HRV key alone does not establish a measurement method. The legacy/live daily, morning raw/cached, fixed-clock baseline, actual sleep interval, workout and post-workout projections each have a separate contract. The legacy daily score retains the provenance of the writer that produced it. Unknown method observations stay in compatibility aggregations wherever the Go writer includes them.

The Go server embeds `healthkit_ingress` from this catalog. The Apple Health XML parser resolves the exact HealthKit type identifier before staging. The live storage adapter uses the same compatibility aliases and conversions, including for existing wire clients. Original source labels and the distinction between archived samples and client-generated hourly aggregates remain intact. The other inventory-only, analysis-only and dormant definitions document existing producers or their declared target shapes.

Health Sync uses the Swift projection of the catalog for live quantities and events. The app owns its SDK query options, including the concrete `HKUnit` used for each identifier. After editing the shared catalog, regenerate the iOS source and check both checkouts together:

```bash
python3 experiments/dbt_metrics/scripts/check_semantic_catalog.py \
  --write-swift-ingress ../health-sync/health-sync/HealthKitIngress.generated.swift
python3 experiments/dbt_metrics/scripts/check_semantic_catalog.py \
  --swift ../health-sync/health-sync/HealthKitManager.swift
```

The checker verifies matching identifiers, keys, units and granularities and detects stale Swift output. Unsupported quantity IDs retain a distinct fallback key when it is available. Reserved fallback keys cannot map to an existing observation. For stair speed and physical effort the archive must state the unit explicitly. A recognized stair speed in feet per second converts to metres per second. Physical effort currently requires the documented `kcal/hr·kg` label. Category events accept the specific SDK values defined for their type.

## Querying the catalog and nightly metrics

Build the opt-in models against a populated, isolated shadow target with the existing dbt environment. Supply the same project, profile and target to every command. Set the two variables shown below for the build. The default pilot keeps the shadow disabled.

```sh
cd experiments/dbt_metrics
.venv/bin/dbt build --vars '{semantic_shadow: true, sleep_duration_reference: true}' \
  --select 'semantic_*' sleep_duration_reference_v1
```

The model metadata in `target/manifest.json` carries the complete registry. The view `semantic_metric_catalog_v1` exposes one row per identifier, including the definition inherited from its family. A populated `dbt_binding` names the value columns, model and applicable row filters. Definitions owned solely by Go, an adapter or a presentation component remain discoverable with a null binding. In the configured output schema:

```sql
select metric_id, definition->'units' as units, dbt_binding
from semantic_metric_catalog_v1
where metric_id in ('sleep_comparison.duration_minutes', 'main_sleep_internal_awakenings_v1');
```

The native semantic model `main_night` exposes ten simple metrics over per-night duration, reference and difference, five stage durations, internal awakenings and quality. Each metric links to its catalog ID through `config.meta.catalog_id`. Grouping by wake day returns the dated observation. Grouping across dates averages observed nightly values for the requested field within the profile. An unobserved stage stays null and measured zero contributes to the average. `reference_minutes` already contains each night's historical reference.

For a self-hosted PostgreSQL target, install the optional compatible CLI with `.venv/bin/pip install -c requirements.lock dbt-metricflow==0.15.0`. After building, run:

```sh
.venv/bin/mf query --metrics mean_nightly_duration_minutes,mean_nightly_quality_score_pct \
  --group-by metric_time__day --order metric_time__day
```

The dbt build checks bound columns and the declared reference window and eligibility threshold against the shared application calculation. The shadow driver also compares every resolved catalog definition with PostgreSQL and checks each native metric against its bound field for every evaluation date. Edits to descriptive text appear in the compiled catalog. Changes to formula behavior require an executable model change and its parity checks.
