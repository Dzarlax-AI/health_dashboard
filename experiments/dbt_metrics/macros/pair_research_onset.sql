{% macro pair_research_onset() %}
SELECT c.day,c.source,c.source_epoch,c.duration_hours,c.canonical_night_eligible,
 c.onset,c.asleep_end,c.reason,c.packet_provenance,c.canonical_binding,w.end_time AS workout_end,
 labels.label,labels.after_sleep_label
FROM classified c
LEFT JOIN LATERAL (SELECT end_time FROM workouts WHERE c.reason='candidate_proxy' AND start_time<c.onset
 AND end_time>=c.onset-interval '24 hours' AND end_time>start_time ORDER BY end_time DESC LIMIT 1) w ON true
LEFT JOIN LATERAL (
 SELECT CASE WHEN count(DISTINCT CASE WHEN k.answer IN ('great','ok') THEN 'positive' ELSE k.answer END)=1
 THEN min(CASE WHEN k.answer IN ('great','ok') THEN 'positive' ELSE k.answer END) END AS label,
 CASE WHEN count(DISTINCT CASE WHEN k.answered_at>=c.asleep_end THEN CASE WHEN k.answer IN ('great','ok') THEN 'positive' ELSE k.answer END END)=1
 AND bool_and(k.answered_at>=c.asleep_end) THEN min(CASE WHEN k.answer IN ('great','ok') THEN 'positive' ELSE k.answer END) END AS after_sleep_label
 FROM subjective_checkins k WHERE k.date=c.day::text AND k.status='answered' AND k.answer IN ('great','ok','meh','sick')
 AND k.answered_at<k.expires_at AND k.answered_at>=k.prompted_at AND (k.answered_at AT TIME ZONE c.tz)::date=c.day
 AND (k.answered_at AT TIME ZONE c.tz)::time>=time '05:00' AND (k.answered_at AT TIME ZONE c.tz)::time<time '12:00'
 AND (k.prompted_at AT TIME ZONE c.tz)::date=c.day AND (c.asleep_end IS NULL OR k.answered_at>=c.asleep_end)
) labels ON true
{% endmacro %}
