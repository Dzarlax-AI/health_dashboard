with pairs as (
    select c.case_id,count(p.predicted)::int as eligible_count,
           count(p.predicted) filter(where p.label>=0.5)::int as positive_count,
           percentile_cont(0.8) within group(order by p.predicted::double precision) as p80
    from replay.chip_calibration_input c left join replay.chip_pairs p on c.case_id=p.case_id and
        c.epoch not in ('','unknown') and p.prediction_epoch=c.epoch and p.label_epoch=c.epoch
        and p.eligible and p.predicted is not null and p.label is not null
    group by c.case_id
), classified as (
    select p.*,case when c.epoch in ('','unknown') then 'no_current_epoch'
        when eligible_count<90 then 'insufficient_eligible' when positive_count<10 then 'insufficient_positives'
        else 'active' end as status,positive_count::float8/nullif(eligible_count,0) as rate from pairs p
    join replay.chip_calibration_input c using(case_id)
), calibrations as (
    select (c.case_id,c.eligible_count,c.positive_count,c.status,
            case when c.status='active' then c.p80 end,case when c.status='active' then c.rate end,
            case when c.status='active' then greatest(c.p80,c.rate) end)
            is distinct from (m.case_id,m.n_eligible,m.n_positives,m.calibration_status,m.p80,m.base_rate,m.cutoff) as differs
    from classified c full join archive_formula.chip_calibration_v1 m using(case_id)
), labels as (
    select i.target_value is not null as measured,
           (m.case_id,m.target_value::real,m.eligible,m.reason) is distinct from
           (i.case_id,i.target_value,i.eligible,i.eligibility_reason) as differs
    from replay.chip_target_input i full join archive_formula.chip_targets_v1 m using(case_id)
), baselines as (
    select i.predicted_value is not null as measured,
           (m.case_id,m.predicted::real,m.reason) is distinct from
           (i.case_id,i.predicted_value,i.expected_reason) as differs
    from replay.chip_baseline_input i full join archive_formula.chip_baseline_v1 m using(case_id)
)
select json_build_object('target_rows',(select count(*) from labels),'target_measured',(select count(*) from labels where measured),
    'target_differences',(select count(*) from labels where differs),'baseline_rows',(select count(*) from baselines),
    'baseline_measured',(select count(*) from baselines where measured),'baseline_differences',(select count(*) from baselines where differs),
    'calibration_replays',(select count(*) from calibrations),'calibration_differences',(select count(*) from calibrations where differs));
