{% macro chip_baseline_v1(inputs) %}
with continuous as (
    select * from {{ inputs }} where sub_score not in ('acute_risk','chronic_load')
), features as (
    {{ chip_feature_windows_v1('continuous') }}
), predictions as (
    select i.case_id,i.sub_score,i.target_kind,k.kind,k.predicted,
           i.as_of_date-k.lookback < i.epoch_start as clipped
    from continuous i join features f using(case_id)
    cross join lateral (values
        ('persistence_yesterday',0,f.today),
        ('rolling_7d_mean',6,f.mean7),
        ('rolling_30d_mean',29,f.mean30),
        ('ewma_45d',135,f.adaptive_value)
    ) k(kind,lookback,predicted)
    union all
    select i.case_id,i.sub_score,i.target_kind,'event_base_rate',e.predicted,
           i.as_of_date-90 < i.epoch_start
    from {{ inputs }} i
    cross join lateral (
        select avg((d->>'value')::float8) as predicted from jsonb_array_elements(i.days) d
        where (d->>'eligible')::bool
          and (d->>'date')::date between i.as_of_date-90 and i.as_of_date-1
          and (i.epoch_start is null or (d->>'date')::date>=i.epoch_start)
    ) e
    where i.sub_score in ('acute_risk','chronic_load')
)
select case_id,sub_score,target_kind,kind as baseline_kind,predicted,
       case when predicted is not null then '' when clipped then 'baseline_source_epoch_boundary'
            else 'baseline_warmup' end as reason
from predictions
{% endmacro %}
