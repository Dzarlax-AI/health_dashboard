{% macro chip_recovery_sleep_v1(inputs) %}
select i.case_id, 52.5::double precision - coalesce(s.slept, 0) as debt_hours,
       s.mean_confidence, s.low_days, coalesce(s.class_counts, '{}'::jsonb) as class_counts
from {{ inputs }} i
cross join lateral (
    with assessments as (
        select a.* from jsonb_to_recordset(case when jsonb_typeof(i.days) = 'array' then i.days end)
            as a(day date, eligible_hours double precision, class text, confidence double precision, low boolean)
        where a.day between greatest(i.as_of_date - 6, i.epoch_start) and i.as_of_date
    )
    select sum(eligible_hours order by day desc) as slept, avg(confidence order by day desc) as mean_confidence,
           count(*) filter (where low)::int as low_days,
           (select jsonb_object_agg(class, n) from
               (select class, count(*) as n from assessments where class is not null group by class) counts) as class_counts,
           count(*) as n, count(distinct day) as distinct_days,
           count(*) filter (where class is null or confidence is null or low is null) as incomplete
    from assessments
) s
where jsonb_typeof(i.days) = 'array' and s.incomplete = 0 and s.n = s.distinct_days
  and s.n = greatest(0, i.as_of_date - greatest(i.as_of_date - 6, i.epoch_start) + 1)
{% endmacro %}
