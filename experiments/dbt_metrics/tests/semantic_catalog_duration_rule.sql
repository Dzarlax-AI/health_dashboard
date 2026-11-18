{{ config(enabled=var('semantic_shadow', false)) }}

-- Independently evaluate the declared reference rule against its input history.
with rule as (
    select (dbt_binding->>'window_days')::integer as days,
           (dbt_binding->>'minimum_nights')::integer as minimum_nights
    from {{ ref('semantic_metric_catalog_v1') }}
    where metric_id='sleep_duration_reference_v1'
)
select actual.wake_date
from {{ ref('sleep_duration_reference_v1') }} actual
cross join rule
cross join lateral (
    select count(*) as n, avg(history.duration_hours) as mean_hours
    from {{ source('source_data','completed_night_sleep') }} history
    where history.wake_date >= (actual.wake_date::date-rule.days)::text
      and history.wake_date < actual.wake_date
      and (history.source,history.source_epoch,history.algorithm_version)
          = (actual.source,actual.source_epoch,actual.algorithm_version)
      and (history.capture_completeness,history.duration_assessment,history.finalization_state,history.claim_eligibility)
          = ('complete','plausible','final','eligible')
) expected
where rule.days is null or rule.minimum_nights is null
   or actual.window_start <> (actual.wake_date::date-rule.days)::text
   or actual.eligible_nights <> expected.n
   or (actual.reference_hours is not null) <> (expected.n >= rule.minimum_nights)
   or abs(actual.reference_hours-expected.mean_hours) > 0.000000001
union all
select 'missing reference rule' where not exists (select from rule)
