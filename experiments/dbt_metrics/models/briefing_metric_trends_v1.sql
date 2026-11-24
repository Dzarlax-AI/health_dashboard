{{ config(enabled=var('briefing_components_shadow', false) or var('tenant_native_catalog', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view', post_hook="alter view {{ this }} set (security_invoker = true)") }}

with series as (
  select c.profile,c.capture,s.metric,x.ordinal,x.ms::float8 as value
  from ({{ today_metric_captures_v1('trend') }}) c
  cross join lateral (values
    ('step_count','Steps'),('resting_heart_rate','RHR'),('heart_rate_variability','HRV'),
    ('respiratory_rate','Resp'),
    (case when jsonb_array_length(coalesce(nullif(inputs->'raw'->'NightSleep','null'),'[]'))>0 then 'night_sleep_total' else 'sleep_total' end,
     case when jsonb_array_length(coalesce(nullif(inputs->'raw'->'NightSleep','null'),'[]'))>0 then 'NightSleep' else 'Sleep' end)
  ) s(metric,key)
  cross join lateral jsonb_array_elements_text(coalesce(nullif(inputs->'raw'->s.key,'null'),'[]')) with ordinality x(ms,ordinal)
)
select * from ({{ briefing_metric_trends() }}) results
