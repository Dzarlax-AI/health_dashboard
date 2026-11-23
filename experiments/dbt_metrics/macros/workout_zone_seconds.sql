{% macro workout_zone_seconds(input) %}
with workout as (
    select {{ input }} as payload
), samples as (
    select ordinal,
           (sample->>'second')::numeric * 1000000000 + (sample->>'nano')::integer as instant,
           (payload->>'end_second')::numeric * 1000000000 + (payload->>'end_nano')::integer as workout_end,
           (sample->>'avg')::float8 as bpm, payload->'borders' as borders
    from workout cross join lateral jsonb_array_elements(payload->'samples')
        with ordinality as points(sample, ordinal)
), buckets as (
    select (lead(instant, 1, workout_end) over (order by ordinal) - instant) as duration_ns,
           case when bpm < nullif(borders->>0, 'NaN')::float8 then 1
                when bpm < nullif(borders->>1, 'NaN')::float8 then 2
                when bpm < nullif(borders->>2, 'NaN')::float8 then 3
                when bpm < nullif(borders->>3, 'NaN')::float8 then 4
                else 5 end as zone
    from samples
), totals as (
    select zone, sum(round(least(duration_ns, 300000000000) / 1000000000))::bigint as seconds
    from buckets where duration_ns > 0 group by zone
)
select array_agg(coalesce(seconds,0) order by zone) as zone_seconds
from generate_series(1,5) as zones(zone) left join totals using (zone)
{% endmacro %}
