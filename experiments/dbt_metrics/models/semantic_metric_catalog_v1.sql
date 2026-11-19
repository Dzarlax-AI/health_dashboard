{{ config(enabled=var('semantic_shadow', false) or var('sleep_daily_shadow', false), materialized='view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

-- dbt reads the registry from this model's native properties at compile time.
with registry as (
    select '{{ tojson(model.config.meta["health_catalog"] if execute else {}) | replace("'", "''") }}'::jsonb as spec
), definitions as (
    select obs.key as metric_id,
           spec->'observation_defaults' || jsonb_build_object(
               'source_keys', jsonb_build_array(obs.key), 'evidence', obs.value,
               'units', coalesce((
                   select jsonb_agg(unit order by unit) from (
                       select distinct unit from (values (obs.value#>>'{xml,1}'), (obs.value#>>'{ios,1}'),
                           (obs.value->>'xml_special_unit'), (case when obs.value ? 'xml_categories' then 'hr' end)
                       ) declared(unit) where unit is not null
                   ) units
               ), '["unknown"]'::jsonb),
               'quality', coalesce(obs.value->'inclusive_range', '"no named range; retain source quality"'::jsonb),
               'consumers', case when obs.value ? 'ui_group' then '["Metrics"]'::jsonb else '["unverified"]'::jsonb end,
               'method', 'channel-dependent; see adapter evidence',
               'provenance', case when obs.value ?| array['xml','ios','xml_special_unit','xml_categories']
                                  then 'named adapter' else 'producer unconfirmed' end
           ) as definition
    from registry cross join lateral jsonb_each(spec->'observations') obs
    union all
    select family.key || '.' || field.key, spec->'derived_defaults' ||
           (family.value - array['fields','ignored_fields','go_type']) || field.value
    from registry cross join lateral jsonb_each(spec->'families') family
    cross join lateral jsonb_each(family.value->'fields') field
    union all
    select contract.key, spec->'derived_defaults' || contract.value
    from registry cross join lateral jsonb_each(spec->'contracts') contract
), bindings as (
    select binding.key as metric_id, binding.value as dbt_binding
    from registry cross join lateral jsonb_each(spec->'dbt_bindings') binding
)
-- An orphan binding stays visible so the not-null definition test fails.
select metric_id, definition, dbt_binding
from definitions full join bindings using (metric_id)
