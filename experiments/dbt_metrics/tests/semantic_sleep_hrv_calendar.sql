{{ config(enabled=var('semantic_shadow', false)) }}
with calendar as (select date from {{ source('source_data', 'calendar') }}),
facts as (select * from {{ ref('semantic_sleep_hrv_day_v1') }})
select c.date from calendar c left join facts f on f.wake_date=c.date
group by c.date having count(f.wake_date)<>1
union all
select wake_date from facts f where not exists (select from calendar c where c.date=f.wake_date)
   or (sources_with_sdnn>0 and interval_count=0)
   or (sources_with_sdnn=0 and measured_points+timestamp_points<>0)
