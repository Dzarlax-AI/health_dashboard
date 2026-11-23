{% macro chip_calibration_v1(inputs, pairs) %}
with summary as (
    select i.case_id, i.epoch, count(p.predicted) as n_eligible,
           count(*) filter (where p.label >= 0.5) as n_positives,
           percentile_cont(0.8) within group (order by p.predicted::float8) as percentile
    from {{ inputs }} i
    left join {{ pairs }} p on i.case_id=p.case_id
        and i.epoch not in ('','unknown') and p.prediction_epoch=i.epoch and p.label_epoch=i.epoch
        and p.day between i.as_of_date-179 and i.as_of_date and p.eligible
        and p.baseline_kind='event_base_rate' and p.predicted is not null and p.label is not null
    group by i.case_id, i.epoch
), status as (
    select *, case when epoch in ('','unknown') then 'no_current_epoch'
                   when n_eligible<90 then 'insufficient_eligible' when n_positives<10 then 'insufficient_positives'
                   else 'active' end as calibration_status
    from summary
)
select case_id, n_eligible, n_positives, calibration_status,
       case when calibration_status='active' then percentile end as p80,
       case when calibration_status='active' then n_positives::float8/n_eligible end as base_rate,
       case when calibration_status='active' then greatest(percentile,n_positives::float8/n_eligible) end as cutoff
from status
{% endmacro %}
