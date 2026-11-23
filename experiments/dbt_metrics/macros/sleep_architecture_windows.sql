{% macro sleep_architecture_windows() %}
with calendar as (
    select a.anchor,n.days,a.anchor-lag_days as observation
    from anchors a cross join (values (7),(14)) n(days)
    cross join lateral generate_series(0,n.days-1) d(lag_days)
), inputs as (
    select c.*,v.*,coalesce(nullif(v.reason,''),'architecture_segments_missing') as missing_reason
    from calendar c left join architecture v on v.day=c.observation::text
), missing as (
    select anchor,days,jsonb_object_agg(missing_reason,n) as reasons from (
        select anchor,days,missing_reason,count(*) as n from inputs
        where eligible is not true group by anchor,days,missing_reason
    ) r group by anchor,days
)
select i.anchor::text,i.days,count(*) filter(where eligible) as eligible_days,
       count(*) filter(where source<>'') as source_days,coalesce(m.reasons,'{}') as reasons,
       case count(*) filter(where eligible) when 0 then 'unavailable' when i.days then 'high' else 'partial' end as confidence,
       avg(waso order by observation) filter(where eligible) as waso,
       avg(explicit_bouts::float8 order by observation) filter(where eligible) as explicit_bouts,
       avg(gaps::float8 order by observation) filter(where eligible) as gaps,
       avg(fragmentation order by observation) filter(where eligible) as fragmentation,
       max(longest) filter(where eligible) as longest
from inputs i left join missing m using(anchor,days) group by i.anchor,i.days,m.reasons
{% endmacro %}
