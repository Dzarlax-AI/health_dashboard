-- Compare the entire retrospective dataset for each recorded oxygen date.
-- The six columns follow the product's positive, good-quality, named-source rule.
with all_days as (
    select distinct left(date,10) as capture from source_data.metric_points
    where metric_name='blood_oxygen_saturation'
), valid as (
    select left(date,10) as capture,source,qty from source_data.metric_points
    where metric_name='blood_oxygen_saturation' and quality='ok' and source is not null and qty>0
), by_source as (
    select capture,source,count(*) as n,sum(qty::double precision) as sum_q,
           min(qty) as low,count(*) filter(where qty<94) as below94,count(*) filter(where qty<92) as below92
    from valid group by capture,source
), reference as (
    select d.capture,case when count(b.source)>0 then 'ok' else 'missing' end as status,
           coalesce(sum(n),0)::int as valid_readings,coalesce(sum(below94),0)::int as below94_count,
           coalesce(sum(below92),0)::int as below92_count,coalesce(min(low),0)::float8 as min,
           coalesce(sum(sum_q)/sum(n),0)::float8 as avg
    from all_days d left join by_source b using(capture) group by d.capture
), compared as (
    select coalesce(r.capture,m.capture) as day,r.capture is null or m.capture is null as unmatched,
           (r.status,r.valid_readings,r.below94_count,r.below92_count,r.min)
            is distinct from (m.status,m.valid_readings,m.below94_count,m.below92_count,m.min) as differs,
           r.avg is distinct from m.avg as average_differs,abs(r.avg-m.avg) as average_delta,
           r.status='ok' as available
    from reference r full join archive_formula.oxygen_cluster_v1 m using(capture)
)
select json_build_object('days',count(*),'available_days',count(*) filter(where available),
       'missing_or_extra',count(*) filter(where unmatched),'unequal_fields',count(*) filter(where differs),
       'unequal_average',count(*) filter(where average_differs),
       'average_outside_1e_12',count(*) filter(where average_delta>1e-12),
       'max_average_delta',coalesce(max(average_delta),0)) from compared;
