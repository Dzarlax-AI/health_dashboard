{% set native = var('tenant_native_catalog', false) %}
{{ config(materialized='ephemeral' if native else 'view', enabled=native or var('workout_shadow', false),
          post_hook='alter view {{ this }} set (security_invoker=true)') }}

{% if native %}
with periods as (
    select input_hash, inputs->'sessions' as sessions from {{ adapter.quote(target.schema) }}.hdbt_decision_inputs
    where metric='workout_period' and formula_version=1 and jsonb_typeof(inputs->'sessions')='array'
)
select current_user::text as profile,p.input_hash as period_id,t.*
from periods p cross join lateral (
    with sessions as materialized (
        select s.* from jsonb_array_elements(p.sessions) with ordinality a(ref,ord)
        join {{ adapter.quote(target.schema) }}.hdbt_decision_inputs j
          on j.metric='workout_session' and j.formula_version=1
         and j.decision_key=a.ref->>0 and j.input_hash=a.ref->>1
        cross join lateral jsonb_to_record(j.inputs) s(duration_sec float8,distance_km float8,energy_kcal float8,
            avg_hr_bpm float8,max_hr_bpm float8,hr_z1_sec integer,hr_z2_sec integer,
            hr_z3_sec integer,hr_z4_sec integer,hr_z5_sec integer)
        order by a.ord
    )
    {{ workout_period_totals('sessions','true') }}
) t where t.count=jsonb_array_length(p.sessions)
{% else %}
select p.profile, p.period_id, totals.*
from {{ source('workout_capture','periods') }} p
cross join lateral (
    {{ workout_period_totals(source('workout_capture','sessions'),
        "s.profile=p.profile and s.start_time between p.from_time and p.to_time
         and (p.name_filter='' or p.name_filter=s.name)") }}
) totals
{% endif %}
