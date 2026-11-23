{{ config(enabled=var('briefing_components_shadow', false), materialized='view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

select c.profile,c.capture,item.ordinality,s.*
from ({{ today_metric_captures_v1() }}) c
cross join lateral jsonb_array_elements(nullif(c.inputs#>'{decision,illness_suspicion,signals}','null'))
    with ordinality item(record,ordinality)
cross join lateral jsonb_to_record(item.record) s(
    metric text,kind text,role text,category text,direction text,strength text,status text,
    contributes bool,value float8,baseline float8,delta_abs float8,z_score float8,method text
)
