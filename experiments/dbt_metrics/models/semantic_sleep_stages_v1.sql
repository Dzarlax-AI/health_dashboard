{{ config(enabled=var('semantic_shadow', false), materialized='view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

-- One measured stage per main night and its canonical source. Same-stage
-- intervals were unioned first. The day view retains a reason when mixed
-- labels conflict. No row means that stage duration cannot be stated.
select i.wake_date, i.source, i.stage, count(*) as disjoint_intervals,
       sum(extract(epoch from i.end_at - i.start_at)) / 60.0 as observed_minutes
from {{ ref('semantic_sleep_intervals_v1') }} i
join {{ ref('semantic_sleep_day_v1') }} d using (wake_date,source)
where not d.conflicting_stages
group by i.wake_date, i.source, i.stage
