{% set native = var('tenant_native_catalog', false) %}
{{ config(materialized='ephemeral' if native else 'view', enabled=native or var('workout_shadow', false),
          post_hook='alter view {{ this }} set (security_invoker=true)') }}

{% if native %}
select current_user::text as profile, t.*, row_number() over (order by t.count desc,t.name) as sort_order
from ({{ workout_type_totals(source('archive_records','workouts'), 'true') }}) t
{% else %}
select p.profile, t.*, row_number() over (partition by p.profile order by t.count desc,t.name) as sort_order
from (select distinct profile from {{ source('workout_capture','sessions') }}) p
cross join lateral (
    {{ workout_type_totals(source('workout_capture','sessions'), 's.profile=p.profile') }}
) t
{% endif %}
