CREATE SCHEMA synthetic;
SET search_path=synthetic,public;

CREATE TABLE research_parameters(start_date date,end_date date,tz text);
INSERT INTO research_parameters VALUES ('2026-11-01','2026-11-30','Europe/Belgrade');
CREATE TABLE health_records(
 id bigserial primary key,received_at timestamptz NOT NULL,payload text NOT NULL,
 processing_status text NOT NULL DEFAULT 'complete'
);
CREATE TABLE metric_points(
 id bigserial primary key,health_record_id bigint NOT NULL REFERENCES health_records(id),
 metric_name text NOT NULL,units text,date text NOT NULL,qty real,source text,
 origin text NOT NULL DEFAULT 'live',quality text NOT NULL DEFAULT 'ok',
 UNIQUE(metric_name,date,source)
);
CREATE TABLE completed_night_sleep(
 wake_date text primary key,duration_hours double precision NOT NULL,source text NOT NULL,
 source_epoch text NOT NULL,input_hash text NOT NULL,capture_completeness text NOT NULL,
 duration_assessment text NOT NULL,finalization_state text NOT NULL,claim_eligibility text NOT NULL,
 coverage_generation text,covered_interval_start timestamptz,covered_interval_end timestamptz,
 observed_at timestamptz NOT NULL,diagnostics jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE TABLE night_sleep_coverage_commitments(
 wake_date text NOT NULL,source text NOT NULL,metric_date text NOT NULL,source_epoch text NOT NULL,
 capture_completeness text NOT NULL,coverage_generation text NOT NULL,
 covered_interval_start timestamptz NOT NULL,covered_interval_end timestamptz NOT NULL,
 observed_at timestamptz NOT NULL,input_hash text NOT NULL,PRIMARY KEY(wake_date,source)
);

-- Each case is a raw accepted `/health` packet that owns its selected canonical
-- night total. The exact Go fingerprint contracts are reproduced below so
-- packet ownership, coverage receipt, and canonical projection all agree.
CREATE FUNCTION add_packet_case(
 p_day date,p_hours double precision,p_stages jsonb,p_extra jsonb DEFAULT '[]'::jsonb,
 p_canonical boolean DEFAULT true,p_coverage boolean DEFAULT true,
 p_source text DEFAULT 'watch',p_stage_source text DEFAULT NULL,
 p_grain text DEFAULT NULL,p_epoch text DEFAULT 'epoch-a'
) RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE
  metric_date text := (p_day::text || ' 07:00:00 +0100');
  covered_start timestamptz := (p_day::timestamp - interval '1 day' + time '12:00') AT TIME ZONE 'Europe/Belgrade';
  covered_end timestamptz := (p_day::timestamp + time '12:00') AT TIME ZONE 'Europe/Belgrade';
  observed timestamptz := (p_day::timestamp + time '11:00') AT TIME ZONE 'Europe/Belgrade';
  raw_coverage_hash text := 'raw-coverage-' || p_day::text;
  coverage_hash text;
  generation text := 'generation-' || p_day::text;
  stage_source_value text := coalesce(p_stage_source,p_source);
  metric_array jsonb;
  packet jsonb;
  record_id bigint;
  active_hash text;
  night_hash text;
BEGIN
  metric_array := jsonb_build_array(jsonb_build_object(
    'name','night_sleep_total','units','hr','data',jsonb_build_array(
      jsonb_build_object('date',metric_date,'qty',p_hours,'source',p_source)
    )
  ));
  IF p_stages IS NOT NULL THEN
    metric_array := metric_array || jsonb_build_array(jsonb_build_object(
      'name','sleep_core','units','hr','data',p_stages,
      'grain',p_grain
    ));
  END IF;
  metric_array := metric_array || coalesce(p_extra,'[]'::jsonb);
  packet := jsonb_build_object('data',jsonb_build_object(
    'metrics',metric_array,
    'night_sleep_coverage',CASE WHEN p_coverage THEN jsonb_build_array(jsonb_build_object(
      'wake_date',p_day::text,'metric_date',metric_date,'source',p_source,
      'source_epoch',p_epoch,'sync_generation',generation,'capture_completeness','complete',
      'covered_interval_start',covered_start,'covered_interval_end',covered_end
    )) ELSE '[]'::jsonb END
  ));
  INSERT INTO health_records(received_at,payload,processing_status)
    VALUES(observed,packet::text,'complete') RETURNING id INTO record_id;
  INSERT INTO metric_points(health_record_id,metric_name,units,date,qty,source,quality)
    VALUES(record_id,'night_sleep_total','hr',metric_date,p_hours,p_source,'ok');
  active_hash := encode(sha256(convert_to(raw_coverage_hash || chr(31) || 'hr' || chr(31) || p_hours::real::text,'UTF8')),'hex');
  coverage_hash := active_hash;
  IF p_coverage THEN
    INSERT INTO night_sleep_coverage_commitments VALUES(
      p_day::text,p_source,metric_date,p_epoch,'complete',generation,
      covered_start,covered_end,observed,coverage_hash
    );
  END IF;
  IF p_canonical THEN
    night_hash := encode(sha256(convert_to(concat_ws(chr(31),coverage_hash,metric_date,p_source,
      trim_scale(round(p_hours::numeric,8-floor(log(abs(p_hours)::numeric))::int))::text,'ok'),'UTF8')),'hex');
    INSERT INTO completed_night_sleep(
      wake_date,duration_hours,source,source_epoch,input_hash,capture_completeness,
      duration_assessment,finalization_state,claim_eligibility,coverage_generation,
      covered_interval_start,covered_interval_end,observed_at,diagnostics
    ) VALUES (
      p_day::text,p_hours,p_source,p_epoch,night_hash,'complete','plausible','final','eligible',generation,
      covered_start,covered_end,observed,jsonb_build_object('night_ingest_versions',
        jsonb_build_object(p_source,jsonb_build_object('record_id',record_id,'active',active_hash,'received_at',observed)))
    );
  END IF;
  RETURN record_id;
END $$;

-- Normal sleep night: source/epoch, exact raw owner, commitment and packet receipt.
SELECT add_packet_case('2026-11-01',8,jsonb_build_array(
 jsonb_build_object('date','2026-10-31T22:00:00+01:00','qty',4,'source','watch'),
 jsonb_build_object('date','2026-11-01T02:00:00+01:00','qty',4,'source','watch')));
-- Main night plus a daytime nap: longest local session remains the onset proxy.
SELECT add_packet_case('2026-11-02',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-01T22:00:00+01:00','qty',4,'source','watch'),
 jsonb_build_object('date','2026-11-02T02:00:00+01:00','qty',4,'source','watch'),
 jsonb_build_object('date','2026-11-02T14:00:00+01:00','qty',1,'source','watch')));
-- Contradictory stage labels overlap by more than the tolerance.
SELECT add_packet_case('2026-11-03',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-02T22:00:00+01:00','qty',4,'source','watch'),
 jsonb_build_object('date','2026-11-03T01:00:00+01:00','qty',4,'source','watch')),
 jsonb_build_array(jsonb_build_object('name','sleep_deep','units','hr','data',jsonb_build_array(
 jsonb_build_object('date','2026-11-03T01:30:00+01:00','qty',1,'source','watch')))));
-- Wake date is evaluated in the requested local timezone, not UTC.
SELECT add_packet_case('2026-11-04',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-03T13:00:00+01:00','qty',4,'source','watch'),
 jsonb_build_object('date','2026-11-03T17:00:00+01:00','qty',4,'source','watch')));
-- Canonical duration disagrees with stage-derived asleep duration.
SELECT add_packet_case('2026-11-05',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-04T22:00:00+01:00','qty',3.5,'source','watch'),
 jsonb_build_object('date','2026-11-05T02:00:00+01:00','qty',3.5,'source','watch')));
-- Midnight aggregate masquerading as an interval is rejected.
SELECT add_packet_case('2026-11-06',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-05T00:00:00+01:00','qty',8,'source','watch')));
-- Wrong source within the otherwise correct packet cannot be mixed into a night.
SELECT add_packet_case('2026-11-07',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-06T22:00:00+01:00','qty',4,'source','other-watch'),
 jsonb_build_object('date','2026-11-07T02:00:00+01:00','qty',4,'source','other-watch')),
 '[]',true,true,'watch');
-- No standalone interval metrics: nested sleep_analysis summary is not stages.
SELECT add_packet_case('2026-11-08',8,NULL,'[]',true,true,'watch','watch',NULL,'epoch-a');
UPDATE health_records SET payload=jsonb_set(payload::jsonb,'{data,metrics}',
 jsonb_build_array(jsonb_build_object('name','night_sleep_total','units','hr','data',jsonb_build_array(
 jsonb_build_object('date','2026-11-08 07:00:00 +0100','qty',8,'source','watch'))),
 jsonb_build_object('name','sleep_analysis','units','hr','data',jsonb_build_array(
 jsonb_build_object('date','2026-11-08 07:00:00 +0100','totalSleep',8,'source','watch')))))::text
WHERE id=(SELECT max(id) FROM health_records WHERE payload::jsonb->'data'->'night_sleep_coverage'->0->>'wake_date'='2026-11-08');
-- Explicit daily aggregate grain must not be reconstructed as interval data.
SELECT add_packet_case('2026-11-09',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-08T22:00:00+01:00','qty',8,'source','watch')),
 '[]',true,true,'watch',NULL,'daily');
-- Legacy receipt still exists, but its record id is not current canonical owner.
SELECT add_packet_case('2026-11-10',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-09T22:00:00+01:00','qty',4,'source','watch'),
 jsonb_build_object('date','2026-11-10T02:00:00+01:00','qty',4,'source','watch')));
UPDATE completed_night_sleep SET diagnostics=jsonb_set(diagnostics,
 '{night_ingest_versions,watch,record_id}',to_jsonb((SELECT min(id) FROM health_records WHERE id=(SELECT min(health_record_id) FROM metric_points WHERE date='2026-11-10 07:00:00 +0100'))+1000))
WHERE wake_date='2026-11-10';
-- Missing same-packet coverage cannot be repaired from another receipt.
SELECT add_packet_case('2026-11-11',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-10T22:00:00+01:00','qty',4,'source','watch'),
 jsonb_build_object('date','2026-11-11T02:00:00+01:00','qty',4,'source','watch')),
 '[]',true,false);
-- Stage sample without an explicit timezone offset is invalid evidence.
SELECT add_packet_case('2026-11-12',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-11T22:00:00','qty',8,'source','watch')));
-- Out-of-coverage samples cannot qualify even if duration matches.
SELECT add_packet_case('2026-11-13',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-12T22:00:00+01:00','qty',8,'source','watch')));
UPDATE night_sleep_coverage_commitments SET covered_interval_end='2026-11-13 00:00:00+01'
WHERE wake_date='2026-11-13';
UPDATE completed_night_sleep SET covered_interval_end='2026-11-13 00:00:00+01'
WHERE wake_date='2026-11-13';
-- Corrected current packet plus a later raw replay carrying older stages:
-- metric ownership remains the selected current record, so replay content is ignored.
SELECT add_packet_case('2026-11-14',7.5,jsonb_build_array(
 jsonb_build_object('date','2026-11-13T22:00:00+01:00','qty',3.5,'source','watch'),
 jsonb_build_object('date','2026-11-14T01:30:00+01:00','qty',4,'source','watch')));
-- Awake at the leading edge is preserved as an ambiguity, not onset.
SELECT add_packet_case('2026-11-15',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-14T22:00:00+01:00','qty',8,'source','watch')),
 jsonb_build_array(jsonb_build_object('name','sleep_awake','units','hr','data',jsonb_build_array(
 jsonb_build_object('date','2026-11-14T21:45:00+01:00','qty',0.25,'source','watch')))));
-- Broken JSON from a record otherwise selected by canonical diagnostics.
SELECT add_packet_case('2026-11-16',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-15T22:00:00+01:00','qty',8,'source','watch')));
UPDATE health_records SET payload='not-json'
WHERE id=(SELECT health_record_id FROM metric_points WHERE metric_name='night_sleep_total'
 AND date='2026-11-16 07:00:00 +0100');
-- Current corrected owner remains the source of stages after a stale packet
-- replay. The replay is retained as raw history but has no selected point.
INSERT INTO health_records(received_at,payload,processing_status)
SELECT received_at - interval '1 day',jsonb_set(payload::jsonb,'{data,metrics}',
 jsonb_build_array(jsonb_build_object('name','sleep_core','units','hr','data',jsonb_build_array(
 jsonb_build_object('date','2026-11-13T22:00:00+01:00','qty',8,'source','watch')))))::text,'complete'
FROM health_records WHERE id=(SELECT health_record_id FROM metric_points WHERE metric_name='night_sleep_total'
 AND date='2026-11-14 07:00:00 +0100');
-- Non-hour stage units and a packet epoch mismatch are not acceptable.
SELECT add_packet_case('2026-11-17',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-16T22:00:00+01:00','qty',8,'source','watch')));
UPDATE health_records SET payload=jsonb_set(payload::jsonb,'{data,metrics,1,units}','"min"')::text
WHERE id=(SELECT health_record_id FROM metric_points WHERE metric_name='night_sleep_total'
 AND date='2026-11-17 07:00:00 +0100');
SELECT add_packet_case('2026-11-18',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-17T22:00:00+01:00','qty',8,'source','watch')));
UPDATE health_records SET payload=jsonb_set(payload::jsonb,'{data,night_sleep_coverage,0,source_epoch}','"epoch-b"')::text
WHERE id=(SELECT health_record_id FROM metric_points WHERE metric_name='night_sleep_total'
 AND date='2026-11-18 07:00:00 +0100');
-- A malformed calendar date with a valid-looking offset is invalid evidence.
SELECT add_packet_case('2026-11-19',8,jsonb_build_array(
 jsonb_build_object('date','2026-02-30T22:00:00+01:00','qty',8,'source','watch')));

-- Convert a current synthetic row into the pre-ledger storage shape. Before
-- `night_ingest_versions`, the commitment retained the adapter's bound
-- coverage hash and completed_night_sleep hashed that value with the selected
-- metric identity, duration and quality. Diagnostics had no receipt ID.
CREATE FUNCTION legacyize_packet_case(p_day date) RETURNS void LANGUAGE plpgsql AS $$
DECLARE
  source_name text;
  metric_stamp text;
  epoch_name text;
  completeness text;
  generation text;
  window_start timestamptz;
  window_end timestamptz;
  duration double precision;
  quality_value text;
  raw_hash text;
  canonical_hash text;
BEGIN
  SELECT c.source,c.metric_date,c.source_epoch,c.capture_completeness,c.coverage_generation,
         c.covered_interval_start,c.covered_interval_end,n.duration_hours,m.quality
  INTO source_name,metric_stamp,epoch_name,completeness,generation,window_start,window_end,duration,quality_value
  FROM night_sleep_coverage_commitments c
  JOIN completed_night_sleep n USING(wake_date,source)
  JOIN metric_points m ON m.metric_name='night_sleep_total' AND m.date=c.metric_date AND m.source=c.source
  WHERE c.wake_date=p_day::text;
  raw_hash := encode(sha256(convert_to(concat_ws(chr(31),p_day::text,metric_stamp,source_name,epoch_name,completeness,generation,
    to_char(window_start AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS') || CASE WHEN to_char(window_start AT TIME ZONE 'UTC','US')='000000' THEN '' ELSE '.'||rtrim(to_char(window_start AT TIME ZONE 'UTC','US'),'0') END || 'Z',
    to_char(window_end AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS') || CASE WHEN to_char(window_end AT TIME ZONE 'UTC','US')='000000' THEN '' ELSE '.'||rtrim(to_char(window_end AT TIME ZONE 'UTC','US'),'0') END || 'Z'),'UTF8')),'hex');
  canonical_hash := encode(sha256(convert_to(concat_ws(chr(31),raw_hash,metric_stamp,source_name,
    trim_scale(round(duration::numeric,8-floor(log(abs(duration)::numeric))::int))::text,quality_value),'UTF8')),'hex');
  UPDATE night_sleep_coverage_commitments SET input_hash=raw_hash WHERE wake_date=p_day::text;
  UPDATE completed_night_sleep SET input_hash=canonical_hash,
    diagnostics=(diagnostics-'night_ingest_versions')||jsonb_build_object('candidate_state','selected','coverage_generation',generation)
  WHERE wake_date=p_day::text;
END $$;

-- Legacy happy path and isolated fail-closed controls.
SELECT add_packet_case('2026-11-20',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-19T22:00:00+01:00','qty',4,'source','watch'),
 jsonb_build_object('date','2026-11-20T02:00:00+01:00','qty',4,'source','watch')));
SELECT legacyize_packet_case('2026-11-20');
-- A present journal which names another owner must never fall back to legacy.
SELECT add_packet_case('2026-11-21',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-20T22:00:00+01:00','qty',8,'source','watch')));
UPDATE completed_night_sleep SET diagnostics=jsonb_set(diagnostics,'{night_ingest_versions,watch,record_id}',to_jsonb(999999)) WHERE wake_date='2026-11-21';
-- Canonical hash mismatch is distinct from a missing legacy ledger.
SELECT add_packet_case('2026-11-22',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-21T22:00:00+01:00','qty',8,'source','watch')));
SELECT legacyize_packet_case('2026-11-22');
UPDATE completed_night_sleep SET input_hash='wrong-raw-hash' WHERE wake_date='2026-11-22';
-- Same packet tuple but a changed raw coverage clock fails the exact hash.
SELECT add_packet_case('2026-11-23',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-22T22:00:00+01:00','qty',8,'source','watch')));
SELECT legacyize_packet_case('2026-11-23');
UPDATE health_records SET payload=jsonb_set(payload::jsonb,'{data,night_sleep_coverage,0,metric_date}',to_jsonb('2026-11-23 08:00:00 +0100'::text))::text
WHERE id=(SELECT health_record_id FROM metric_points WHERE metric_name='night_sleep_total' AND date='2026-11-23 07:00:00 +0100');
-- Coverage bounds must match both the legacy hash and canonical row.
SELECT add_packet_case('2026-11-24',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-23T22:00:00+01:00','qty',8,'source','watch')));
SELECT legacyize_packet_case('2026-11-24');
UPDATE health_records SET payload=jsonb_set(payload::jsonb,'{data,night_sleep_coverage,0,covered_interval_start}',to_jsonb('2026-11-22T11:00:00Z'::text))::text
WHERE id=(SELECT health_record_id FROM metric_points WHERE metric_name='night_sleep_total' AND date='2026-11-24 07:00:00 +0100');
-- The raw packet total must be the same selected canonical point.
SELECT add_packet_case('2026-11-25',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-24T22:00:00+01:00','qty',8,'source','watch')));
SELECT legacyize_packet_case('2026-11-25');
UPDATE health_records SET payload=jsonb_set(payload::jsonb,'{data,metrics,0,data,0,qty}','7.5')::text
WHERE id=(SELECT health_record_id FROM metric_points WHERE metric_name='night_sleep_total' AND date='2026-11-25 07:00:00 +0100');
-- A replayed owner key cannot reuse an otherwise valid packet from the prior day.
SELECT add_packet_case('2026-11-26',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-25T22:00:00+01:00','qty',8,'source','watch')));
SELECT legacyize_packet_case('2026-11-26');
UPDATE metric_points SET health_record_id=(SELECT health_record_id FROM metric_points WHERE metric_name='night_sleep_total' AND date='2026-11-25 07:00:00 +0100')
WHERE metric_name='night_sleep_total' AND date='2026-11-26 07:00:00 +0100';
-- Duplicate coverage for the same wake/source with a different epoch/generation
-- is conflicting evidence, even when one row individually matches.
SELECT add_packet_case('2026-11-27',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-26T22:00:00+01:00','qty',8,'source','watch')));
SELECT legacyize_packet_case('2026-11-27');
UPDATE health_records SET payload=jsonb_set(payload::jsonb,'{data,night_sleep_coverage}',
 payload::jsonb->'data'->'night_sleep_coverage' || jsonb_build_array(
   (payload::jsonb->'data'->'night_sleep_coverage'->0) || jsonb_build_object('sync_generation','replayed-generation')))::text
WHERE id=(SELECT health_record_id FROM metric_points WHERE metric_name='night_sleep_total' AND date='2026-11-27 07:00:00 +0100');
-- Conflicting raw night totals for one source/calendar date must not be
-- resolved by picking the first array element.
SELECT add_packet_case('2026-11-28',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-27T22:00:00+01:00','qty',8,'source','watch')));
SELECT legacyize_packet_case('2026-11-28');
UPDATE health_records SET payload=jsonb_set(payload::jsonb,'{data,metrics,0,data}',
 payload::jsonb->'data'->'metrics'->0->'data' || jsonb_build_array(
   jsonb_build_object('date','2026-11-28 07:00:00 +0100','qty',9,'source','watch')))::text
WHERE id=(SELECT health_record_id FROM metric_points WHERE metric_name='night_sleep_total' AND date='2026-11-28 07:00:00 +0100');
-- The same local date/source with a conflicting units label is also rejected.
SELECT add_packet_case('2026-11-29',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-28T22:00:00+01:00','qty',8,'source','watch')));
SELECT legacyize_packet_case('2026-11-29');
UPDATE health_records SET payload=jsonb_set(payload::jsonb,'{data,metrics}',
 payload::jsonb->'data'->'metrics' || jsonb_build_array(jsonb_build_object(
   'name','night_sleep_total','units','min','data',jsonb_build_array(
     jsonb_build_object('date','2026-11-29 07:00:00 +0100','qty',8,'source','watch')))))::text
WHERE id=(SELECT health_record_id FROM metric_points WHERE metric_name='night_sleep_total' AND date='2026-11-29 07:00:00 +0100');
-- Two timestamp spellings that map to one tenant-local calendar date remain a
-- conflicting raw total even when the numeric quantities happen to agree.
SELECT add_packet_case('2026-11-30',8,jsonb_build_array(
 jsonb_build_object('date','2026-11-29T22:00:00+01:00','qty',8,'source','watch')));
SELECT legacyize_packet_case('2026-11-30');
UPDATE health_records SET payload=jsonb_set(payload::jsonb,'{data,metrics,0,data}',
 payload::jsonb->'data'->'metrics'->0->'data' || jsonb_build_array(
   jsonb_build_object('date','2026-11-29T23:30:00Z','qty',8,'source','watch')))::text
WHERE id=(SELECT health_record_id FROM metric_points WHERE metric_name='night_sleep_total' AND date='2026-11-30 07:00:00 +0100');
