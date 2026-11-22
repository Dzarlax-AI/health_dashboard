-- Private intermediate rows. Consume only through an aggregate in production.
-- research_parameters is exactly one row: start_date date, end_date date, tz text.
WITH calendar AS (
 SELECT d::date AS day,p.tz FROM research_parameters p CROSS JOIN LATERAL generate_series(p.start_date,p.end_date,interval '1 day') d
), nights AS (
 SELECT c.day,c.tz,n.source,n.source_epoch,n.coverage_generation,n.duration_hours,n.capture_completeness,n.duration_assessment,n.finalization_state,n.claim_eligibility,n.wake_date,
        d.stage_conflict,d.sleep_seconds,s.snapshot
 FROM calendar c LEFT JOIN completed_night_sleep n ON n.wake_date=c.day::text
 LEFT JOIN sleep_rollout_daily d ON d.wake_date=c.day
 LEFT JOIN sleep_rollout_candidates s ON s.id=d.candidate AND s.revision=d.revision AND s.wake_date=d.wake_date
), stage AS (
 SELECT n.day,n.tz,x.kind,x.start_at,x.end_at FROM nights n
 CROSS JOIN LATERAL jsonb_to_recordset(CASE WHEN jsonb_typeof(n.snapshot->'stages')='array' THEN n.snapshot->'stages' ELSE '[]'::jsonb END)
 AS x(kind text,start_at timestamptz,end_at timestamptz)
 WHERE x.kind IN ('core','deep','rem','asleep_unspecified','awake') AND x.end_at>x.start_at
), prior AS (
 SELECT *,max(end_at) OVER(PARTITION BY day ORDER BY start_at,end_at,kind ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING) previous_end FROM stage
), grouped AS (
 SELECT *,sum(CASE WHEN previous_end IS NULL OR start_at>previous_end+interval '30 minutes' THEN 1 ELSE 0 END)
 OVER(PARTITION BY day ORDER BY start_at,end_at,kind) group_id FROM prior
), ranges AS (
 SELECT day,group_id,kind,range_agg(tstzrange(start_at,end_at,'[)')) spans FROM grouped GROUP BY day,group_id,kind
), merged AS (
 SELECT day,group_id,kind,lower(span) start_at,upper(span) end_at FROM ranges CROSS JOIN LATERAL unnest(spans) r(span)
), session_bounds AS (
 SELECT day,group_id,min(start_at) start_at,min(start_at) FILTER(WHERE kind<>'awake') onset,
 max(end_at) FILTER(WHERE kind<>'awake') asleep_end,
 sum(extract(epoch FROM end_at-start_at)) FILTER(WHERE kind<>'awake') asleep_seconds,
 bool_or(EXISTS(SELECT FROM merged b WHERE b.day=a.day AND b.group_id=a.group_id AND b.kind<>a.kind
  AND b.start_at<a.end_at-interval '1 second' AND a.start_at<b.end_at-interval '1 second')) conflict
 FROM merged a GROUP BY day,group_id
), ranked AS (
 SELECT *,row_number() OVER(PARTITION BY day ORDER BY asleep_seconds DESC NULLS LAST,onset) rank,
 lead(asleep_seconds) OVER(PARTITION BY day ORDER BY asleep_seconds DESC NULLS LAST,onset) runner_up FROM session_bounds WHERE onset IS NOT NULL
), prepared AS (
 SELECT n.*,r.onset,r.asleep_end,r.asleep_seconds,r.runner_up,r.conflict,(r.start_at<r.onset) begins_awake,
 COALESCE(n.capture_completeness='complete' AND n.duration_assessment='plausible' AND n.finalization_state='final' AND n.claim_eligibility='eligible' AND n.duration_hours>0 AND n.duration_hours<=24,false) AS canonical_night_eligible,
 COALESCE(EXISTS(SELECT FROM jsonb_array_elements(CASE WHEN jsonb_typeof(n.snapshot->'legacy_aliases')='array' THEN n.snapshot->'legacy_aliases' ELSE '[]'::jsonb END) a
   WHERE a->>'source'=n.source AND a->>'source_epoch'=n.source_epoch AND a->>'sync_generation'=n.coverage_generation
   AND n.source<>'' AND n.source_epoch<>'' AND n.coverage_generation<>'')
 AND jsonb_typeof(n.snapshot->'coverage')='object'
 AND (n.snapshot->'coverage'->>'revision')::bigint=(n.snapshot->>'revision')::bigint
 AND (n.snapshot->'coverage'->>'start_at')::timestamptz<=r.onset
 AND (n.snapshot->'coverage'->>'end_at')::timestamptz>=r.asleep_end,false) AS canonical_binding
 FROM nights n LEFT JOIN ranked r ON r.day=n.day AND r.rank=1
), classified AS (
 SELECT *,CASE WHEN wake_date IS NULL THEN 'no_night'
 WHEN NOT canonical_night_eligible THEN 'night_ineligible'
 WHEN snapshot IS NULL OR onset IS NULL THEN 'no_selected_stages'
 WHEN runner_up IS NOT NULL AND asleep_seconds-runner_up<=60 THEN 'ambiguous_session'
 WHEN stage_conflict IS DISTINCT FROM false OR conflict THEN 'overlapping_labels'
 WHEN begins_awake THEN 'awake_at_boundary'
 WHEN (asleep_end AT TIME ZONE tz)::date<>day THEN 'wake_date_mismatch'
 WHEN sleep_seconds IS NULL OR NOT (sleep_seconds>0 AND sleep_seconds<=86400) OR abs(asleep_seconds-sleep_seconds)>60 OR abs(asleep_seconds-duration_hours*3600)>60 THEN 'duration_mismatch'
 ELSE 'candidate_proxy' END reason, NULL::text AS packet_provenance FROM prepared
)
{{ pair_research_onset() }}
