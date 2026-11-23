{{ config(materialized='view', enabled=var('workout_shadow', false),
          post_hook='alter view {{ this }} set (security_invoker=true)') }}

select p.profile, p.period_id, totals.*
from {{ source('workout_capture','periods') }} p
cross join lateral (
    {{ workout_period_totals(source('workout_capture','sessions'),
        "s.profile=p.profile and s.start_time between p.from_time and p.to_time
         and (p.name_filter='' or p.name_filter=s.name)") }}
) totals
