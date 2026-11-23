-- Retrospective binary calibration dates come from retained dated pairs.
-- No original operator-requested as-of date is inferred from a write clock.
create index on source_data.naive_baselines(sub_score,target_kind,date);
create index on source_data.target_snapshots(sub_score,target_kind,date);
create index on source_data.feature_snapshots(sub_score,date);
create table replay.chip_calibration_input as
select row_number() over(order by sub_score,target_kind,source_epoch)::text as case_id,
       sub_score,target_kind,source_epoch as epoch,max(date)::date as as_of_date
from source_data.naive_baselines where baseline_kind='event_base_rate'
and (sub_score,target_kind) in (('acute_risk','event_t1_t3'),('chronic_load','chronic_label'))
group by sub_score,target_kind,source_epoch;
create table replay.chip_pairs as
select c.case_id,n.date::date as day,n.source_epoch as prediction_epoch,t.source_epoch as label_epoch,
       n.baseline_kind,n.predicted_value as predicted,t.target_value as label,t.eligible
from replay.chip_calibration_input c join source_data.naive_baselines n using(sub_score,target_kind)
join source_data.target_snapshots t using(sub_score,target_kind,date)
where n.date between (c.as_of_date-179)::text and c.as_of_date::text;

create table replay.chip_target_input as
with stored as (
    select t.*,row_number() over(order by t.date,t.sub_score,t.target_kind)::text as case_id,
           t.data_coverage as cov,f.features->>'paired_count_to_t' as chronic_paired
    from source_data.target_snapshots t left join source_data.feature_snapshots f using(date,sub_score)
    where t.sub_score in ('acute_risk','chronic_load') or
          (t.sub_score='passive_efficiency' and t.target_kind='daily_point')
)
select case_id,sub_score,target_kind,date::date as target_date,null::date as latest_sleep,
       coalesce((cov->>'paired_count')::int,chronic_paired::int) as paired_count,
       (case when target_kind='chronic_acute_density' then cov->>'acute_density_threshold'
             else cov->>'breach_threshold' end)::int as breach_threshold,
       case when sub_score='passive_efficiency' then jsonb_build_array(jsonb_build_object(
                'date',(date::date+1)::text,'eligible',coalesce((cov->>'observed_bpm')::float8 between 50 and 180,false),
                'value',case when (cov->>'observed_bpm')::float8 between 50 and 180 then (cov->>'observed_bpm')::float8 end,
                'reason',case when cov->>'observed_bpm' is null then 'no_walking_hr'
                         when (cov->>'observed_bpm')::float8 between 50 and 180 then 'ok' else 'walking_hr_out_of_range' end))
            else coalesce((select jsonb_agg(d||jsonb_build_object('observed',
                coalesce((d->>'recovery_observed')::bool,(d->>'observed')::bool,
                d->>'hrv' is not null or d->>'rhr' is not null))) from jsonb_array_elements(cov->'per_day') d),'[]') end as days,
       target_value,eligible,eligibility_reason,source_epoch
from stored where
    (sub_score='passive_efficiency' and (cov ? 'observed_bpm' or cov ? 'target_date'))
    or (sub_score='acute_risk' and cov ? 'paired_count' and (cov ? 'per_day' or cov ? 'missing_window_days' or (cov->>'paired_count')::int<30))
    or (sub_score='chronic_load' and ((cov ? 'paired_count' and (cov->>'paired_count')::int<30)
       or (cov ? 'per_day' and chronic_paired is not null)));

-- Event base rates use only preceding labels, including the current source epoch.
-- The baseline references persisted raw labels for an explicitly retrospective replay.
create table replay.chip_baseline_input as
select row_number() over(order by n.sub_score,n.target_kind,n.date)::text as case_id,
       n.sub_score,n.target_kind,n.date::date as as_of_date,e.start_date::date as epoch_start,
       coalesce((select jsonb_agg(jsonb_build_object('date',t.date,'eligible',t.eligible,'value',t.target_value))
          from source_data.target_snapshots t where (t.sub_score,t.target_kind)=(n.sub_score,n.target_kind)
          and t.date between (n.date::date-90)::text and (n.date::date-1)::text),'[]') as days,
       n.predicted_value,coalesce(n.reason,'') as expected_reason
from source_data.naive_baselines n join source_data.source_epochs e on e.epoch_id=n.source_epoch
where n.baseline_kind='event_base_rate';
