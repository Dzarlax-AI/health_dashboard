{% macro briefing_metric_trends() %}
with means as (
  select profile,capture,metric,max(value) filter(where ordinal=1) as current_value,
    avg(value order by ordinal) filter(where ordinal between 2 and 8) as baseline7,
    avg(value order by ordinal) filter(where ordinal>1) as baseline30 from series group by 1,2,3
), scaled as (
  select *,coalesce((current_value-baseline7)/nullif(baseline7,0)*100*10,0) as x7,
    coalesce((current_value-baseline30)/nullif(baseline30,0)*100*10,0) as x30,
    case when metric='resting_heart_rate' then -1 else 1 end as direction from means
), changes as (
  select *, sign(x7)*(floor(abs(x7))+(abs(x7)-floor(abs(x7))>=0.5)::int)/10 as pct7,
    sign(x30)*(floor(abs(x30))+(abs(x30)-floor(abs(x30))>=0.5)::int)/10 as pct30
  from scaled
)
select profile,capture,metric,current_value,baseline7,baseline30,pct7,pct30,
  case when pct7*direction>1 then 'positive' when pct7*direction < -1 then 'negative' else 'neutral' end as status7,
  case when pct30*direction>1 then 'positive' when pct30*direction < -1 then 'negative' else 'neutral' end as status30
from changes
{% endmacro %}
