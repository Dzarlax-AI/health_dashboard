WITH study AS (SELECT *, (canonical_night_eligible AND label IS NOT NULL) AS duration_pair,
 (reason='candidate_proxy' AND asleep_end IS NOT NULL) AS proxy_end
 FROM {{ ref('research_stage_onset_rows_v1') }}),
reasons AS (SELECT reason,count(*) nights FROM study GROUP BY reason),
duration_bands AS (
 SELECT label,count(*) nights,CASE WHEN count(*)>=5 THEN avg(duration_hours) END mean_sleep_hours,
 CASE WHEN count(*)>=5 THEN stddev_samp(duration_hours) END sd_sleep_hours,
 count(*) FILTER(WHERE proxy_end) proxy_end_nights,
 CASE WHEN count(*) FILTER(WHERE proxy_end)>=5 THEN avg(duration_hours) FILTER(WHERE proxy_end) END proxy_end_mean_sleep_hours
 FROM study WHERE duration_pair GROUP BY label
), duration_epochs AS (
 SELECT source,source_epoch,label,count(*) nights,
 CASE WHEN count(*)>=5 THEN avg(duration_hours) END mean_sleep_hours,
 count(*) FILTER(WHERE proxy_end) proxy_end_nights,
 CASE WHEN count(*) FILTER(WHERE proxy_end)>=5 THEN avg(duration_hours) FILTER(WHERE proxy_end) END proxy_end_mean_sleep_hours
 FROM study WHERE duration_pair GROUP BY source,source_epoch,label
), duration_epoch_output AS (
 SELECT dense_rank() OVER(ORDER BY source,source_epoch) epoch_number,label,nights,mean_sleep_hours,proxy_end_nights,proxy_end_mean_sleep_hours FROM duration_epochs
), paired AS (
 SELECT *,extract(epoch FROM(onset-workout_end))/3600.0 AS interval_hours
 FROM study WHERE reason='candidate_proxy' AND workout_end<=onset AND after_sleep_label IN ('positive','meh')
), epoch_groups AS (
 SELECT source,source_epoch,count(*) nights,count(*) FILTER(WHERE canonical_binding) strict_nights,
 count(*) FILTER(WHERE after_sleep_label='positive') positive_nights,count(*) FILTER(WHERE after_sleep_label='meh') meh_nights,
 CASE WHEN count(*)>=5 THEN min(interval_hours) END min_hours,CASE WHEN count(*)>=5 THEN max(interval_hours) END max_hours,
 CASE WHEN count(*)>=5 THEN stddev_samp(interval_hours) END sd_hours,
 CASE WHEN count(*) FILTER(WHERE after_sleep_label='positive')>=5 THEN avg(interval_hours) FILTER(WHERE after_sleep_label='positive') END positive_mean_hours,
 CASE WHEN count(*) FILTER(WHERE after_sleep_label='meh')>=5 THEN avg(interval_hours) FILTER(WHERE after_sleep_label='meh') END meh_mean_hours
 FROM paired GROUP BY source,source_epoch
), epoch_output AS (
 SELECT row_number() OVER(ORDER BY source,source_epoch) epoch_number,nights,strict_nights,positive_nights,meh_nights,min_hours,max_hours,sd_hours,positive_mean_hours,meh_mean_hours FROM epoch_groups
)
SELECT json_build_object('calendar_days',count(*),'candidate_onset',count(*) FILTER(WHERE reason='candidate_proxy'),
 'strict_bound_onset',count(*) FILTER(WHERE reason='candidate_proxy' AND canonical_binding),
 'candidate_prior_workout',count(*) FILTER(WHERE reason='candidate_proxy' AND workout_end<=onset),
 'candidate_post_sleep_morning_label',count(*) FILTER(WHERE reason='candidate_proxy' AND after_sleep_label IN ('positive','meh','sick')),
 'candidate_workout_on_time_label',count(*) FILTER(WHERE reason='candidate_proxy' AND workout_end<=onset AND after_sleep_label IN ('positive','meh','sick')),
 'strict_workout_on_time_label',count(*) FILTER(WHERE reason='candidate_proxy' AND canonical_binding AND workout_end<=onset AND after_sleep_label IN ('positive','meh','sick')),
 'candidate_score_pair',count(*) FILTER(WHERE reason='candidate_proxy' AND workout_end<=onset AND after_sleep_label IN ('positive','meh')),
 'candidate_sick_pair',count(*) FILTER(WHERE reason='candidate_proxy' AND workout_end<=onset AND after_sleep_label='sick'),
 'duration_label_proxy_end',count(*) FILTER(WHERE duration_pair AND proxy_end),
 'duration_label_calendar_only',count(*) FILTER(WHERE duration_pair AND NOT proxy_end),
 'reasons',COALESCE((SELECT json_object_agg(reason,nights) FROM reasons),'{}'::json),
 'epochs',COALESCE((SELECT json_agg(row_to_json(e) ORDER BY epoch_number) FROM epoch_output e),'[]'::json),
 'duration_checkin_bands',COALESCE((SELECT json_agg(row_to_json(b) ORDER BY label) FROM duration_bands b),'[]'::json),
 'duration_epochs',COALESCE((SELECT json_agg(row_to_json(e) ORDER BY epoch_number,label) FROM duration_epoch_output e),'[]'::json)) AS summary FROM study
