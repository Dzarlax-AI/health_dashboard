{{ config(enabled=var('semantic_shadow', false), materialized='view', post_hook='alter view {{ this }} set (security_invoker=true)') }}

-- One row per calendar date. Leave per-device means at their natural grain.
with nights as (
    select wake_date, sum(interval_points)::int as measured_points,
           sum(timestamp_points)::int as timestamp_points, count(mean_ms)::int as sources_with_sdnn,
           encode(sha256(convert_to(jsonb_agg(jsonb_build_array(source,observations_version,reason)
               order by source)::text,'UTF8')), 'hex') as hrv_observations_version
    from {{ ref('semantic_hrv_night_v1') }} group by wake_date
)
select s.wake_date, s.status as sleep_comparison_status, s.stage_status, s.interval_count,
       s.input_version as sleep_version, 'hrv_main_sleep_sdnn_v1'::text as hrv_definition,
       n.hrv_observations_version, n.measured_points, n.timestamp_points, n.sources_with_sdnn,
       h.sdnn_points as same_day_sdnn_points, h.n_points as same_day_hrv_points
from {{ ref('semantic_sleep_day_v1') }} s
join nights n using (wake_date)
join {{ ref('semantic_hrv_daily_v1') }} h on h.date=s.wake_date
