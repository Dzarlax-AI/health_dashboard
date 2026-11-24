{{ config(enabled=var('complex_sleep_parity',false) or var('tenant_native_catalog',false),materialized='ephemeral' if var('tenant_native_catalog',false) else 'view',post_hook="alter view {{ this }} set (security_invoker = true)") }}

with {% if var('tenant_native_catalog',false) %}
    balance_capture as (
      select current_user::text as profile,j.decision_key||':'||j.input_hash as capture,
             (inputs->>'wake_date')::date as wake_date,inputs->>'zone' as zone,inputs
      from {{ source('source_data','hdbt_decision_inputs') }} j where metric='sleep_balance' and formula_version=1
        and inputs ?& array['wake_date','zone','goals','coverage','episodes']
    ), balance_goal as (
      select profile,capture,g."EffectiveDate" as effective_date,g."Hours" as hours from balance_capture c
      cross join lateral jsonb_to_recordset(nullif(inputs->'goals','null')) g("EffectiveDate" date,"Hours" float8)
    ), balance_coverage as (
      select profile,capture,k."WakeDate" as period_day,k."CaptureState" as state from balance_capture c
      cross join lateral jsonb_to_recordset(nullif(inputs->'coverage','null')) k("WakeDate" date,"CaptureState" text)
    ), balance_episode as (
      select profile,capture,e."ID" as id,e."Start" as start_at,e."End" as end_at,e."DurationAssessment" as assessment
      from balance_capture c cross join lateral jsonb_to_recordset(nullif(inputs->'episodes','null'))
      e("ID" text,"Start" timestamptz,"End" timestamptz,"DurationAssessment" text)
    ), {% endif %}period_rows as (
    {{ sleep_balance_period_v1(
        'balance_capture' if var('tenant_native_catalog',false) else source('complex_sleep','balance_capture'),
        'balance_goal' if var('tenant_native_catalog',false) else source('complex_sleep','balance_goal'),
        'balance_coverage' if var('tenant_native_catalog',false) else source('complex_sleep','balance_coverage'),
        'balance_episode' if var('tenant_native_catalog',false) else source('complex_sleep','balance_episode')
    ) }}
)
select * from period_rows
