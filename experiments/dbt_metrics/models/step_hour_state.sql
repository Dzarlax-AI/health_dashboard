{{ config(alias='hdbt_w_step' if var('tenant_aggregate_worker', false) else 'step_hour_state', materialized='incremental', unique_key=['hour','source'], incremental_strategy='merge',on_schema_change='append_new_columns') }}
{% do validate_affected_dates() %}
{% set prior_hours = none %}
{% if execute and not is_incremental() %}
    {# A pre-state-model installation can seed its known cells from the last
       successful hourly publication. This is an upgrade read, not a DAG input. #}
    {% set prior_hours = adapter.get_relation(database=this.database,schema=this.schema,identifier='hourly_metrics') %}
{% endif %}

-- Keep the last known source hour when a partial capture omits it. A formerly
-- usable hour can also be revoked by explicit nonpositive/invalid readings.
with points as (
    select 'step_count'::text as metric_name,i.* from (
        {{ step_point_inputs(ref('retained_metric_points'),source('source_data','health_records'),step_affected_dates_filter('substring(p.date,1,10)')) }}
    ) i
), valid as (
    {{ step_wall_rows('points') }}
), receipts as (
    {% if var('verified_step_withdrawals', false) %}
    select distinct substring(point_date,1,13)||':00' as hour,source from {{ source('source_data','verified_step_withdrawals') }}
    where {{ step_affected_dates_filter('substring(point_date,1,10)') }}
    {% else %}
    select null::text as hour,null::text as source where false
    {% endif %}
), prior as (
    {% if is_incremental() %}
    select hour,source,avg_val,min_val,max_val,sample_count,withdrawn, to_jsonb(prior_row)->>'proven_input' as proven_input from {{ this }} prior_row
    where {{ step_affected_dates_filter('substring(hour,1,10)') }}
    {% elif prior_hours is not none %}
    select hour,source,avg_val,min_val,max_val,sample_count,false as withdrawn,null::text as proven_input
    from {{ prior_hours }} where metric_name='step_count'
    {% else %}
    select null::text as hour,null::text as source,null::real as avg_val,null::real as min_val,
           null::real as max_val,null::int as sample_count,false as withdrawn,null::text as proven_input where false
    {% endif %}
), signatures as ({{ step_capture_signature('points') }}), prior_valid as (
    select p.* from prior p where avg_val is not null
      and not exists(select from signatures s where (p.hour,p.proven_input)=(s.hour,s.signature))
      and not exists (select from receipts r where (p.hour,p.source)=(r.hour,r.source))
), frozen as (
    {{ incomplete_step_hours('points','prior_valid') }}
    union select a.hour from ({{ ambiguous_step_hours('points') }}) a
    where exists(select from prior p where p.hour=a.hour and p.avg_val is not null
        and not exists(select from receipts r where (r.hour,r.source)=(p.hour,p.source)))
), frozen_prior as (
    select p.* from prior p join frozen f using(hour)
    where not exists(select from receipts r where (r.hour,r.source)=(p.hour,p.source))
), repairs as ({{ reconciled_step_alias_cells('points','frozen_prior') }}),
held_hours as (
    select distinct p.hour from frozen_prior p left join repairs r using(hour,source)
    where p.avg_val is not null and (r.hour is null or r.avg_val is not null)
), suppressed as (
    select distinct substring(date,1,13)||':00' as hour,source from points where not counted
), affected as (
    select substring(date,1,13)||':00' as hour,source from points
    union select hour,source from receipts
)
select a.hour,a.source,v.avg_val,v.min_val,v.max_val,v.sample_count,
       v.hour is null and (r.hour is not null or p.avg_val is not null or coalesce(p.withdrawn,false) or s.hour is not null) as withdrawn,
       null::text as proven_input
from affected a left join valid v using(hour,source) left join receipts r using(hour,source) left join prior p using(hour,source)
left join suppressed s using(hour,source)
where r.hour is not null or not exists(select from held_hours f where f.hour=a.hour)
union all
select p.* from frozen_prior p join held_hours using(hour)
where not exists(select from repairs r where (r.hour,r.source)=(p.hour,p.source))
union all
select r.*,r.avg_val is null as withdrawn,null::text as proven_input from repairs r join held_hours using(hour)

{% if prior_hours is not none %}
union all
select p.* from prior p where not exists(select from frozen f where f.hour=p.hour) and not exists(select from affected a where (a.hour,a.source)=(p.hour,p.source))
{% endif %}
