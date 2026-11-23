WITH step_points AS (
    SELECT 'step_count'::text AS metric_name, i.* FROM ({{WITHDRAWAL_STEP_INPUTS}}) i
), cached AS (
    SELECT h.* FROM hourly_metrics h WHERE metric_name='step_count'
    AND hour IN (SELECT substring(date,1,13)||':00' FROM step_points)
), incomplete AS ({{INCOMPLETE_STEP_HOURS}}), reconciled AS ({{RECONCILED_STEP_ALIAS_CELLS}}), withdrawn AS (
    SELECT substring(date,1,13) || ':00' AS hour, source
    FROM step_points
    GROUP BY substring(date,1,13), source
    HAVING NOT bool_or(coalesce((qty>0 OR (qty=0 AND grain='hour_sum')) AND quality='ok' AND counted, false))
), cleared AS (
    DELETE FROM hourly_metrics h USING withdrawn w
    WHERE h.metric_name='step_count' AND h.hour=w.hour AND h.source=w.source
      AND (NOT EXISTS (SELECT FROM incomplete i WHERE i.hour=h.hour)
           OR EXISTS (SELECT FROM reconciled r WHERE (h.hour,h.source)=(r.hour,r.source) AND r.avg_val IS NULL))
    RETURNING h.hour, h.source
), touched AS (
    SELECT DISTINCT substring(hour,1,10) AS day FROM cleared
    UNION SELECT substring(date,1,10) FROM step_points WHERE NOT counted
), surviving AS (
    -- A data-modifying CTE and its sibling reads share a snapshot. Exclude
    -- the returned cells explicitly so the same statement sees their removal.
    SELECT h.* FROM hourly_metrics h JOIN touched t ON substring(h.hour,1,10)=t.day
    WHERE h.metric_name='step_count'
      AND NOT EXISTS (SELECT FROM cleared c WHERE c.hour=h.hour AND c.source=h.source)
), totals AS (
    {{SURVIVING_STEP_TOTALS}}
)
UPDATE daily_scores d SET steps=s.source_total, computed_at=now()::text
FROM touched t LEFT JOIN totals s USING(day) WHERE d.date=t.day
