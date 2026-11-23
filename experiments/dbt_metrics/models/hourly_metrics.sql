{{ config(alias='hdbt_w_hourly' if var('tenant_aggregate_worker', false) else 'hourly_metrics', materialized='incremental', incremental_strategy='append', pre_hook="{{ delete_hourly_affected_days() }}") }}
{% do validate_affected_dates() %}

WITH valid_points AS (
    SELECT metric_name, date, source, qty, quality
    FROM {{ ref('retained_metric_points') }} p
    WHERE {{ affected_dates_filter('SUBSTRING(date, 1, 10)') }}
      AND (metric_name NOT LIKE 'sleep\_%' ESCAPE '\'
           OR {{ keep_sleep_point(ref('retained_metric_points'), 'p') }})
),
sum_points as (
    select * from valid_points
    where metric_name<>'step_count' and (metric_name like 'sleep\_%' escape '\' or metric_name = any({{ sum_metrics_array() }}))
),
avg_points as (
    select * from valid_points
    where metric_name not like 'sleep\_%' escape '\' and metric_name <> all({{ sum_metrics_array() }})
)
{{ hourly_avg_rows('avg_points') }}
union all
{{ hourly_sum_rows('sum_points') }}
union all
select 'step_count',hour,source,avg_val,min_val,max_val,sample_count from {{ ref('step_hour_state') }}
where avg_val is not null and {{ step_affected_dates_filter('substring(hour,1,10)') }}
