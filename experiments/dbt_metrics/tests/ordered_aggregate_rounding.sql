-- Reversing physical input order must preserve the chronological float4 sum.
with points(metric_name,source,date,qty,quality) as (values
    ('active_energy','watch','2026-04-06 08:02:00 +0000',0.00001::real,'ok'),
    ('active_energy','watch','2026-04-06 08:01:00 +0000',0.00001::real,'ok'),
    ('active_energy','watch','2026-04-06 08:00:00 +0000',400::real,'ok')
), hours as (
    {{ hourly_sum_rows('points', true) }}
), slots(metric_name,source,hour,avg_val,min_val,max_val) as (values
    ('active_energy','watch','2026-04-06 10:00',0.00001::real,0.00001::real,0.00001::real),
    ('active_energy','watch','2026-04-06 09:00',0.00001::real,0.00001::real,0.00001::real),
    ('active_energy','watch','2026-04-06 08:00',400::real,400::real,400::real)
), source_days as (
    {{ daily_source_rows('slots','true') }}
)
select avg_val from hours where avg_val<>400::real
union all select sum_val from source_days where sum_val<>400::real
