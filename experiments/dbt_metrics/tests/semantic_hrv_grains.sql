{{ config(enabled=var('semantic_shadow', false)) }}
-- Zero result verifies row identities plus preservation of unavailable dates.
with invalid as (
    select date from {{ ref('semantic_hrv_daily_v1') }} group by date having count(*)<>1
    union all
    select c.date from {{ source('source_data','calendar') }} c
    where not exists(select from {{ ref('semantic_hrv_daily_v1') }} f where f.date=c.date)
       or not exists(select from {{ ref('semantic_hrv_night_v1') }} n where n.wake_date=c.date)
    union all
    select wake_date from {{ ref('semantic_hrv_night_v1') }} group by wake_date,source having count(*)<>1
    union all
    select id from {{ ref('semantic_hrv_workout_windows_v1') }} group by id,period,source having count(*)<>1
    union all
    select id from {{ ref('semantic_hrv_workout_windows_v1') }} where
        (interval_points=0)<>(interval_mean_ms is null)
        or (timestamp_points=0)<>(timestamp_mean_ms is null)
        or interval_points+timestamp_points+unknown_method_points+other_method_points+imprecise_points
           +ambiguous_points+invalid_points+unsupported_unit_points+boundary_points+active_workout_points+duplicate_points<>candidates
)
select * from invalid
