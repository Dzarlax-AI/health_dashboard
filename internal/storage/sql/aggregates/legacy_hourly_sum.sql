WITH hourly_points AS NOT MATERIALIZED (
    SELECT metric_name, date, source, qty, quality FROM metric_points
    WHERE metric_name = $1 {{SLEEP_DEDUP}} {{FROM_CLAUSE}}
), {{CONFIRMED_INVALID_NAP_HOURS}}, projected AS ({{HOURLY_SUM_ROWS}}),
step_points AS (select 'step_count'::text as metric_name, i.* from ({{LEGACY_STEP_INPUTS}}) i),
step_rows AS ({{STEP_WALL_ROWS}}),
cached AS (
    select h.* from hourly_metrics h
    where metric_name='step_count' and hour in (select substring(date,1,13)||':00' from step_points)
), incomplete AS ({{INCOMPLETE_STEP_HOURS}}), reconciled AS ({{RECONCILED_STEP_ALIAS_CELLS}})
INSERT INTO hourly_metrics (metric_name, hour, source, avg_val, min_val, max_val, sample_count)
select p.* from projected p where metric_name<>'step_count'
union all select p.* from step_rows p where not exists (select from incomplete i where i.hour=p.hour)
union all select 'step_count',r.hour,r.source,r.avg_val,r.min_val,r.max_val,r.sample_count
from reconciled r join incomplete i using(hour) where r.avg_val is not null
ON CONFLICT (metric_name, hour, source) DO UPDATE SET
    avg_val=EXCLUDED.avg_val, min_val=EXCLUDED.min_val, max_val=EXCLUDED.max_val,
    sample_count=EXCLUDED.sample_count
