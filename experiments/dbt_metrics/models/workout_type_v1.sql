{{ config(materialized='view', enabled=var('workout_shadow', false),
          post_hook='alter view {{ this }} set (security_invoker=true)') }}

select p.profile, t.*, row_number() over (partition by p.profile order by t.count desc,t.name) as sort_order
from (select distinct profile from {{ source('workout_capture','sessions') }}) p
cross join lateral (
    {{ workout_type_totals(source('workout_capture','sessions'), 's.profile=p.profile') }}
) t
