{{ config(enabled=var('semantic_shadow', false)) }}

-- Explicit refs make dbt schedule this check after every bound relation.
with relations(model_name, relation) as (
    values
      ('semantic_hrv_after_workout_v1', '{{ ref('semantic_hrv_after_workout_v1') }}'::regclass),
      ('semantic_hrv_daily_v1', '{{ ref('semantic_hrv_daily_v1') }}'::regclass),
      ('semantic_hrv_night_v1', '{{ ref('semantic_hrv_night_v1') }}'::regclass),
      ('semantic_hrv_observation_v1', '{{ ref('semantic_hrv_observation_v1') }}'::regclass),
      ('semantic_hrv_workout_v1', '{{ ref('semantic_hrv_workout_v1') }}'::regclass),
      ('semantic_sleep_quality_v1', '{{ ref('semantic_sleep_quality_v1') }}'::regclass),
      ('semantic_sleep_continuity_v1', '{{ ref('semantic_sleep_continuity_v1') }}'::regclass),
      ('semantic_sleep_day_v1', '{{ ref('semantic_sleep_day_v1') }}'::regclass),
      ('semantic_sleep_duration_comparison_v1', '{{ ref('semantic_sleep_duration_comparison_v1') }}'::regclass),
      ('semantic_sleep_history_v1', '{{ ref('semantic_sleep_history_v1') }}'::regclass),
      ('sleep_duration_reference_v1', '{{ ref('sleep_duration_reference_v1') }}'::regclass)
), bindings as (
    select metric_id, dbt_binding->>'model' as model_name, field
    from {{ ref('semantic_metric_catalog_v1') }}
    cross join lateral (
        select jsonb_array_elements_text(dbt_binding->'columns') as field
        union all
        select jsonb_object_keys(dbt_binding->'filter')
    ) fields
    where dbt_binding->>'model' <> 'shadow_sleep_daily_v1' -- checked by the sleep shadow test
)
select b.metric_id, b.model_name, b.field
from bindings b left join relations r using (model_name)
left join pg_attribute a on a.attrelid=r.relation and a.attname=b.field and a.attnum>0 and not a.attisdropped
where a.attname is null
union all
select metric_id, dbt_binding->>'model', null
from {{ ref('semantic_metric_catalog_v1') }}
where dbt_binding is not null and coalesce(jsonb_array_length(dbt_binding->'columns'),0)=0
