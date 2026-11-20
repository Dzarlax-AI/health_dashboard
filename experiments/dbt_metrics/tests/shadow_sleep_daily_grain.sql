{{ config(enabled=var('sleep_daily_shadow', false) and not var('sleep_daily_runtime_sql', false)) }}

SELECT profile, wake_date
FROM {{ ref('shadow_sleep_daily_v1') }}
GROUP BY profile, wake_date
HAVING count(*)<>1 OR profile IS NULL OR wake_date IS NULL OR
       bool_or(sleep_seconds IS NULL OR sleep_seconds<=0 OR stage_conflict IS NULL)
