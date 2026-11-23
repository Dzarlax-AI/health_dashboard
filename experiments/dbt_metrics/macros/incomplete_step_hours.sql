{% macro incomplete_step_hours(points, cached, ordered_steps=false) %}
with alias_cells as (
    select distinct substring(date,1,13) as wall_hour,source from {{ points }} where not counted
), proof_points as (
    -- Older hourly rows used only the primary key. New cache rows can also
    -- carry archive samples whose original primary record has been replaced.
    select p.*,mode as proof_mode from {{ points }} p
    cross join (values (0),(1),(2),(3),(4),(5),(6),(7)) modes(mode)
    left join alias_cells a on (a.wall_hour,a.source)=(substring(p.date,1,13),p.source)
    where (mode<4 or (p.counted and a.wall_hour is not null))
      and (mode%4<2 or p.stored_key='step_count') and (mode%2=0 or p.quality='ok')
), minutes as (
    -- A quality correction can withdraw a previously published value. Its
    -- quantity still proves which old aggregate can actually be reconstructed.
    select substring(date,1,16) as minute,source,proof_mode,max(qty)::real as hi,min(qty)::real as lo
    from proof_points where qty>0
    group by substring(date,1,16),source,proof_mode
), evidence as (
    select substring(minute,1,13)||':00' as hour,source,
           array_agg(hi order by minute) as highs,array_agg(lo order by minute) as lows,
           count(*) as n,sum(hi{% if ordered_steps %} order by minute collate "C"{% endif %})::real as total,min(lo) as low,max(hi) as high
    from minutes group by substring(minute,1,13),source,proof_mode
)
select distinct c.hour from {{ cached }} c
where not exists(select from {{ points }} z where c.avg_val=0 and z.source=c.source
    and substring(z.date,1,13)||':00'=c.hour and z.qty=0 and z.grain='hour_sum')
  and not exists (
    select from evidence e where (e.hour,e.source)=(c.hour,c.source) and (
    coalesce((e.n=c.sample_count and (e.total,e.low,e.high)=(c.avg_val,c.min_val,c.max_val)),false)
    or (e.n>c.sample_count and exists (
        select from generate_series(1,(e.n-c.sample_count+1)::int) s(i)
        where (select sum(v{% if ordered_steps %} order by ordinal{% endif %})::real from unnest(e.highs[s.i:s.i+c.sample_count-1]) {% if ordered_steps %}with ordinality as slice(v,ordinal){% else %}v{% endif %})=c.avg_val
          and (select min(v) from unnest(e.lows[s.i:s.i+c.sample_count-1]) v)=c.min_val
          and (select max(v) from unnest(e.highs[s.i:s.i+c.sample_count-1]) v)=c.max_val
    ))
  ))
union
select a.hour from ({{ ambiguous_step_hours(points) }}) a where exists(select from {{ cached }} c where c.hour=a.hour)
{% endmacro %}
