{% macro personal_baseline_stats() %}
select m.n as sample_count, m.median,
       (select percentile_cont(0.5) within group (order by abs(s.value - m.median)) * 1.4826
        from samples s) as sd
from (
    select count(*) as n, percentile_cont(0.5) within group (order by value) as median
    from samples
) m
{% endmacro %}
