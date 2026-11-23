{{ config(enabled=(var('readiness_hrv_shadow', false) or var('readiness_score_shadow', false)), materialized='view', post_hook="alter view {{ this }} set (security_invoker = true)") }}

-- Inputs are immutable captures of dated candidates available to a Go reader.
-- A new correction gets a new snapshot_id; current rows cannot recover an old morning.
with candidates as (
    select snapshot_id, profile, target_date::date as target_date, reader, reader_path,
           snapshot_state, evidence_present, observed_date::date as observed_date,
           raw_ms::double precision as raw_ms, cached_ms::double precision as cached_ms,
           coalesce(repair_pending,false) as repair_pending,
           coalesce(cache_row_eligible,false) as cache_row_eligible
    from {{ source('source_data', 'readiness_hrv_snapshot_inputs') }}
), snapshots as (
    select snapshot_id, profile, target_date, reader,
           min(reader_path) as reader_path, min(snapshot_state) as snapshot_state,
           bool_and(evidence_present) as evidence_present,
           snapshot_id is not null and profile is not null and target_date is not null
             and count(reader_path)=count(*) and count(snapshot_state)=count(*)
             and count(evidence_present)=count(*)
             and count(distinct reader_path)=1 and count(distinct snapshot_state)=1
             and count(distinct evidence_present)=1
             and count(observed_date)=count(distinct observed_date)
             and bool_and((reader='history' and reader_path='history')
                       or (reader='morning' and reader_path in ('morning_warm','morning_cold')))
             and bool_and(snapshot_state in ('captured','unrecoverable')) as metadata_valid
    from candidates group by snapshot_id,profile,target_date,reader
), candidate_values as (
    select *, case when reader_path in ('history','morning_cold') then raw_ms
                   when observed_date=target_date then
                     coalesce(raw_ms,case when not repair_pending then cached_ms end)
                   when not repair_pending then cached_ms end as hrv_ms
    from candidates c
    where snapshot_state='captured'
      and exists (select 1 from snapshots s where s.snapshot_id=c.snapshot_id
          and s.profile=c.profile and s.target_date=c.target_date and s.reader=c.reader
          and s.metadata_valid)
      and observed_date<=target_date
      and observed_date>=target_date-case when reader_path='morning_cold' then 30 else 29 end
      and (reader_path<>'morning_warm' or cache_row_eligible
           or (observed_date=target_date and raw_ms is not null))
), limited as (
    -- Warm's cache row limit precedes positive filtering; raw fallback limits
    -- nonempty HRV dates. Captures preserve the path actually taken.
    select *, row_number() over(partition by snapshot_id,profile,target_date,reader
                                order by observed_date desc) as candidate_position
    from candidate_values
    where reader_path='morning_warm' or hrv_ms>0
), selected as (
    select *, (row_number() over(partition by snapshot_id,profile,target_date,reader
                                order by observed_date desc)-1)::integer as ordinal
    from limited l where candidate_position<=30 and hrv_ms>0
      and (reader_path<>'history' or exists (
          select 1 from candidate_values anchor
          where anchor.snapshot_id=l.snapshot_id and anchor.profile=l.profile
            and anchor.target_date=l.target_date and anchor.reader=l.reader
            and anchor.observed_date=l.target_date and anchor.hrv_ms>0
      ))
), captured as (
    select s.*, p.ordinal, p.observed_date, p.hrv_ms
    from snapshots s left join selected p
      on p.snapshot_id=s.snapshot_id and p.profile=s.profile
     and p.target_date=s.target_date and p.reader=s.reader
), grouped as (
    select snapshot_id, profile, target_date, reader,
           min(reader_path) as reader_path,
           min(snapshot_state) as snapshot_state,
           bool_and(evidence_present) as evidence_present,
           count(ordinal)::integer as n_values,
           coalesce(jsonb_agg(ordinal order by ordinal) filter (where ordinal is not null), '[]'::jsonb) as input_ordinals,
           coalesce(jsonb_agg(observed_date order by ordinal) filter (where ordinal is not null), '[]'::jsonb) as input_dates,
           coalesce(jsonb_agg(hrv_ms order by ordinal) filter (where ordinal is not null), '[]'::jsonb) as input_values,
           max(hrv_ms) filter (where ordinal=0) as current_ms,
           max(observed_date) filter (where ordinal=0) as current_observed_date,
           avg(hrv_ms) filter (where ordinal between 0 and 6) as recent7_mean_ms,
           avg(hrv_ms) filter (where ordinal>=7) as baseline_mean_ms,
           stddev_pop(hrv_ms) filter (where ordinal>=7) as baseline_sd_ms,
           bool_and(metadata_valid) and count(distinct snapshot_state)=1 and count(distinct evidence_present)=1
             and bool_and(snapshot_state in ('captured','unrecoverable'))
             and reader in ('history','morning')
             and count(distinct reader_path)=1
             and bool_and(reader_path in ('history','morning_warm','morning_cold'))
             and (count(ordinal)=0 or
                  (min(ordinal)=0 and max(ordinal)=count(ordinal)-1
                   and count(distinct ordinal)=count(ordinal)
                   and bool_and(ordinal is null or (observed_date is not null and hrv_ms>0
                       and hrv_ms<'Infinity'::double precision)))) as valid_snapshot
    from captured group by snapshot_id, profile, target_date, reader
), eligibility as (
    select *, case
        when valid_snapshot is not true then 'invalid_snapshot'
        when snapshot_state='unrecoverable' then 'unrecoverable_snapshot'
        when reader='morning' and not evidence_present then 'missing_same_day_evidence'
        when n_values=0 or (reader='history' and current_observed_date<>target_date) then 'missing_same_day_value'
        when n_values<9 then 'insufficient_observations'
        end as skip_reason
    from grouped
), standardized as (
    {{ readiness_hrv_math_v1('eligibility') }}
)
select snapshot_id, profile, target_date, reader, reader_path, snapshot_state, evidence_present,
       n_values, input_ordinals, input_dates, input_values, available, skip_reason,
       case when available and current_observed_date=target_date then current_ms end as current_ms,
       current_observed_date,
       case when available then recent7_mean_ms end as recent7_mean_ms,
       case when available then baseline_mean_ms end as baseline_mean_ms,
       case when available then baseline_sd_ms end as baseline_sd_ms,
       z_today, z_trend, blended_z, component_weight, weighted_z
from standardized
