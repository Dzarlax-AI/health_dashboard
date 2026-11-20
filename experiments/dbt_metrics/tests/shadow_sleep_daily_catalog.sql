{{ config(enabled=var('sleep_daily_shadow', false) and not var('sleep_daily_runtime_sql', false)) }}

WITH expected(metric_id, field) AS (
    VALUES ('shadow_sleep_daily_duration_v1','sleep_seconds'),
           ('shadow_sleep_daily_stages_v1','deep_seconds'),
           ('shadow_sleep_daily_stages_v1','rem_seconds'),
           ('shadow_sleep_daily_stages_v1','core_seconds'),
           ('shadow_sleep_daily_stages_v1','unspecified_seconds'),
           ('shadow_sleep_daily_stages_v1','awake_seconds'),
           ('shadow_sleep_daily_stage_conflict_v1','stage_conflict')
), bindings AS (
    SELECT c.metric_id, field
    FROM {{ ref('semantic_metric_catalog_v1') }} c
    CROSS JOIN LATERAL jsonb_array_elements_text(c.dbt_binding->'columns') AS fields(field)
    WHERE c.dbt_binding->>'model'='shadow_sleep_daily_v1'
), mismatches AS (
    (SELECT * FROM expected EXCEPT SELECT * FROM bindings)
    UNION ALL
    (SELECT * FROM bindings EXCEPT SELECT * FROM expected)
)
SELECT * FROM mismatches
UNION ALL
SELECT b.* FROM bindings b
LEFT JOIN pg_attribute a ON a.attrelid='{{ ref('shadow_sleep_daily_v1') }}'::regclass
    AND a.attname=b.field AND a.attnum>0 AND NOT a.attisdropped
WHERE a.attname IS NULL
