{{ config(enabled=var('personal_baseline_shadow', false), materialized='view', post_hook="alter view {{ this }} set (security_invoker = true)") }}

with captures as (
    select *, anchor::timestamp at time zone zone as as_of,
           anchor - case when window_days > 0 then window_days else 30 end as first_day
    from {{ source('personal_baseline_pilot', 'captures') }}
)
select c.profile, c.capture, c.channel, c.anchor, c.zone, c.window_days,
       b.state, b.sample_count, coalesce(b.age_ns,0) / 1000000000::numeric as age_seconds,
       b.eligible, b.baseline_median, b.mad_sd, coalesce(b.age_ns,0)::bigint as age_nanoseconds
from captures c cross join lateral (
    with samples as materialized (
        select s.value, s.observed_at
        from {{ source('personal_baseline_pilot', 'selected_samples') }} s
        where s.profile = c.profile and s.channel = c.channel
          and s.writer_day >= c.first_day and s.writer_day < c.anchor
          and s.value > '-Infinity'::float8 and s.value < 'Infinity'::float8
    ), stats as (
        {{ personal_baseline_stats() }}
    ), summary as (
        select stats.*, c.channel,
               extract(epoch from (c.as_of - (select max(observed_at) from samples))) * 1000000000 as age_ns
        from stats
    )
    {{ personal_baseline_result() }}
) b
