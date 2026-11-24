{{ config(enabled=var('preferred_sleep_source_shadow', false) or var('briefing_components_shadow', false) or var('tenant_native_catalog',false), materialized='ephemeral' if var('tenant_native_catalog',false) else 'view', post_hook="alter view {{ this }} set (security_invoker = true)") }}
{% set input_source = 'preferred_sleep_source' if var('preferred_sleep_source_shadow', false) else 'briefing_pilot' %}

{% if var('tenant_native_catalog',false) %}
with captures as (
    select current_user::text as profile,j.decision_key||':'||j.input_hash as capture,
           j.inputs->>'preferred_source' as preferred_source,(j.inputs->>'window_days')::int as days,
           j.inputs->'revisions' as revisions
    from {{ source('source_data','hdbt_decision_inputs') }} j
    where j.metric='sri_window' and j.formula_version=1 and jsonb_typeof(j.inputs->'revisions')='object'
      and not exists(select from jsonb_each_text(j.inputs->'revisions') v
         where not exists(select from {{ source('source_data','hdbt_decision_inputs') }} r
              where r.metric='sri_day' and r.formula_version=1 and r.decision_key=v.key and r.input_hash=v.value
                and jsonb_typeof(r.inputs->'segments')='array'))
), segments as (
    select c.profile,c.capture,c.preferred_source,c.days,s.*
    from captures c cross join lateral jsonb_each_text(c.revisions) v
    join {{ source('source_data','hdbt_decision_inputs') }} r
      on r.metric='sri_day' and r.formula_version=1 and r.decision_key=v.key and r.input_hash=v.value
    cross join lateral jsonb_to_recordset(r.inputs->'segments') s(observed_at timestamp,source text,metric_name text,qty float8)
)
{% else %}
with captures as (select * from {{ source(input_source,'sri_captures') }}), segments as (
    select c.profile, c.capture, c.preferred_source, greatest(7, c.window_days) as days, s.*
    from captures c
    cross join lateral (
        select observed_at, source, metric_name, qty
        from {{ source(input_source,'sri_segments') }} s
        where (s.profile,s.capture) = (c.profile,c.capture)
          and quality = 'ok' and qty > 0
          and observed_at::date >= c.anchor - greatest(7, c.window_days) - 1
          and date_trunc('second',observed_at)::time <> time '00:00'
          and metric_name in ('sleep_total','sleep_deep','sleep_core','sleep_unspecified','sleep_rem')
    ) s
)
{% endif %}
select * from ({{ sleep_regularity_result() }}) result
