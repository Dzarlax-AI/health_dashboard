{% macro chip_targets_v1(inputs) %}
-- Every forward date must be represented, even when its source is missing.
with windows as (
    select i.*, i.sub_score in ('recovery_stability','passive_efficiency') as continuous,
           i.sub_score = 'acute_risk' as acute
    from {{ inputs }} i
), operands as (
    select i.*, d, (d->>'value')::float8 as value, d->>'reason' as reason,
           case d->>'reason' when 'ok' then 0 when 'ok_awake_structural_zero' then 1
                when 'coarse_only_source' then 2 when 'no_walking_hr' then 2
                when 'missing_awake_unknown' then 3 when 'walking_hr_out_of_range' then 3
                when 'sleep_total_out_of_range' then 4 else 5 end as priority,
           offset_day
    from windows i cross join lateral generate_series(1,case when target_kind='daily_point' then 1 when continuous or acute then 3 else 14 end) offset_day
    left join lateral jsonb_array_elements(i.days) d on (d->>'date')::date = i.target_date + offset_day
), summary as (
    select case_id, sub_score, target_kind, target_date, latest_sleep, continuous, acute, paired_count, breach_threshold,
           count(value) filter (where (d->>'eligible')::bool) as eligible_days,
           avg(value) filter (where (d->>'eligible')::bool) as mean_value,
           (array_agg(coalesce(reason, case when sub_score='passive_efficiency' then 'no_walking_hr'
                                           else 'sleep_total_out_of_range' end) order by
              case when d is null and sub_score='passive_efficiency' then 2
                   when d is null then 4 else priority end desc, offset_day))[1] as blocking_reason,
           count(*) filter (where case when target_kind='chronic_acute_density' then (d->>'acute_observed')::bool else (d->>'observed')::bool end) as observed_days,
           count(*) filter (where case when target_kind='chronic_acute_density' then (d->>'acute_observed')::bool and (d->>'acute_or')::int=1 else (d->>'z')::float8 < -1 end) as breaches,
           bool_or(case when target_kind='event_strict_t1_t3' then (d->>'hrv_z')::float8 <= -1.5 and (d->>'rhr_z')::float8 >= 1.5 else (d->>'hrv_z')::float8 <= -1.5 or (d->>'rhr_z')::float8 >= 1.5 end) as acute_event
    from operands group by case_id, sub_score, target_kind, target_date, latest_sleep, continuous, acute, paired_count, breach_threshold
), decisions as (
    select *, case when target_kind='daily_point' then blocking_reason
                   when target_kind='rolling_3d_candidate_2of3' and (latest_sleep is null or latest_sleep < target_date+3) then 'sleep_data_missing'
                   when continuous and eligible_days < case when target_kind='rolling_3d_candidate_2of3' then 2 else 3 end then blocking_reason
                   when continuous then 'ok'
                   when paired_count < 30 then 'baseline_warmup'
                   when acute and observed_days < 3 then 'event_window_data_missing'
                   when acute or breaches >= breach_threshold or breaches + 14 - observed_days < breach_threshold then 'ok'
                   else 'event_window_data_missing' end as reason
    from summary
)
select case_id, sub_score, target_kind, reason, reason in ('ok','ok_awake_structural_zero') as eligible,
       case when reason not in ('ok','ok_awake_structural_zero') then null when continuous then mean_value
            when acute then coalesce(acute_event,false)::int::float8
            else (breaches >= breach_threshold)::int::float8 end as target_value
from decisions
{% endmacro %}
