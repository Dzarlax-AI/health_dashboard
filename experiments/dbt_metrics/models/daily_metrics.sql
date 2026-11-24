{{ config(alias='hdbt_w_daily' if var('tenant_aggregate_worker', false) else 'daily_metrics', materialized='incremental', unique_key='date', incremental_strategy='merge') }}
{% do validate_affected_dates() %}
{% set previous_daily = none %}
{% if execute and not is_incremental() %}
    {% set previous_daily = adapter.get_relation(database=this.database,schema=this.schema,identifier=this.identifier) %}
{% endif %}
{% set prior_metric = {
    'hrv_avg': ['heart_rate_variability'],
    'rhr_avg': ['resting_heart_rate'],
    'sleep_total': ['sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_awake','sleep_unspecified'],
    'calories': ['active_energy'],
    'exercise_min': ['apple_exercise_time'],
    'spo2_avg': ['blood_oxygen_saturation','oxygen_saturation'],
    'vo2_avg': ['vo2_max'],
    'resp_avg': ['respiratory_rate']
} %}

WITH days AS (
    SELECT date FROM {{ source('source_data', 'calendar') }} c
    WHERE {{ step_affected_dates_filter('c.date') }}
),
daily_steps AS (
    with points as (
        {{ step_point_inputs(ref('retained_metric_points'),source('source_data','health_records'),step_affected_dates_filter('substring(p.date,1,10)')) }}
    ), cached as (
        select * from {{ ref('hourly_metrics') }} where metric_name='step_count'
        and {{ step_affected_dates_filter('substring(hour,1,10)') }}
    ), chosen as ({{ physical_step_hours('points','cached', ordered_steps=var('tenant_aggregate_worker', false)) }})
    {{ daily_step_totals('chosen', ordered_steps=var('tenant_aggregate_worker', false)) }}
),
per_source AS (
    {{ daily_source_rows(ref('hourly_metrics'), affected_dates_filter('SUBSTRING(hour, 1, 10)')) }}
),
{{ legacy_sleep_selection_v1('per_source',
    "CASE WHEN st.source LIKE '%Ultra%' OR st.source LIKE '%Apple Watch%' THEN 0 WHEN st.source LIKE '%RingConn%' THEN 1 ELSE 2 END",
    '1.0', '1.4', "'sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_awake'", 5,
    "'sleep_total','sleep_unspecified'", 2) }},
daily_picks as (
    select p.date,p.source as picked_source,g.complete from picked p join sleep_gate g using(date)
), candidates as (
    {{ daily_candidates("source like '%Ultra%' or source like '%Apple Watch%'", "source like '%iPhone%'") }}
), changes as (
    select distinct substring(hour,1,10) as date from {{ ref('step_hour_state') }}
    where withdrawn and {{ step_affected_dates_filter('substring(hour,1,10)') }}
)
select r.date,{{ daily_retained_values('r', 'prior', 'changed.date is not null') }}
from candidates r left join changes changed on changed.date=r.date
{% if is_incremental() %}
{% if var('tenant_aggregate_worker', false) %}
LEFT JOIN (
    select previous.date,previous.steps,
    {% for column, metrics in prior_metric.items() %}
    case when not coalesce(withdrawals.metrics && array['{{ metrics | join("','") }}']::text[],false)
        then previous.{{ column }} end as {{ column }},
    {% endfor %}
    {% for column in ['sleep_deep','sleep_rem','sleep_core','sleep_awake','sleep_unspecified'] %}
    case when not coalesce(withdrawals.metrics && array['{{ prior_metric['sleep_total'] | join("','") }}']::text[],false)
        then previous.{{ column }} end as {{ column }}{% if not loop.last %},{% endif %}
    {% endfor %}
    from {{ this }} previous left join (
        select date,array_agg(metric_name) as metrics
        from {{ source('source_data','hdbt_i_daily_withdrawals') }}
        group by date
    ) withdrawals on withdrawals.date=previous.date
) prior ON prior.date=r.date
{% else %}
LEFT JOIN {{ this }} prior ON prior.date = r.date
{% endif %}
{% else %}
LEFT JOIN (SELECT {% if previous_daily is not none %}date{% else %}NULL::text{% endif %} AS date, NULL::real AS hrv_avg, NULL::real AS rhr_avg,
  NULL::real AS sleep_total, NULL::real AS sleep_deep, NULL::real AS sleep_rem,
  NULL::real AS sleep_core, NULL::real AS sleep_awake, NULL::real AS sleep_unspecified,
  {% if previous_daily is not none %}steps{% else %}NULL::real{% endif %} AS steps, NULL::real AS calories, NULL::real AS exercise_min,
  NULL::real AS spo2_avg, NULL::real AS vo2_avg, NULL::real AS resp_avg
  {% if previous_daily is not none %}FROM {{ previous_daily }}{% endif %}) prior ON prior.date=r.date
{% endif %}
