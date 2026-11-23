{{ config(enabled=var('complex_sleep_parity',false),materialized='view',post_hook="alter view {{ this }} set (security_invoker = true)") }}

with dates as (
    select p.*, min(period_day) filter(where reason is not null)
        over(partition by profile,capture) as first_missing
    from {{ ref('sleep_balance_period_v1') }} p
)
select profile,capture,wake_date,min(period_day) as window_start_date,max(ends_at) as calculated_through,
       case when first_missing is null then 'complete' else 'incomplete' end as state,
       case when bool_or(assessment='outlier') filter(where first_missing is null or period_day<first_missing)
            then 'low' else 'normal' end as confidence,
       max(reason) filter(where period_day=first_missing) as incomplete_reason,
       max(period_day) filter(where first_missing is null or period_day<first_missing) as last_complete_date,
       case when first_missing is null then sum(total_hours-goal_hours order by period_day) end as balance_hours
from dates group by profile,capture,wake_date,first_missing
