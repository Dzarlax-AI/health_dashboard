{{ config(enabled=var('semantic_shadow', false), materialized='view', post_hook="alter view {{ this }} set (security_invoker = true)") }}

select * from {{ ref('semantic_hrv_workout_windows_v1') }} where period='after_30m'
