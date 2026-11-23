{{ config(enabled=var('briefing_components_shadow', false), materialized='view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

select c.profile,c.capture,kv.key as component,kv.value->>'evaluated_date' as evaluated_date,
       kv.value->>'source_date' as source_date,(kv.value->>'value')::float8 as value,
       coalesce((kv.value->>'present')::boolean,false) as present,
       coalesce(kv.value->>'freshness','') as freshness,
       coalesce((kv.value->>'sample_count')::int,0) as sample_count,
       coalesce(kv.value->>'confidence','') as confidence,
       coalesce(kv.value->>'missing_reason','') as missing_reason
from ({{ today_metric_captures_v1() }}) c
cross join lateral jsonb_each(coalesce(nullif(c.inputs#>'{raw,ReadinessEvidence}', 'null'::jsonb),'{}')) kv
where kv.key in ('HRV','RHR','OvernightHR','SleepDuration','SleepQuality',
                 'SleepDeep','SleepREM','SleepCore','SleepAwake','Respiratory')
  and jsonb_typeof(kv.value)='object'
