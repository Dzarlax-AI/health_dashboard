# Health metric catalog

`catalog.yml` uses the JSON subset of YAML 1.2. It contains definitions and source declarations only. Run the checker from the repository root with a checkout of the Health Sync client alongside the server.

```bash
python3 experiments/dbt_metrics/scripts/check_semantic_catalog.py
python3 experiments/dbt_metrics/scripts/check_semantic_catalog.py --expand
python3 experiments/dbt_metrics/scripts/check_semantic_catalog.py --swift /path/to/HealthKitManager.swift
```

The observation registry covers the explicit XML type map, sleep categories and special emissions, Health Sync quantity, event and sleep declarations, UI category lists, daily column mappings, SUM rules and named inclusive quality bounds. An XML unit in that map is the importer's default when the file omits the unit. The iOS field records the actual outgoing `serverUnits`, along with the quantity/event type and time resolution. Generic payloads can add keys at runtime. `--inventory /path/to/aggregate.json` compares an offline array of `{"metric_name", "units", "rows"}` objects to those declarations. A unit absent from the declarations needs review even when its spelling resembles a known unit. The checker does not open a database.

Each `family.field` resolves to one definition. Shared properties come from `derived_defaults` and the family, then field overrides apply. The owner and the Go type point to the canonical source declaration. `ignored_fields` records metadata, presentation or nested objects omitted at that level. Nested evidence has its own grain where listed. Feature families carry their sub-score and formula-specific clock. The unit labels `component_unit`, `channel_unit` and `target_unit` resolve from the component, channel or target kind. `unknown` flags missing documentation. Version 1 identifies this catalog definition. Producer versions and input revisions remain properties of individual facts.

Named observation keys and derived values have different identities. In particular, a stored HRV key alone does not establish a measurement method. The legacy/live daily, morning raw/cached, fixed-clock baseline, actual sleep interval, workout and post-workout projections each have a separate contract. The legacy daily score retains the provenance of the writer that produced it. Unknown method observations stay in compatibility aggregations wherever the Go writer includes them.

The public application does not read this catalog. Inventory-only, analysis-only and dormant definitions describe existing producers or their declared target shapes. Declared consumers designate the code path or presentation family that owns the meaning. They do not imply feature enablement, complete profile coverage, publication of a shadow value or clinical validation.
