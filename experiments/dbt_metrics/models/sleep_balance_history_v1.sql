{{ config(enabled=var('complex_sleep_parity',false),materialized='view',post_hook="alter view {{ this }} set (security_invoker = true)") }}

-- The saved history exposes only the complete prefix of a captured balance window.
select p.*, p.total_hours-p.goal_hours as delta_hours
from {{ ref('sleep_balance_period_v1') }} p
join {{ ref('sleep_duration_balance_v1') }} s using (profile,capture,wake_date)
where p.period_day <= s.last_complete_date
