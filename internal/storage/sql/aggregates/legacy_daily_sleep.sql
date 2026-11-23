WITH daily_steps AS (SELECT null::text AS day, null::float8 AS source_total WHERE false), per_source AS (
    {{DAILY_SLEEP_SOURCES}}
),
{{BACKFILL_SLEEP_SELECTION}},
days as (select date from picked),
daily_picks as (select p.date,p.source as picked_source,g.complete from picked p join sleep_gate g using(date)),
candidates as ({{DAILY_SLEEP_CANDIDATES}})
insert into daily_scores(date,sleep_total,sleep_deep,sleep_rem,sleep_core,sleep_awake,sleep_unspecified,computed_at)
select date,sleep_total,sleep_deep,sleep_rem,sleep_core,sleep_awake,sleep_unspecified,now()::text from candidates
on conflict(date) do update set
    (sleep_total,sleep_deep,sleep_rem,sleep_core,sleep_awake,sleep_unspecified) = (select {{DAILY_RETAINED_SLEEP}}),
    computed_at=excluded.computed_at
