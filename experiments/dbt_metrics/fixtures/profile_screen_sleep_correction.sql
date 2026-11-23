-- Accepted corrections in the primary disposable profile only.
update completed_night_sleep set duration_hours=9,input_hash='sleep-screen-late'
where wake_date='1981-03-08' and source='Graph Watch';
update completed_sleep_episode set end_at=end_at+interval '90 minutes',input_hash='sleep-screen-late'
where episode_id='screen-1981-03-08' and source='Graph Watch';
update metric_points set qty=case metric_name when 'sleep_total' then 8.5 when 'sleep_core' then 5 end
where metric_name in ('sleep_total','sleep_core') and date='2026-02-01 05:00:00 +0000'
  and source='Apple Watch';
update derived_metrics set value_timestamp=value_timestamp+interval '30 minutes',inputs_hash='wake-screen-late'
where metric_name='wake_time' and metric_date='2026-02-01';
