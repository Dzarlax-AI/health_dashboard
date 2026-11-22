-- Sensitive intermediate. Execute only inside an aggregate and an authorized tenant.
-- research_parameters is exactly one row: start_date date, end_date date, tz text.
WITH calendar AS (
 SELECT d::date AS day,p.tz FROM research_parameters p CROSS JOIN LATERAL generate_series(p.start_date,p.end_date,interval '1 day') d
), night AS (
 SELECT c.day,c.tz,n.wake_date,n.source,n.source_epoch,n.coverage_generation,n.duration_hours,n.observed_at,n.covered_interval_start,n.covered_interval_end,n.input_hash AS night_hash,n.diagnostics,
 COALESCE(n.capture_completeness='complete' AND n.duration_assessment='plausible' AND n.finalization_state='final' AND n.claim_eligibility='eligible' AND n.duration_hours>0 AND n.duration_hours<=24,false) AS canonical_night_eligible
 FROM calendar c LEFT JOIN completed_night_sleep n ON n.wake_date=c.day::text
), bound AS MATERIALIZED (
 SELECT n.*,c.metric_date,c.capture_completeness AS commitment_capture,c.input_hash AS commitment_hash,m.health_record_id,m.qty,m.quality,h.processing_status,
 COALESCE(n.observed_at=c.observed_at AND n.covered_interval_start=c.covered_interval_start AND n.covered_interval_end=c.covered_interval_end AND
   m.quality='ok' AND m.origin='live' AND abs(m.qty::double precision-n.duration_hours)<0.000001 AND h.processing_status='complete',false) AS owner_common,
 CASE WHEN n.diagnostics ? 'night_ingest_versions' AND jsonb_typeof(n.diagnostics->'night_ingest_versions') IS DISTINCT FROM 'object' THEN true
 ELSE COALESCE(n.diagnostics->'night_ingest_versions' ? n.source,false) END AS journal_present,
 COALESCE(CASE WHEN (n.diagnostics->'night_ingest_versions'->n.source->>'record_id') ~ '^[0-9]+$' THEN
   (n.diagnostics->'night_ingest_versions'->n.source->>'record_id')::numeric=m.health_record_id ELSE false END AND
   n.diagnostics->'night_ingest_versions'->n.source->>'active'=c.input_hash AND
   CASE WHEN pg_input_is_valid(n.diagnostics->'night_ingest_versions'->n.source->>'received_at','timestamp with time zone') THEN
     (n.diagnostics->'night_ingest_versions'->n.source->>'received_at')::timestamptz=h.received_at ELSE false END,false) AS journal_bound,
 CASE WHEN n.duration_hours BETWEEN 0.0001 AND 24 AND m.quality IS NOT NULL THEN
  n.night_hash=encode(sha256(convert_to(concat_ws(chr(31),c.input_hash,c.metric_date,c.source,
    trim_scale(round(n.duration_hours::numeric,8-floor(log(abs(n.duration_hours)::numeric))::int))::text,m.quality)
    || CASE WHEN n.diagnostics->>'candidate_state'='conflicting' THEN chr(31)||'conflicting' ELSE '' END,'UTF8')),'hex')
 ELSE false END AS fingerprint_bound
 FROM night n LEFT JOIN night_sleep_coverage_commitments c ON c.wake_date=n.wake_date AND c.source=n.source AND c.source_epoch=n.source_epoch AND c.coverage_generation=n.coverage_generation
 LEFT JOIN metric_points m ON m.metric_name='night_sleep_total' AND m.date=c.metric_date AND m.source=c.source
 LEFT JOIN health_records h ON h.id=m.health_record_id
), packet AS MATERIALIZED (
 SELECT h.id,CASE WHEN octet_length(h.payload)<=5000000 AND h.payload IS JSON OBJECT THEN h.payload::jsonb END AS body,
 COALESCE(octet_length(h.payload)>5000000,false) AS packet_oversize
 FROM health_records h JOIN (SELECT DISTINCT health_record_id FROM bound WHERE canonical_night_eligible AND owner_common AND fingerprint_bound AND (NOT journal_present OR journal_bound)) ids ON ids.health_record_id=h.id
), checked AS MATERIALIZED (
 SELECT b.*,p.body,p.packet_oversize,COALESCE((SELECT count(*)>0 AND count(DISTINCT c)=1 AND bool_and((c->>'source_epoch'=b.source_epoch AND c->>'metric_date'=b.metric_date AND c->>'sync_generation'=b.coverage_generation AND c->>'capture_completeness'=b.commitment_capture) IS TRUE) FROM jsonb_array_elements(CASE WHEN jsonb_typeof(p.body->'data'->'night_sleep_coverage')='array' THEN p.body->'data'->'night_sleep_coverage' ELSE '[]'::jsonb END) k(c)
 WHERE c->>'source'=b.source AND c->>'wake_date'=b.wake_date),false) AS packet_coverage
 ,COALESCE((SELECT count(*)>0 AND count(DISTINCT jsonb_build_array(pt->>'date',pt->'qty',mt->>'units'))=1 AND bool_and(CASE WHEN pt->>'date'=b.metric_date AND mt->>'units'='hr' AND jsonb_typeof(pt->'qty')='number' AND pg_input_is_valid(pt->>'qty','real') AND pg_input_is_valid(pt->>'qty','double precision') THEN (pt->>'qty')::real=b.qty AND abs((pt->>'qty')::double precision-b.duration_hours)<0.000001 ELSE false END)
 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(p.body->'data'->'metrics')='array' THEN p.body->'data'->'metrics' ELSE '[]'::jsonb END) x(mt)
 CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(mt->'data')='array' THEN mt->'data' ELSE '[]'::jsonb END) y(pt)
 WHERE mt->>'name'='night_sleep_total' AND pt->>'source'=b.source AND
 (pt->>'date'=b.metric_date OR left(pt->>'date',10)=left(b.metric_date,10) OR
 CASE WHEN pt->>'date' ~ '(Z|[+-][0-9]{2}:?[0-9]{2})$' AND pg_input_is_valid(pt->>'date','timestamp with time zone') THEN ((pt->>'date')::timestamptz AT TIME ZONE b.tz)::date=b.day ELSE false END)),false) AS packet_total,
 COALESCE((SELECT count(*)>0 AND count(DISTINCT c)=1 AND bool_and(
   CASE WHEN c->>'source_epoch'=b.source_epoch AND c->>'metric_date'=b.metric_date AND c->>'sync_generation'=b.coverage_generation AND c->>'capture_completeness'=b.commitment_capture
    AND c->>'covered_interval_start' ~ '(Z|[+-][0-9]{2}:?[0-9]{2})$'
    AND c->>'covered_interval_end' ~ '(Z|[+-][0-9]{2}:?[0-9]{2})$'
    AND c->>'covered_interval_start' !~ '[.][0-9]{7,}' AND c->>'covered_interval_end' !~ '[.][0-9]{7,}'
    AND pg_input_is_valid(c->>'covered_interval_start','timestamp with time zone')
    AND pg_input_is_valid(c->>'covered_interval_end','timestamp with time zone')
   THEN (c->>'covered_interval_start')::timestamptz=b.covered_interval_start
    AND (c->>'covered_interval_end')::timestamptz=b.covered_interval_end
    AND b.commitment_hash=encode(sha256(convert_to(concat_ws(chr(31),b.wake_date,b.metric_date,b.source,b.source_epoch,b.commitment_capture,b.coverage_generation,
     to_char(b.covered_interval_start AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS') || CASE WHEN to_char(b.covered_interval_start AT TIME ZONE 'UTC','US')='000000' THEN '' ELSE '.'||rtrim(to_char(b.covered_interval_start AT TIME ZONE 'UTC','US'),'0') END || 'Z',
     to_char(b.covered_interval_end AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS') || CASE WHEN to_char(b.covered_interval_end AT TIME ZONE 'UTC','US')='000000' THEN '' ELSE '.'||rtrim(to_char(b.covered_interval_end AT TIME ZONE 'UTC','US'),'0') END || 'Z'),'UTF8')),'hex')
   ELSE false END)
 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(p.body->'data'->'night_sleep_coverage')='array' THEN p.body->'data'->'night_sleep_coverage' ELSE '[]'::jsonb END) x(c)
 WHERE c->>'source'=b.source AND c->>'wake_date'=b.wake_date),false) AS legacy_coverage_hash
 FROM bound b LEFT JOIN packet p ON p.id=b.health_record_id
), metric AS (
 SELECT b.day,b.tz,b.source,b.covered_interval_start,b.covered_interval_end,x.metric FROM checked b CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(b.body->'data'->'metrics')='array' THEN b.body->'data'->'metrics' ELSE '[]'::jsonb END) x(metric)
 WHERE b.owner_common AND b.fingerprint_bound AND (b.journal_bound OR (NOT b.journal_present AND b.legacy_coverage_hash)) AND b.packet_coverage AND b.packet_total
 AND x.metric->>'name' IN ('sleep_core','sleep_deep','sleep_rem','sleep_unspecified','sleep_awake') AND x.metric->>'units'='hr'
 AND lower(coalesce(x.metric->>'grain','')) NOT SIMILAR TO '%(hour|day|daily|minute|bucket|aggregat|week|month|year)%'
), sample AS (
 SELECT m.day,m.tz,m.covered_interval_start,m.covered_interval_end,m.metric->>'name' AS stage,x.point,
 CASE WHEN x.point->>'date' ~ '(Z|[+-][0-9]{2}:?[0-9]{2})$' AND pg_input_is_valid(x.point->>'date','timestamp with time zone') THEN (x.point->>'date')::timestamptz END AS start_at,
 CASE WHEN jsonb_typeof(x.point->'qty')='number' AND pg_input_is_valid(x.point->>'qty','double precision') THEN (x.point->>'qty')::double precision END AS hours
 FROM metric m CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(m.metric->'data')='array' THEN m.metric->'data' ELSE '[]'::jsonb END) x(point)
 WHERE x.point->>'source'=m.source
), sample_validated AS (
 SELECT *,start_at IS NULL OR hours IS NULL OR hours<0 OR hours>24 AS invalid FROM sample
), invalid_days AS (SELECT day,bool_or(invalid) AS has_invalid,
 bool_or(CASE WHEN NOT invalid AND hours>0 AND substring(point->>'date',12,8)<>'00:00:00' AND (start_at AT TIME ZONE tz)::time<>time '00:00' THEN
 start_at<covered_interval_end AND start_at+hours*interval '1 hour'>covered_interval_start AND
 (start_at<covered_interval_start OR start_at+hours*interval '1 hour'>covered_interval_end) ELSE false END) AS boundary_crossing
 FROM sample_validated GROUP BY day),
ranges_raw AS (
 SELECT DISTINCT day,stage,start_at,start_at+hours*interval '1 hour' AS end_at FROM sample_validated
 WHERE NOT invalid AND hours>0 AND substring(point->>'date',12,8)<>'00:00:00' AND (start_at AT TIME ZONE tz)::time<>time '00:00'
 AND start_at>=covered_interval_start AND start_at+hours*interval '1 hour'<=covered_interval_end
), prior AS (
 SELECT *,max(end_at) OVER(PARTITION BY day ORDER BY start_at,end_at,stage ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING) previous_end FROM ranges_raw
), grouped AS (
 SELECT *,sum(CASE WHEN previous_end IS NULL OR start_at>previous_end+interval '30 minutes' THEN 1 ELSE 0 END)
 OVER(PARTITION BY day ORDER BY start_at,end_at,stage) group_id FROM prior
), ranges AS (
 SELECT day,group_id,stage,range_agg(tstzrange(start_at,end_at,'[)')) spans FROM grouped GROUP BY day,group_id,stage
), merged AS (
 SELECT day,group_id,stage,lower(span) start_at,upper(span) end_at FROM ranges CROSS JOIN LATERAL unnest(spans) r(span)
), session_bounds AS (
 SELECT day,group_id,min(start_at) start_at,min(start_at) FILTER(WHERE stage<>'sleep_awake') onset,
 max(end_at) FILTER(WHERE stage<>'sleep_awake') asleep_end,
 sum(extract(epoch FROM end_at-start_at)) FILTER(WHERE stage<>'sleep_awake') asleep_seconds,
 bool_or(EXISTS(SELECT FROM merged b WHERE b.day=a.day AND b.group_id=a.group_id AND b.stage<>a.stage
  AND b.start_at<a.end_at-interval '1 second' AND a.start_at<b.end_at-interval '1 second')) conflict
 FROM merged a GROUP BY day,group_id
), ranked AS (
 SELECT *,row_number() OVER(PARTITION BY s.day ORDER BY asleep_seconds DESC NULLS LAST,onset) rank,
 lead(asleep_seconds) OVER(PARTITION BY s.day ORDER BY asleep_seconds DESC NULLS LAST,onset) runner_up
 FROM session_bounds s JOIN calendar c USING(day) WHERE onset IS NOT NULL AND (asleep_end AT TIME ZONE c.tz)::date=s.day
)
SELECT b.day,b.tz,b.source,b.source_epoch,b.duration_hours,b.canonical_night_eligible,r.onset,r.asleep_end,
 CASE WHEN b.wake_date IS NULL THEN 'no_night'
 WHEN NOT b.canonical_night_eligible THEN 'night_ineligible'
 WHEN NOT b.owner_common OR (b.journal_present AND NOT b.journal_bound) THEN 'unbound_current_packet'
 WHEN NOT COALESCE(b.fingerprint_bound,false) THEN 'fingerprint_unverified'
 WHEN b.packet_oversize THEN 'oversize_packet'
 WHEN b.body IS NULL THEN 'invalid_packet'
 WHEN NOT b.packet_coverage OR NOT b.packet_total OR (NOT b.journal_present AND NOT b.legacy_coverage_hash) THEN 'unbound_current_packet'
 WHEN coalesce(i.has_invalid,false) THEN 'invalid_stage_sample'
 WHEN coalesce(i.boundary_crossing,false) THEN 'coverage_boundary_crossing'
 WHEN r.onset IS NULL AND EXISTS(SELECT FROM session_bounds s WHERE s.day=b.day AND s.onset IS NOT NULL) THEN 'wake_date_mismatch'
 WHEN r.onset IS NULL THEN 'no_standalone_stages'
 WHEN r.runner_up IS NOT NULL AND r.asleep_seconds-r.runner_up<=60 THEN 'ambiguous_session'
 WHEN r.conflict THEN 'overlapping_labels'
 WHEN r.start_at<r.onset THEN 'awake_at_boundary'
 WHEN (r.asleep_end AT TIME ZONE b.tz)::date<>b.day THEN 'wake_date_mismatch'
 WHEN abs(r.asleep_seconds-b.duration_hours*3600)>60 THEN 'duration_mismatch'
 ELSE 'candidate_proxy' END AS reason,
 false AS canonical_binding,
 CASE WHEN b.owner_common AND b.fingerprint_bound AND b.packet_coverage AND b.packet_total AND b.journal_bound THEN 'journal_bound'
 WHEN b.owner_common AND b.fingerprint_bound AND b.packet_coverage AND b.packet_total AND NOT b.journal_present AND b.legacy_coverage_hash THEN 'legacy_content_equivalent' END AS packet_provenance
FROM checked b LEFT JOIN ranked r ON r.day=b.day AND r.rank=1 LEFT JOIN invalid_days i ON i.day=b.day
