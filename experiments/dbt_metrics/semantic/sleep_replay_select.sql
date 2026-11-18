-- Minimal packet, point and commitment projection for the isolated replay.
select 1/((current_setting('transaction_read_only')='on'
    and current_setting('transaction_isolation')='repeatable read'
    and current_schema()='health' and current_user=(select pg_get_userbyid(nspowner)
       from pg_namespace where nspname=current_schema()))::int) as guarded \gset
SELECT COALESCE(max(id),0) AS top_id,
       COALESCE(min(id) FILTER(WHERE received_at >= '2026-08-18'::timestamptz),0) AS first_id
FROM health_records
\gset
SELECT jsonb_build_object('kind','meta','snapshot',transaction_timestamp(),'raw_high_water',:top_id,'read_only',current_setting('transaction_read_only'));
SELECT jsonb_build_object('kind','night','day',n.wake_date,'source',n.source,'epoch',n.source_epoch,'duration',n.duration_hours,
  'capture',n.capture_completeness,'assessment',n.duration_assessment,'finality',n.finalization_state,'claim',n.claim_eligibility,
  'hash',n.input_hash,'observed',n.observed_at,'algorithm',n.algorithm_version,
  'period',p.capture_completeness,
  'commitment',CASE WHEN c.wake_date IS NOT NULL THEN to_jsonb(c) END)
FROM completed_night_sleep n
LEFT JOIN sleep_period_coverage p ON p.wake_date=n.wake_date
LEFT JOIN night_sleep_coverage_commitments c ON (c.wake_date,c.source)=(n.wake_date,n.source)
WHERE n.wake_date BETWEEN '2026-06-21' AND '2026-11-17'
ORDER BY n.wake_date;
SELECT jsonb_build_object('kind','point','record_id',health_record_id,'date',date,'time',h.received_at,'source',m.source,'duration',qty,'units',units,'origin',origin,'quality',quality)
FROM metric_points m JOIN health_records h ON h.id=m.health_record_id
WHERE metric_name='night_sleep_total' AND substring(date,1,10) BETWEEN '2026-06-20' AND '2026-11-18'
ORDER BY date,source;
SELECT format($batch$
WITH candidate AS MATERIALIZED (
 SELECT id,received_at,processing_kind,processing_status,payload::jsonb->'data' AS body
 FROM health_records WHERE id BETWEEN %s AND %s
  AND processing_status IN ('complete','pending') AND payload LIKE '%%"night_sleep_coverage"%%' AND payload IS JSON OBJECT
), covers AS MATERIALIZED (
 SELECT a.*, selected.coverages
 FROM candidate a CROSS JOIN LATERAL (
  SELECT jsonb_agg(c) AS coverages FROM jsonb_array_elements(COALESCE(a.body->'night_sleep_coverage','[]')) c
   WHERE c->>'wake_date' BETWEEN '2026-08-20' AND '2026-11-17'
 ) selected WHERE selected.coverages IS NOT NULL
)
SELECT jsonb_build_object('kind','packet','id',id,'received',received_at,'processing_kind',processing_kind,'status',processing_status,
  'data',jsonb_build_object('night_sleep_coverage',coverages,'metrics',(
    SELECT jsonb_agg(metric) FROM jsonb_array_elements(COALESCE(body->'metrics','[]')) metric
    WHERE metric->>'name'='night_sleep_total'
  )))
FROM covers ORDER BY id;
$batch$, start_id, least(start_id+399,:top_id))
FROM generate_series(:first_id,:top_id,400) start_id
\gexec
rollback;
