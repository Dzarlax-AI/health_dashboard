{{ config(enabled=var('semantic_shadow', false)) }}

with history as (
    select wake_date, count(*) as n, count(distinct history_date) as distinct_n,
        min(history_date) as oldest, max(history_date) as newest,
        array_agg(history_date order by history_date) filter (where eligible) as included
    from {{ ref('semantic_sleep_history_v1') }} group by wake_date
)
select f.wake_date from {{ ref('semantic_sleep_day_v1') }} f
left join history h using (wake_date)
where h.n is distinct from 30 or h.distinct_n is distinct from 30
   or h.oldest is distinct from f.window_start or h.newest is distinct from (f.wake_date::date-1)::text
   or coalesce(f.eligible_dates,'{}'::text[]) <> coalesce(h.included,'{}'::text[])
   or ((f.status = 'available') <> (f.reference_minutes is not null and f.delta_minutes is not null))
   or f.delta_minutes is distinct from f.duration_minutes - f.reference_minutes
union all
select c.date from {{ source('source_data','calendar') }} c
left join {{ ref('semantic_sleep_day_v1') }} f on c.date = f.wake_date
group by c.date having count(f.wake_date) <> 1
union all
select s.wake_date from {{ ref('semantic_sleep_stages_v1') }} s
join {{ ref('semantic_sleep_day_v1') }} d using (wake_date)
where d.conflicting_stages or s.observed_minutes <= 0 or s.source <> d.source
