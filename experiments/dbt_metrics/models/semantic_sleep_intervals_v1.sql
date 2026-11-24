{{ config(enabled=var('semantic_shadow', false) or var('tenant_native_catalog', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

-- Inputs must have independent episode attribution. A capture commitment is
-- not an interval source. Canonicalize labels before unioning same-stage time.
with labels as (
    select i.wake_date, i.source, i.start_at, i.end_at,
           regexp_replace(i.stage, '^sleep_', '') as label
    from {{ ref('native_sleep_interval_v1') if var('tenant_native_catalog',false) else source('source_data', 'sleep_interval') }} i
    join {{ source('source_data', 'completed_night_sleep') }} n using (wake_date, source)
), unions as (
    select wake_date, source,
           case when label = 'asleep' then 'unspecified' else label end as stage,
           range_agg(tstzrange(start_at, end_at, '[)')) as spans
    from labels
    where label in ('asleep','core','deep','rem','unspecified','awake')
    group by 1, 2, 3
)
select wake_date, source, lower(span) as start_at, upper(span) as end_at, stage
from unions cross join lateral unnest(spans) as r(span)
