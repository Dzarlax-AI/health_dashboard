-- Preserve the candidate's recorded lookahead horizon. For passive targets the
-- writer retains BPM for complete three-day windows and reasons for all dates.
create table replay.chip_target_input as
select concat_ws('/',t.sub_score,t.target_kind,t.date) as case_id,t.sub_score,t.target_kind,
       t.date::date as target_date,nullif(t.data_coverage->>'latest_observed_sleep_date','')::date as latest_sleep,
       null::int as paired_count,null::int as breach_threshold,operands.days
from source_data.target_snapshots t cross join lateral (
    select coalesce(jsonb_agg(jsonb_build_object('date',x.day,'eligible',x.reason in ('ok','ok_awake_structural_zero'),
        'reason',x.reason,'value',x.value) order by x.day),'[]') as days from (
        select (t.date::date + offs)::text as day,
            case t.sub_score when 'recovery_stability' then coalesce(s.reason,'sleep_total_out_of_range')
                 else coalesce(t.data_coverage->'per_day_reason'->>(offs-1),'no_walking_hr') end as reason,
            case t.sub_score when 'recovery_stability' then s.value
                 else (t.data_coverage->'per_day_value'->>((t.date::date + offs)::text))::float8 end as value
        from generate_series(1,case when t.target_kind='daily_point' then 1 else 3 end) offs
        left join replay.sleep_operands s on s.day=(t.date::date+offs)::text
    ) x
) operands
where (t.sub_score='recovery_stability' and t.target_kind in ('daily_point','rolling_3d','rolling_3d_candidate_2of3'))
   or (t.sub_score='passive_efficiency' and t.target_kind='rolling_3d');
create schema sleep_shadow;
create view sleep_shadow.candidate as select * from source_data.sleep_rollout_candidates;
