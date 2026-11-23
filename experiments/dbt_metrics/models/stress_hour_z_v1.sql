{{ config(enabled=var('briefing_components_shadow', false), materialized='view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

select c.profile, c.capture, h.hour_start, h.coverage_ok,
       case when not h.coverage_ok or h.coverage_ok is null then null
            when h.hour_hr is null or h.baseline is null or h.sd is null then null
            when h.sd <= 0 or not (h.sd < 'Infinity'::float8 and
                 h.hour_hr > '-Infinity'::float8 and h.hour_hr < 'Infinity'::float8 and
                 h.baseline > '-Infinity'::float8 and h.baseline < 'Infinity'::float8) then 0
            else (h.hour_hr - h.baseline) / h.sd end as hour_z
from {{ source('briefing_pilot', 'captured_inputs') }} c
cross join lateral jsonb_to_recordset(nullif(c.inputs->'stress_hours','null'::jsonb)) as h(
    hour_start timestamptz, coverage_ok boolean, hour_hr float8, baseline float8, sd float8)
