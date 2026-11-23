{% macro weekly_sleep_analysis() %}
with windows as (
    select *,least(7,coalesce(cardinality(sleep),0)) as window_size from weekly_inputs
), stats as (
    select w.*,s.*
    from windows w cross join lateral (
        select count(*) filter(where sleep[i]>0) as nights,
               avg(sleep[i] order by i) filter(where sleep[i]>0) as total_avg,
               avg(deep[i] order by i) as deep_avg,
               avg(rem[i] order by i) as rem_avg,
               avg(awake[i] order by i) as awake_avg
        from generate_series(1,w.window_size) n(i)
    ) s
), metrics as (
    select profile,capture,window_size,nights,v.name,v.value,v.scale
    from stats cross join lateral (values
        ('total_avg',total_avg,100),
        ('deep_avg',case when nights>0 then deep_avg end,100),
        ('rem_avg',case when nights>0 then rem_avg end,100),
        ('awake_avg',case when nights>0 then awake_avg end,100),
        ('efficiency',case when sleep[1]>0 then greatest(0,(sleep[1]-awake[1])/sleep[1]*100) end,10)
    ) v(name,value,scale)
)
select profile,capture,case when window_size>0 then
    jsonb_build_object('nights',nights) || jsonb_object_agg(name,
        coalesce({{ weekly_sleep_round('value', 'scale') }},0))
    end as weekly_sleep
from metrics group by profile,capture,window_size,nights
{% endmacro %}
