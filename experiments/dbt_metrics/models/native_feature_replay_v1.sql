{{ config(enabled=var('tenant_native_catalog',false), materialized='ephemeral') }}
with receipts as (
    select decision_key||':'||input_hash as revision,inputs, captured_at as computed_at,
           inputs->>'date' as date,inputs->>'sub_score' as sub_score,
           inputs->>'source_epoch' as source_epoch,(inputs->>'feature_version')::int as feature_version,
           (inputs->>'date')::date as as_of_date,nullif(inputs->>'epoch_start','')::date as epoch_start
    from {{ source('source_data','hdbt_decision_inputs') }}
    where metric='chip_features' and formula_version=1 and jsonb_typeof(inputs->'primary_days')='array'
      and (inputs->>'sub_score',(inputs->>'feature_version')::int) in
          (('recovery_stability',3),('passive_efficiency',1),('acute_risk',1),('chronic_load',2))
), streams as (
    select r.revision||':'||k as case_id,r.revision,k,r.as_of_date,r.epoch_start,r.inputs->k as days
    from receipts r cross join (values ('primary_days'),('rhr_days')) s(k) where jsonb_typeof(r.inputs->k)='array'
), windows as ({{ chip_feature_windows_v1('streams') }}),
rhr_dates as materialized (
    select distinct r.revision,day->>'date' as day
    from receipts r cross join lateral jsonb_array_elements(r.inputs->'rhr_days') day
    where r.sub_score='acute_risk'
), pairs as (
    select r.revision,count(*) as paired
    from receipts r cross join lateral jsonb_array_elements(r.inputs->'primary_days') a
    left join rhr_dates b on b.revision=r.revision and b.day=a->>'date'
    where r.sub_score in ('chronic_load','acute_risk')
      and (a->>'date')::date between greatest(r.epoch_start,r.as_of_date-180) and r.as_of_date
      and (r.sub_score='chronic_load' or b.day is not null)
    group by r.revision
), sleep_values as (
    select r.revision,r.as_of_date,r.epoch_start,s."Date"::date as day,s."Total" as total,s."Awake" as awake,
           s."Deep" as deep,s."REM" as rem,s."Core" as core,s."Unspecified" as unspecified
    from receipts r cross join lateral jsonb_to_recordset(r.inputs->'sleep') s(
      "Date" text,"Total" float8,"Awake" float8,"Deep" float8,"REM" float8,"Core" float8,"Unspecified" float8)
    where r.sub_score='recovery_stability'
), assessments as (
    select s.*,s.capture_class as class,s.capture_confidence as confidence,s.capture_reason as reason,s.capture_low as low
    from ({{ recovery_sleep_assessment_v1('sleep_values') }}) s
), sleep_inputs as (
    select revision as case_id,as_of_date,epoch_start,jsonb_agg(to_jsonb(s) order by day) as days
    from assessments s group by revision,as_of_date,epoch_start
), sleep_stats as ({{ chip_recovery_sleep_v1('sleep_inputs') }})
select r.date,r.sub_score,r.source_epoch,r.feature_version,r.computed_at,r.revision,
       a.today as prev_efficiency,a.mean7 as sleep_eff_mean_7d,a.adaptive_value as sleep_eff_ewma_45d,a.slow_value as sleep_eff_ewma_180d,
       s.debt_hours as sleep_debt_7d_hours,d.class as sleep_capture_class,d.confidence::float8 as sleep_capture_confidence,
       d.reason as sleep_capture_reason,d.low as sleep_capture_low,s.class_counts as sleep_capture_class_counts_7d,
       s.low_days as sleep_capture_low_days_7d,s.mean_confidence as sleep_capture_mean_confidence_7d,
       a.n7 as eligible_count_7d,a.adaptive_n as eligible_count_45d,a.slow_n as eligible_count_180d,
       (case when r.sub_score='chronic_load' then a.n45 else a.adaptive_n end)>=22 as warmup_complete_45d,
       (case when r.sub_score='chronic_load' then a.n180 else a.slow_n end)>=60 as warmup_complete_180d,
       a.today as prev_walking_hr,a.mean7 as walking_hr_mean_7d,a.mean30 as walking_hr_mean_30d,
       a.adaptive_value as walking_hr_ewma_45d,a.slow_value as walking_hr_ewma_180d,
       a.delta_adaptive as walking_hr_delta_vs_ewma_45d,a.delta_slow as walking_hr_delta_vs_ewma_180d,
       a.today as hrv_today,b.today as rhr_today,a.mean45 as hrv_mean_45d,a.sd45 as hrv_sd_45d,
       b.mean45 as rhr_mean_45d,b.sd45 as rhr_sd_45d,a.z45 as hrv_z_today,b.z45 as rhr_z_today,
       coalesce(p.paired,0) as paired_count_to_t,coalesce(p.paired,0)>=30 as warmup_met,
       a.n45 as hrv_eligible_count_45d,b.n45 as rhr_eligible_count_45d,
       a.today as recovery_3d_today,a.mean45 as recovery_3d_baseline_mean_45d,
       a.sd45 as recovery_3d_baseline_sd_45d,a.z45 as recovery_3d_z_today,
       a.n45 as recovery_eligible_count_45d,a.n180 as recovery_eligible_count_180d
from receipts r join windows a on a.case_id=r.revision||':primary_days'
left join windows b on b.case_id=r.revision||':rhr_days'
left join pairs p using(revision)
left join sleep_stats s on s.case_id=r.revision
left join assessments d on d.revision=r.revision and d.day=r.as_of_date
