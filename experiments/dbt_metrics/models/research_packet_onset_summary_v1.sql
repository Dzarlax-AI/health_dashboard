-- Compare both methods on identical wake dates using the same workout and answer rules.
WITH old_path AS (
    SELECT * FROM {{ ref('research_stage_onset_rows_v1') }}
), packet_path AS (
    SELECT * FROM {{ ref('research_packet_onset_rows_v1') }}
), evidence AS (
    SELECT p.day, p.source, p.source_epoch, p.reason, p.packet_provenance, p.canonical_night_eligible,
           o.reason = 'candidate_proxy' AS old_onset,
           p.reason = 'candidate_proxy' AS packet_onset,
           p.reason = 'candidate_proxy' AND p.canonical_binding AS packet_strict,
           (o.workout_end <= o.onset) IS TRUE AS old_workout,
           (p.workout_end <= p.onset) IS TRUE AS packet_workout,
           o.after_sleep_label AS old_label, p.after_sleep_label AS packet_label,
           o.onset AS old_start, p.onset AS packet_start,
           o.workout_end AS old_workout_end, p.workout_end AS packet_workout_end,
           abs(extract(epoch FROM p.onset - o.onset)) <= 60
               AND abs(extract(epoch FROM p.asleep_end - o.asleep_end)) <= 60 AS boundaries_agree
    FROM packet_path p JOIN old_path o USING (day)
), pairs AS (
    SELECT *,
           old_onset AND old_workout AND old_label IS NOT NULL AS old_pair,
           packet_onset AND packet_workout AND packet_label IS NOT NULL AS packet_pair
    FROM evidence
), classified AS (
    SELECT *, old_onset AND packet_onset AND
           (NOT boundaries_agree OR old_pair <> packet_pair OR
               (old_pair AND (old_label IS DISTINCT FROM packet_label OR
                              old_workout_end IS DISTINCT FROM packet_workout_end))) AS disagreement
    FROM pairs
), accepted AS (
    SELECT day, source, source_epoch,
           CASE WHEN packet_pair THEN packet_label ELSE old_label END AS label,
           extract(epoch FROM CASE WHEN packet_pair THEN packet_start - packet_workout_end
                                   ELSE old_start - old_workout_end END) / 3600 AS interval_hours
    FROM classified WHERE (old_pair OR packet_pair) AND NOT disagreement
), leave_one AS (
    SELECT d.source,d.source_epoch,d.day,
           avg(a.interval_hours) FILTER (WHERE a.label='positive') - avg(a.interval_hours) FILTER (WHERE a.label='meh') AS mean_diff,
           percentile_cont(0.5) WITHIN GROUP (ORDER BY a.interval_hours) FILTER (WHERE a.label='positive') -
           percentile_cont(0.5) WITHIN GROUP (ORDER BY a.interval_hours) FILTER (WHERE a.label='meh') AS median_diff
    FROM accepted d JOIN accepted a ON a.source=d.source AND a.source_epoch=d.source_epoch AND a.day<>d.day
    WHERE d.label='meh' GROUP BY d.source,d.source_epoch,d.day
), epochs AS (
    SELECT source, source_epoch, count(*) AS nights,
           count(*) FILTER (WHERE label='positive') AS positive,
           count(*) FILTER (WHERE label='meh') AS meh,
           count(*) FILTER (WHERE label='sick') AS sick,
           percentile_cont(0.5) WITHIN GROUP (ORDER BY interval_hours) FILTER (WHERE label='positive') AS positive_median,
           percentile_cont(0.5) WITHIN GROUP (ORDER BY interval_hours) FILTER (WHERE label='meh') AS meh_median,
           CASE WHEN count(*) >= 5 THEN round(min(interval_hours),2) END AS min_hours,
           CASE WHEN count(*) >= 5 THEN round(max(interval_hours),2) END AS max_hours,
           CASE WHEN count(*) FILTER (WHERE label='positive') >= 5 AND
                     count(*) FILTER (WHERE label='meh') >= 5 THEN
               round(avg(interval_hours) FILTER (WHERE label='positive') -
                     avg(interval_hours) FILTER (WHERE label='meh'),2)
           END AS positive_minus_meh_hours
    FROM accepted GROUP BY source, source_epoch
), anonymous_epochs AS (
    SELECT row_number() OVER (ORDER BY e.source,e.source_epoch) AS epoch,
           e.nights,e.positive,e.meh,e.sick,e.min_hours,e.max_hours,e.positive_minus_meh_hours,
           CASE WHEN e.positive>=5 AND e.meh>=5 THEN round(e.positive_median::numeric,2) END AS median_positive_hours,
           CASE WHEN e.positive>=5 AND e.meh>=5 THEN round(e.meh_median::numeric,2) END AS median_meh_hours,
           CASE WHEN e.positive>=5 AND e.meh>=5 THEN round((e.positive_median-e.meh_median)::numeric,2) END AS median_diff_hours,
           CASE WHEN e.positive>=5 AND e.meh>=6 THEN count(l.day) ELSE 0 END AS loo_count,
           CASE WHEN e.positive>=5 AND e.meh>=6 THEN round(min(l.mean_diff),2) END AS loo_mean_min_hours,
           CASE WHEN e.positive>=5 AND e.meh>=6 THEN round(max(l.mean_diff),2) END AS loo_mean_max_hours,
           CASE WHEN e.positive>=5 AND e.meh>=6 THEN round(min(l.median_diff)::numeric,2) END AS loo_median_min_hours,
           CASE WHEN e.positive>=5 AND e.meh>=6 THEN round(max(l.median_diff)::numeric,2) END AS loo_median_max_hours
    FROM epochs e LEFT JOIN leave_one l ON l.source=e.source AND l.source_epoch=e.source_epoch
    GROUP BY e.source,e.source_epoch,e.nights,e.positive,e.meh,e.sick,e.min_hours,e.max_hours,e.positive_minus_meh_hours,e.positive_median,e.meh_median
), provenance AS (
    SELECT packet_provenance, count(*) AS onsets,
           count(*) FILTER (WHERE packet_pair) AS paired,
           count(*) FILTER (WHERE packet_pair AND NOT old_pair AND NOT disagreement) AS new_dates
    FROM classified WHERE packet_onset GROUP BY packet_provenance
), exclusions AS (
    SELECT reason,count(*) AS days FROM classified GROUP BY reason
)
SELECT json_build_object(
    'method_version','packet-onset-v1.1',
    'start_date',(SELECT start_date FROM research_parameters),
    'end_date',(SELECT end_date FROM research_parameters),
    'timezone',(SELECT tz FROM research_parameters),
    'calendar_days',count(*),
    'eligible_nights',count(*) FILTER (WHERE canonical_night_eligible),
    'old_onsets',count(*) FILTER (WHERE old_onset),
    'union_onsets',count(*) FILTER (WHERE old_onset OR packet_onset),
    'union_onset_prior_workout',count(*) FILTER (WHERE (old_onset AND old_workout) OR (packet_onset AND packet_workout)),
    'union_onset_morning_answer',count(*) FILTER (WHERE (old_onset AND old_label IS NOT NULL) OR (packet_onset AND packet_label IS NOT NULL)),
    'packet_onsets',count(*) FILTER (WHERE packet_onset),
    'packet_strict_onsets',count(*) FILTER (WHERE packet_strict),
    'packet_prior_workout',count(*) FILTER (WHERE packet_onset AND packet_workout),
    'packet_morning_answer',count(*) FILTER (WHERE packet_onset AND packet_label IS NOT NULL),
    'old_pairs',count(*) FILTER (WHERE old_pair),
    'packet_pairs',count(*) FILTER (WHERE packet_pair),
    'packet_strict_pairs',count(*) FILTER (WHERE packet_pair AND packet_strict),
    'both_paired',count(*) FILTER (WHERE old_pair AND packet_pair),
    'union_paired_dates',count(*) FILTER (WHERE old_pair OR packet_pair),
    'packet_only_paired',count(*) FILTER (WHERE packet_pair AND NOT old_pair),
    'disagreeing_onset_dates',count(*) FILTER (WHERE old_onset AND packet_onset AND NOT boundaries_agree),
    'excluded_paired_dates',count(*) FILTER (WHERE (old_pair OR packet_pair) AND disagreement),
    'usable_paired_dates',(SELECT count(*) FROM accepted),
    'usable_new_dates',count(*) FILTER (WHERE packet_pair AND NOT old_pair AND NOT disagreement),
    'packet_provenance',coalesce((SELECT json_agg(row_to_json(v) ORDER BY packet_provenance) FROM provenance v),'[]'::json),
    'packet_reasons',(SELECT json_object_agg(reason,days ORDER BY reason) FROM exclusions),
    'epochs',coalesce((SELECT json_agg(row_to_json(a) ORDER BY epoch) FROM anonymous_epochs a),'[]'::json)
) AS summary FROM classified
