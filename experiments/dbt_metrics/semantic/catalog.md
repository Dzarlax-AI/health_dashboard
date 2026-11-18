# Health metric catalog

The [native dbt model properties](../models/health_metric_catalog.yml) hold the authoritative registry in `config.meta.health_catalog`. The checker reads those same properties. Run the checker from the repository root with a checkout of the Health Sync client alongside the server.

```bash
python3 experiments/dbt_metrics/scripts/check_semantic_catalog.py
python3 experiments/dbt_metrics/scripts/check_semantic_catalog.py --expand
python3 experiments/dbt_metrics/scripts/check_semantic_catalog.py --swift /path/to/HealthKitManager.swift
```

The observation registry covers the explicit XML type map, sleep categories and special emissions, Health Sync quantity, event and sleep declarations, UI category lists, daily column mappings, SUM rules and named inclusive quality bounds. An XML unit in that map is the importer's default when the file omits the unit. The iOS field records the actual outgoing `serverUnits`, along with the quantity/event type and time resolution. Generic payloads can add keys at runtime. `--inventory /path/to/aggregate.json` compares an offline array of `{"metric_name", "units", "rows"}` objects to those declarations. A unit absent from the declarations needs review even when its spelling resembles a known unit. The checker does not open a database.

Each `family.field` resolves to one definition. Shared properties come from `derived_defaults` and the family, then field overrides apply. The owner and the Go type point to the canonical source declaration. `ignored_fields` records metadata, presentation or nested objects omitted at that level. Nested evidence has its own grain where listed. Feature families carry their sub-score and formula-specific clock. The unit labels `component_unit`, `channel_unit` and `target_unit` resolve from the component, channel or target kind. `unknown` flags missing documentation. Version 1 identifies this catalog definition. Producer versions and input revisions remain properties of individual facts.

Named observation keys and derived values have different identities. In particular, a stored HRV key alone does not establish a measurement method. The legacy/live daily, morning raw/cached, fixed-clock baseline, actual sleep interval, workout and post-workout projections each have a separate contract. The legacy daily score retains the provenance of the writer that produced it. Unknown method observations stay in compatibility aggregations wherever the Go writer includes them.

The public application does not read this catalog. Inventory-only, analysis-only and dormant definitions describe existing producers or their declared target shapes. Declared consumers designate the code path or presentation family that owns the meaning. They do not imply feature enablement, complete profile coverage, publication of a shadow value or clinical validation.

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
