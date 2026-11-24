{{ config(enabled=var('tenant_native_catalog',false), materialized='ephemeral') }}
select summary_day as date,source,metric_name,
       case when bool_and(coalesce(hours between 0 and 24,false)) and min(hours)=max(hours)
            then min(hours)*60 end as observed_minutes
from {{ ref('native_sleep_stage_points_v1') }}
where summary_day is not null group by summary_day,source,metric_name
