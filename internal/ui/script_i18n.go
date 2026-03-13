package ui

const jsI18N = `
// ---- Localization ----
var LANG = localStorage.getItem('lang') || 'en';
var I18N = {
  en: {
    app_title:'Health', explore:'Explore', loading:'Loading your health data',
    readiness:'Readiness', recovery:'Recovery', back:'Back', compare:'Compare',
    all_metrics:'All metrics', your_trends:'Your trends',
    search_placeholder:'Search metrics...', esc_hint:'ESC to close',
    no_metrics_found:'No metrics found', no_data:'No data',
    no_data_range:'No data for this range', no_sleep_data:'No sleep data for this range',
    start_syncing:'Start syncing health data to see your readiness score.',
    data_from:'Data from ', days_ago:'d ago',
    this_week:'This week', activity_vs_recovery:'Activity vs Recovery',
    activity_recovery_subtitle:'How physical load affects your HRV',
    activity_load:'Activity load', sleep_section:'Sleep',
    sleep_subtitle:'Average over last 3 nights',
    deep_sleep:'Deep sleep', rem_sleep:'REM sleep', awake_time:'Awake time', efficiency:'Efficiency',
    bucket:'Bucket', agg:'Agg', auto:'Auto', minute:'Minute', hour:'Hour', day:'Day',
    avg:'Avg', sum:'Sum', max:'Max', min:'Min',
    previous_period:'Previous period', vs_yesterday:'vs yesterday', stable:'Stable',
    load_pct:'Load %', hrv_ms:'HRV ms',
    nights:'Nights', avg_total:'Avg total', avg_deep:'Avg deep', avg_rem:'Avg REM',
    points:'Points', stale_prefix:'Data from ', stale_suffix:'d ago',
    status_good:'Looking good', status_fair:'Needs attention', status_low:'Take care',
    cat_heart:'Heart & Vitals', cat_activity:'Activity', cat_fitness:'Fitness',
    cat_sleep:'Sleep', cat_env:'Environment', cat_other:'Other',
    phase_deep:'Deep', phase_rem:'REM', phase_core:'Core', phase_awake:'Awake',
    trend_steps:'Steps', trend_heart_rate:'Heart Rate', trend_sleep:'Sleep', trend_hrv:'HRV', trend_readiness:'Readiness',
    card_Steps:'Steps', card_Sleep:'Sleep', card_HRV:'HRV',
    card_Resting_HR:'Resting HR', card_Respiratory_Rate:'Respiratory Rate',
    metric_sleep_total:'Total Sleep', metric_sleep_deep:'Deep Sleep',
    metric_sleep_rem:'REM Sleep', metric_sleep_core:'Core Sleep', metric_sleep_awake:'Awake Time',
    metric_heart_rate:'Heart Rate', metric_resting_heart_rate:'Resting HR',
    metric_walking_heart_rate_average:'Walking HR', metric_heart_rate_variability:'HRV',
    metric_blood_oxygen_saturation:'Blood Oxygen', metric_respiratory_rate:'Respiratory Rate',
    metric_step_count:'Steps', metric_walking_running_distance:'Distance',
    metric_active_energy:'Active Calories', metric_basal_energy_burned:'Resting Calories',
    metric_apple_exercise_time:'Exercise', metric_apple_stand_time:'Stand Time',
    metric_apple_stand_hour:'Stand Hours', metric_physical_effort:'Physical Effort',
    metric_flights_climbed:'Flights Climbed', metric_stair_speed_up:'Stair Speed',
    metric_walking_speed:'Walking Speed', metric_walking_step_length:'Step Length',
    metric_walking_double_support_percentage:'Double Support',
    metric_walking_asymmetry_percentage:'Walking Asymmetry',
    metric_apple_sleeping_wrist_temperature:'Wrist Temp',
    metric_breathing_disturbances:'Breathing Disturbances',
    metric_environmental_audio_exposure:'Noise Exposure',
    metric_headphone_audio_exposure:'Headphone Volume',
    metric_time_in_daylight:'Daylight', metric_vo2_max:'VO2 Max',
    metric_six_minute_walking_test_distance:'6-min Walk',
    metric_readiness:'Readiness',
    by_source:'Sources', source_comparison:'Source comparison',
    lbl_total:'Total', lbl_deep:'Deep', lbl_core:'Core',
    how_it_works:'How it works',
    health_sections:'Health overview',
    at_a_glance:'At a glance',
    admin_title:'Settings', admin_cache_status:'Cache status', admin_actions:'Actions',
    admin_raw:'Raw data', admin_minute:'Minute cache', admin_hourly:'Hourly cache', admin_daily:'Daily scores',
    admin_metrics:'metrics', admin_empty:'empty',
    admin_score_version:'Score version', admin_last_sync:'Last sync',
    admin_incremental_title:'Update cache', admin_incremental_desc:'Fill missing entries since last run. Fast, safe to run anytime.',
    admin_force_title:'Rebuild all', admin_force_desc:'Clear and recompute all caches from raw data. Use after formula changes.',
    admin_run:'Run', admin_force_run:'Rebuild',
    admin_notify_title:'Telegram reports',
    admin_notify_morning_title:'Morning report', admin_notify_morning_desc:'Send a test sleep summary right now.',
    admin_notify_evening_title:'Evening report', admin_notify_evening_desc:'Send a test day summary right now.',
    admin_notify_send:'Send test'
  },
  ru: {
    app_title:'\u0417\u0434\u043e\u0440\u043e\u0432\u044c\u0435', explore:'\u041f\u043e\u0438\u0441\u043a', loading:'\u0417\u0430\u0433\u0440\u0443\u0437\u043a\u0430 \u0434\u0430\u043d\u043d\u044b\u0445',
    readiness:'\u0413\u043e\u0442\u043e\u0432\u043d\u043e\u0441\u0442\u044c', recovery:'\u0412\u043e\u0441\u0441\u0442\u0430\u043d\u043e\u0432\u043b\u0435\u043d\u0438\u0435', back:'\u041d\u0430\u0437\u0430\u0434', compare:'\u0421\u0440\u0430\u0432\u043d\u0438\u0442\u044c',
    all_metrics:'\u0412\u0441\u0435 \u043c\u0435\u0442\u0440\u0438\u043a\u0438', your_trends:'\u0412\u0430\u0448\u0438 \u0442\u0440\u0435\u043d\u0434\u044b',
    search_placeholder:'\u041f\u043e\u0438\u0441\u043a \u043c\u0435\u0442\u0440\u0438\u043a...', esc_hint:'ESC \u2014 \u0437\u0430\u043a\u0440\u044b\u0442\u044c',
    no_metrics_found:'\u041c\u0435\u0442\u0440\u0438\u043a\u0438 \u043d\u0435 \u043d\u0430\u0439\u0434\u0435\u043d\u044b', no_data:'\u041d\u0435\u0442 \u0434\u0430\u043d\u043d\u044b\u0445',
    no_data_range:'\u041d\u0435\u0442 \u0434\u0430\u043d\u043d\u044b\u0445 \u0437\u0430 \u044d\u0442\u043e\u0442 \u043f\u0435\u0440\u0438\u043e\u0434',
    no_sleep_data:'\u041d\u0435\u0442 \u0434\u0430\u043d\u043d\u044b\u0445 \u043e \u0441\u043d\u0435 \u0437\u0430 \u044d\u0442\u043e\u0442 \u043f\u0435\u0440\u0438\u043e\u0434',
    start_syncing:'\u041d\u0430\u0447\u043d\u0438\u0442\u0435 \u0441\u0438\u043d\u0445\u0440\u043e\u043d\u0438\u0437\u0430\u0446\u0438\u044e \u0434\u0430\u043d\u043d\u044b\u0445 \u043e \u0437\u0434\u043e\u0440\u043e\u0432\u044c\u0435.',
    data_from:'\u0414\u0430\u043d\u043d\u044b\u0435 \u043e\u0442 ', days_ago:'\u0434. \u043d\u0430\u0437\u0430\u0434',
    this_week:'\u042d\u0442\u0430 \u043d\u0435\u0434\u0435\u043b\u044f',
    activity_vs_recovery:'\u0410\u043a\u0442\u0438\u0432\u043d\u043e\u0441\u0442\u044c \u0438 \u0432\u043e\u0441\u0441\u0442\u0430\u043d\u043e\u0432\u043b\u0435\u043d\u0438\u0435',
    activity_recovery_subtitle:'\u041a\u0430\u043a \u043d\u0430\u0433\u0440\u0443\u0437\u043a\u0430 \u0432\u043b\u0438\u044f\u0435\u0442 \u043d\u0430 \u0412\u0421\u0420',
    activity_load:'\u041d\u0430\u0433\u0440\u0443\u0437\u043a\u0430', sleep_section:'\u0421\u043e\u043d',
    sleep_subtitle:'\u0421\u0440\u0435\u0434\u043d\u0435\u0435 \u0437\u0430 3 \u043d\u043e\u0447\u0438',
    deep_sleep:'\u0413\u043b\u0443\u0431\u043e\u043a\u0438\u0439 \u0441\u043e\u043d', rem_sleep:'REM \u0441\u043e\u043d',
    awake_time:'\u0411\u043e\u0434\u0440\u0441\u0442\u0432\u043e\u0432\u0430\u043d\u0438\u0435', efficiency:'\u042d\u0444\u0444\u0435\u043a\u0442\u0438\u0432\u043d\u043e\u0441\u0442\u044c',
    bucket:'\u041f\u0435\u0440\u0438\u043e\u0434', agg:'\u0410\u0433\u0440.', auto:'\u0410\u0432\u0442\u043e',
    minute:'\u041c\u0438\u043d\u0443\u0442\u0430', hour:'\u0427\u0430\u0441', day:'\u0414\u0435\u043d\u044c',
    avg:'\u0421\u0440.', sum:'\u0421\u0443\u043c\u043c\u0430', max:'\u041c\u0430\u043a\u0441', min:'\u041c\u0438\u043d',
    previous_period:'\u041f\u0440\u043e\u0448\u043b\u044b\u0439 \u043f\u0435\u0440\u0438\u043e\u0434',
    vs_yesterday:'\u043a \u0432\u0447\u0435\u0440\u0430', stable:'\u0421\u0442\u0430\u0431\u0438\u043b\u044c\u043d\u043e',
    load_pct:'\u041d\u0430\u0433\u0440\u0443\u0437\u043a\u0430 %', hrv_ms:'\u0412\u0421\u0420 \u043c\u0441',
    nights:'\u041d\u043e\u0447\u0435\u0439', avg_total:'\u0421\u0440. \u0432\u0441\u0435\u0433\u043e',
    avg_deep:'\u0421\u0440. \u0433\u043b\u0443\u0431\u043e\u043a\u0438\u0439', avg_rem:'\u0421\u0440. REM',
    points:'\u0422\u043e\u0447\u043a\u0438', stale_prefix:'\u0414\u0430\u043d\u043d\u044b\u0435 \u043e\u0442 ', stale_suffix:'\u0434. \u043d\u0430\u0437\u0430\u0434',
    status_good:'\u0425\u043e\u0440\u043e\u0448\u043e', status_fair:'\u0422\u0440\u0435\u0431\u0443\u0435\u0442 \u0432\u043d\u0438\u043c\u0430\u043d\u0438\u044f', status_low:'\u0411\u0435\u0440\u0435\u0433\u0438\u0442\u0435 \u0441\u0435\u0431\u044f',
    cat_heart:'\u0421\u0435\u0440\u0434\u0446\u0435 \u0438 \u043f\u043e\u043a\u0430\u0437\u0430\u0442\u0435\u043b\u0438', cat_activity:'\u0410\u043a\u0442\u0438\u0432\u043d\u043e\u0441\u0442\u044c',
    cat_fitness:'\u0424\u0438\u0442\u043d\u0435\u0441', cat_sleep:'\u0421\u043e\u043d',
    cat_env:'\u041e\u043a\u0440\u0443\u0436\u0430\u044e\u0449\u0430\u044f \u0441\u0440\u0435\u0434\u0430', cat_other:'\u041f\u0440\u043e\u0447\u0435\u0435',
    phase_deep:'\u0413\u043b\u0443\u0431\u043e\u043a\u0438\u0439', phase_rem:'REM',
    phase_core:'\u041e\u0441\u043d\u043e\u0432\u043d\u043e\u0439', phase_awake:'\u0411\u043e\u0434\u0440\u0441\u0442\u0432\u043e\u0432\u0430\u043d\u0438\u0435',
    trend_steps:'\u0428\u0430\u0433\u0438', trend_heart_rate:'\u0427\u0421\u0421', trend_sleep:'\u0421\u043e\u043d', trend_hrv:'\u0412\u0421\u0420', trend_readiness:'\u0413\u043e\u0442\u043e\u0432\u043d\u043e\u0441\u0442\u044c',
    card_Steps:'\u0428\u0430\u0433\u0438', card_Sleep:'\u0421\u043e\u043d', card_HRV:'\u0412\u0421\u0420',
    card_Resting_HR:'\u041f\u0443\u043b\u044c\u0441 \u043f\u043e\u043a\u043e\u044f', card_Respiratory_Rate:'\u0427\u0414\u0414',
    metric_sleep_total:'\u041e\u0431\u0449\u0438\u0439 \u0441\u043e\u043d', metric_sleep_deep:'\u0413\u043b\u0443\u0431\u043e\u043a\u0438\u0439 \u0441\u043e\u043d',
    metric_sleep_rem:'REM \u0441\u043e\u043d', metric_sleep_core:'\u041e\u0441\u043d\u043e\u0432\u043d\u043e\u0439 \u0441\u043e\u043d',
    metric_sleep_awake:'\u0411\u043e\u0434\u0440\u0441\u0442\u0432\u043e\u0432\u0430\u043d\u0438\u0435',
    metric_heart_rate:'\u0427\u0421\u0421', metric_resting_heart_rate:'\u041f\u0443\u043b\u044c\u0441 \u043f\u043e\u043a\u043e\u044f',
    metric_walking_heart_rate_average:'\u041f\u0443\u043b\u044c\u0441 \u043f\u0440\u0438 \u0445\u043e\u0434\u044c\u0431\u0435',
    metric_heart_rate_variability:'\u0412\u0421\u0420',
    metric_blood_oxygen_saturation:'\u041a\u0438\u0441\u043b\u043e\u0440\u043e\u0434 \u043a\u0440\u043e\u0432\u0438',
    metric_respiratory_rate:'\u0427\u0414\u0414',
    metric_step_count:'\u0428\u0430\u0433\u0438', metric_walking_running_distance:'\u0414\u0438\u0441\u0442\u0430\u043d\u0446\u0438\u044f',
    metric_active_energy:'\u0410\u043a\u0442. \u043a\u0430\u043b\u043e\u0440\u0438\u0438',
    metric_basal_energy_burned:'\u041a\u0430\u043b\u043e\u0440\u0438\u0438 \u043f\u043e\u043a\u043e\u044f',
    metric_apple_exercise_time:'\u0423\u043f\u0440\u0430\u0436\u043d\u0435\u043d\u0438\u044f',
    metric_apple_stand_time:'\u0412\u0440\u0435\u043c\u044f \u0441\u0442\u043e\u044f',
    metric_apple_stand_hour:'\u0427\u0430\u0441\u044b \u0441\u0442\u043e\u044f',
    metric_physical_effort:'\u0424\u0438\u0437. \u043d\u0430\u0433\u0440\u0443\u0437\u043a\u0430',
    metric_flights_climbed:'\u041f\u0440\u043e\u043b\u0451\u0442\u044b \u043b\u0435\u0441\u0442\u043d\u0438\u0446',
    metric_stair_speed_up:'\u0421\u043a\u043e\u0440\u043e\u0441\u0442\u044c \u043f\u043e \u043b\u0435\u0441\u0442\u043d\u0438\u0446\u0435',
    metric_walking_speed:'\u0421\u043a\u043e\u0440\u043e\u0441\u0442\u044c \u0445\u043e\u0434\u044c\u0431\u044b',
    metric_walking_step_length:'\u0414\u043b\u0438\u043d\u0430 \u0448\u0430\u0433\u0430',
    metric_walking_double_support_percentage:'\u0414\u0432\u043e\u0439\u043d\u0430\u044f \u043e\u043f\u043e\u0440\u0430',
    metric_walking_asymmetry_percentage:'\u0410\u0441\u0438\u043c\u043c\u0435\u0442\u0440\u0438\u044f \u0445\u043e\u0434\u044c\u0431\u044b',
    metric_apple_sleeping_wrist_temperature:'\u0422\u0435\u043c\u043f. \u0437\u0430\u043f\u044f\u0441\u0442\u044c\u044f',
    metric_breathing_disturbances:'\u041d\u0430\u0440\u0443\u0448\u0435\u043d\u0438\u044f \u0434\u044b\u0445\u0430\u043d\u0438\u044f',
    metric_environmental_audio_exposure:'\u0428\u0443\u043c\u043e\u0432\u0430\u044f \u043d\u0430\u0433\u0440\u0443\u0437\u043a\u0430',
    metric_headphone_audio_exposure:'\u0413\u0440\u043e\u043c\u043a\u043e\u0441\u0442\u044c \u043d\u0430\u0443\u0448\u043d\u0438\u043a\u043e\u0432',
    metric_time_in_daylight:'\u0414\u043d\u0435\u0432\u043d\u043e\u0439 \u0441\u0432\u0435\u0442',
    metric_vo2_max:'\u041c\u041f\u041a (VO2 Max)',
    metric_six_minute_walking_test_distance:'6-\u043c\u0438\u043d \u0445\u043e\u0434\u044c\u0431\u0430',
    metric_readiness:'\u0413\u043e\u0442\u043e\u0432\u043d\u043e\u0441\u0442\u044c',
    by_source:'\u0418\u0441\u0442\u043e\u0447\u043d\u0438\u043a\u0438', source_comparison:'\u0421\u0440\u0430\u0432\u043d\u0435\u043d\u0438\u0435 \u0438\u0441\u0442\u043e\u0447\u043d\u0438\u043a\u043e\u0432',
    lbl_total:'\u0418\u0442\u043e\u0433\u043e', lbl_deep:'\u0413\u043b\u0443\u0431\u043e\u043a\u0438\u0439', lbl_core:'\u041e\u0441\u043d\u043e\u0432\u043d\u043e\u0439',
    how_it_works:'\u041a\u0430\u043a \u044d\u0442\u043e \u0440\u0430\u0431\u043e\u0442\u0430\u0435\u0442',
    health_sections:'\u041e\u0431\u0437\u043e\u0440 \u0437\u0434\u043e\u0440\u043e\u0432\u044c\u044f',
    at_a_glance:'\u0421\u0435\u0433\u043e\u0434\u043d\u044f',
    admin_title:'\u041d\u0430\u0441\u0442\u0440\u043e\u0439\u043a\u0438', admin_cache_status:'\u0421\u043e\u0441\u0442\u043e\u044f\u043d\u0438\u0435 \u043a\u044d\u0448\u0430', admin_actions:'\u0414\u0435\u0439\u0441\u0442\u0432\u0438\u044f',
    admin_raw:'\u0421\u044b\u0440\u044b\u0435 \u0434\u0430\u043d\u043d\u044b\u0435', admin_minute:'\u041c\u0438\u043d\u0443\u0442\u043d\u044b\u0439 \u043a\u044d\u0448', admin_hourly:'\u0427\u0430\u0441\u043e\u0432\u043e\u0439 \u043a\u044d\u0448', admin_daily:'\u0414\u043d\u0435\u0432\u043d\u044b\u0435 \u043e\u0446\u0435\u043d\u043a\u0438',
    admin_metrics:'\u043c\u0435\u0442\u0440\u0438\u043a', admin_empty:'\u043f\u0443\u0441\u0442\u043e',
    admin_score_version:'\u0412\u0435\u0440\u0441\u0438\u044f \u0441\u043a\u043e\u0440\u0438\u043d\u0433\u0430', admin_last_sync:'\u041f\u043e\u0441\u043b\u0435\u0434\u043d\u0438\u0439 \u0441\u0438\u043d\u0445',
    admin_incremental_title:'\u041e\u0431\u043d\u043e\u0432\u0438\u0442\u044c \u043a\u044d\u0448', admin_incremental_desc:'\u0414\u043e\u0437\u0430\u043f\u043e\u043b\u043d\u0438\u0442\u044c \u043f\u0440\u043e\u043f\u0443\u0449\u0435\u043d\u043d\u044b\u0435 \u0437\u0430\u043f\u0438\u0441\u0438. \u0411\u044b\u0441\u0442\u0440\u043e, \u0431\u0435\u0437\u043e\u043f\u0430\u0441\u043d\u043e.',
    admin_force_title:'\u041f\u0435\u0440\u0435\u0441\u0442\u0440\u043e\u0438\u0442\u044c \u0432\u0441\u0451', admin_force_desc:'\u041e\u0447\u0438\u0441\u0442\u0438\u0442\u044c \u0438 \u043f\u0435\u0440\u0435\u0441\u0447\u0438\u0442\u0430\u0442\u044c \u0432\u0441\u0435 \u043a\u044d\u0448\u0438. \u0418\u0441\u043f\u043e\u043b\u044c\u0437\u0443\u0439\u0442\u0435 \u043f\u043e\u0441\u043b\u0435 \u0438\u0437\u043c\u0435\u043d\u0435\u043d\u0438\u044f \u0444\u043e\u0440\u043c\u0443\u043b\u044b.',
    admin_run:'\u0417\u0430\u043f\u0443\u0441\u0442\u0438\u0442\u044c', admin_force_run:'\u041f\u0435\u0440\u0435\u0441\u0442\u0440\u043e\u0438\u0442\u044c',
    admin_notify_title:'Telegram-\u043e\u0442\u0447\u0451\u0442\u044b',
    admin_notify_morning_title:'\u0423\u0442\u0440\u0435\u043d\u043d\u0438\u0439 \u043e\u0442\u0447\u0451\u0442', admin_notify_morning_desc:'\u041e\u0442\u043f\u0440\u0430\u0432\u0438\u0442\u044c \u0442\u0435\u0441\u0442\u043e\u0432\u0443\u044e \u0441\u0432\u043e\u0434\u043a\u0443 \u043f\u043e \u0441\u043d\u0443 \u0441\u0435\u0439\u0447\u0430\u0441.',
    admin_notify_evening_title:'\u0412\u0435\u0447\u0435\u0440\u043d\u0438\u0439 \u043e\u0442\u0447\u0451\u0442', admin_notify_evening_desc:'\u041e\u0442\u043f\u0440\u0430\u0432\u0438\u0442\u044c \u0442\u0435\u0441\u0442\u043e\u0432\u0443\u044e \u0441\u0432\u043e\u0434\u043a\u0443 \u0437\u0430 \u0434\u0435\u043d\u044c \u0441\u0435\u0439\u0447\u0430\u0441.',
    admin_notify_send:'\u041e\u0442\u043f\u0440\u0430\u0432\u0438\u0442\u044c \u0442\u0435\u0441\u0442'
  },
  sr: {
    app_title:'Zdravlje', explore:'Pretra\u017ei', loading:'U\u010ditavanje podataka',
    readiness:'Spremnost', recovery:'Oporavak', back:'Nazad', compare:'Uporedi',
    all_metrics:'Sve metrike', your_trends:'Va\u0161i trendovi',
    search_placeholder:'Pretra\u017ei metrike...', esc_hint:'ESC \u2014 zatvori',
    no_metrics_found:'Nema metrika', no_data:'Nema podataka',
    no_data_range:'Nema podataka za ovaj period',
    no_sleep_data:'Nema podataka o snu za ovaj period',
    start_syncing:'Po\u010dnite sinhronizaciju podataka o zdravlju.',
    data_from:'Podaci od ', days_ago:'d ranije',
    this_week:'Ova nedelja',
    activity_vs_recovery:'Aktivnost i oporavak',
    activity_recovery_subtitle:'Kako fizi\u010dko optere\u0107enje uti\u010de na HRV',
    activity_load:'Optere\u0107enje', sleep_section:'San',
    sleep_subtitle:'Prosek za poslednje 3 no\u0107i',
    deep_sleep:'Duboki san', rem_sleep:'REM san', awake_time:'Vreme budnosti', efficiency:'Efikasnost',
    bucket:'Period', agg:'Agr.', auto:'Auto', minute:'Minut', hour:'Sat', day:'Dan',
    avg:'Pros.', sum:'Zbir', max:'Maks', min:'Min',
    previous_period:'Prethodni period', vs_yesterday:'vs ju\u010de', stable:'Stabilno',
    load_pct:'Optere\u0107enje %', hrv_ms:'HRV ms',
    nights:'No\u0107i', avg_total:'Pros. ukupno', avg_deep:'Pros. duboki', avg_rem:'Pros. REM',
    points:'Ta\u010dke', stale_prefix:'Podaci od ', stale_suffix:'d ranije',
    status_good:'Odli\u010dno', status_fair:'Treba pa\u017enje', status_low:'\u010cuvajte se',
    cat_heart:'Srce i vitalni znaci', cat_activity:'Aktivnost', cat_fitness:'Fitnes',
    cat_sleep:'San', cat_env:'Okru\u017eenje', cat_other:'Ostalo',
    phase_deep:'Duboki', phase_rem:'REM', phase_core:'Osnovni', phase_awake:'Budan',
    trend_steps:'Koraci', trend_heart_rate:'Puls', trend_sleep:'San', trend_hrv:'HRV', trend_readiness:'Spremnost',
    card_Steps:'Koraci', card_Sleep:'San', card_HRV:'HRV',
    card_Resting_HR:'Puls u miru', card_Respiratory_Rate:'Respiratorni ritam',
    metric_sleep_total:'Ukupan san', metric_sleep_deep:'Duboki san',
    metric_sleep_rem:'REM san', metric_sleep_core:'Osnovni san',
    metric_sleep_awake:'Vreme budnosti',
    metric_heart_rate:'Puls', metric_resting_heart_rate:'Puls u miru',
    metric_walking_heart_rate_average:'Puls pri hodu',
    metric_heart_rate_variability:'HRV',
    metric_blood_oxygen_saturation:'Kiseonik u krvi',
    metric_respiratory_rate:'Respiratorni ritam',
    metric_step_count:'Koraci', metric_walking_running_distance:'Distanca',
    metric_active_energy:'Akt. kalorije', metric_basal_energy_burned:'Kalorije u miru',
    metric_apple_exercise_time:'Ve\u017ebanje', metric_apple_stand_time:'Vreme stajanja',
    metric_apple_stand_hour:'Sati stajanja', metric_physical_effort:'Fizi\u010dki napor',
    metric_flights_climbed:'Penjanje uz stepenice', metric_stair_speed_up:'Brzina na stepenicama',
    metric_walking_speed:'Brzina hoda', metric_walking_step_length:'Du\u017eina koraka',
    metric_walking_double_support_percentage:'Dvostrana podr\u0161ka',
    metric_walking_asymmetry_percentage:'Asimetrija hoda',
    metric_apple_sleeping_wrist_temperature:'Temp. zgloba',
    metric_breathing_disturbances:'Poreme\u0107aji disanja',
    metric_environmental_audio_exposure:'Izlo\u017eenost buci',
    metric_headphone_audio_exposure:'Glasno\u0107a slu\u0161alica',
    metric_time_in_daylight:'Dnevna svetlost',
    metric_vo2_max:'VO2 Maks',
    metric_six_minute_walking_test_distance:'6-min hod',
    metric_readiness:'Spremnost',
    by_source:'Izvori', source_comparison:'Pore\u0111enje izvora',
    lbl_total:'Ukupno', lbl_deep:'Duboki', lbl_core:'Osnovni',
    how_it_works:'Kako to radi',
    health_sections:'Pregled zdravlja',
    at_a_glance:'Danas',
    admin_notify_title:'Telegram izve\u0161taji',
    admin_notify_morning_title:'Jutarnji izve\u0161taj', admin_notify_morning_desc:'Po\u0161alji test san sada.',
    admin_notify_evening_title:'Ve\u010dernji izve\u0161taj', admin_notify_evening_desc:'Po\u0161alji test pregled dana sada.',
    admin_notify_send:'Po\u0161alji test'
  }
};
function t(key) {
  return (I18N[LANG] && I18N[LANG][key] != null) ? I18N[LANG][key] : (I18N['en'][key] != null ? I18N['en'][key] : key);
}
function name(k) { return t('metric_' + k) || k.replace(/_/g,' '); }
function cardName(n) { return t('card_' + n.replace(/ /g,'_')) || n; }
function getCatLabel(key) {
  var map = { heart:'cat_heart', activity:'cat_activity', mobility:'cat_fitness', sleep:'cat_sleep', env:'cat_env', other:'cat_other' };
  return t(map[key] || 'cat_other');
}
function applyLang() {
  document.querySelectorAll('[data-i18n]').forEach(function(el) {
    el.textContent = t(el.getAttribute('data-i18n'));
  });
  var si = $('metrics-search');
  if (si) si.placeholder = t('search_placeholder');
  var lb = $('lang-btn');
  if (lb) lb.textContent = LANG.toUpperCase();
}
function cycleLang() {
  var langs = ['en','ru','sr'];
  LANG = langs[(langs.indexOf(LANG) + 1) % langs.length];
  localStorage.setItem('lang', LANG);
  applyLang();
  // Re-fetch briefing so backend generates text in new language
  fetch('/api/health-briefing?lang=' + LANG).then(function(r){return r.json()}).catch(function(){return null}).then(function(res) {
    briefingData = res;
    if (res) renderBriefing(res);
  });
  if ($('metrics-view').style.display !== 'none') renderMetricsView();
  loadTrendCharts();
  if (currentMetric) loadChart();
  if (currentSection) renderSectionView(currentSection);
}

var CATEGORIES = [
  { label:'Heart & Vitals', color:'var(--heart)',   cat:'heart',    metrics:['heart_rate','resting_heart_rate','walking_heart_rate_average','heart_rate_variability','blood_oxygen_saturation','respiratory_rate'] },
  { label:'Activity',       color:'var(--activity)', cat:'activity', metrics:['step_count','walking_running_distance','active_energy','basal_energy_burned','apple_exercise_time','apple_stand_time','apple_stand_hour','physical_effort','flights_climbed','stair_speed_up'] },
  { label:'Fitness',        color:'#f59e0b',         cat:'mobility', metrics:['vo2_max','six_minute_walking_test_distance','walking_speed','walking_step_length','walking_double_support_percentage','walking_asymmetry_percentage'] },
  { label:'Sleep',          color:'var(--sleep)',    cat:'sleep',    metrics:['sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_awake','apple_sleeping_wrist_temperature','breathing_disturbances'] },
  { label:'Environment',    color:'#06b6d4',         cat:'env',      metrics:['environmental_audio_exposure','headphone_audio_exposure','time_in_daylight'] }
];
function catOf(m) { return CATEGORIES.find(function(c) { return c.metrics.includes(m); }) || null; }
var BAR_METRICS = new Set(['step_count','active_energy','basal_energy_burned','apple_exercise_time','apple_stand_time','flights_climbed','walking_running_distance','time_in_daylight','apple_stand_hour','breathing_disturbances']);
var SLEEP_PHASES = [
  { metric:'sleep_deep', labelKey:'phase_deep', color:'#6366f1' },
  { metric:'sleep_rem',  labelKey:'phase_rem',  color:'#a78bfa' },
  { metric:'sleep_core', labelKey:'phase_core', color:'#93c5fd' },
  { metric:'sleep_awake',labelKey:'phase_awake',color:'#fbbf24' }
];
var SLEEP_METRICS = new Set(['sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_awake']);
var TRENDS = [
  { metric:'step_count',             labelKey:'trend_steps',      color:'#059669', type:'bar' },
  { metric:'heart_rate',             labelKey:'trend_heart_rate', color:'#e11d48', type:'line' },
  { metric:'sleep_total',            labelKey:'trend_sleep',      color:'#7c3aed', type:'bar' },
  { metric:'heart_rate_variability', labelKey:'trend_hrv',        color:'#d97706', type:'line' },
  { metric:'readiness',              labelKey:'trend_readiness',  color:'#0ea5e9', type:'line', virtual:true }
];
var ICON_MAP = {
  battery: '<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="1" y="6" width="18" height="12" rx="2"/><line x1="23" y1="13" x2="23" y2="11"/></svg>',
  moon: '<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>',
  activity: '<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="22 12 18 12 15 21 9 3 6 12 2 12"/></svg>',
  heart: '<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M20.84 4.61a5.5 5.5 0 0 0-7.78 0L12 5.67l-1.06-1.06a5.5 5.5 0 0 0-7.78 7.78l1.06 1.06L12 21.23l7.78-7.78 1.06-1.06a5.5 5.5 0 0 0 0-7.78z"/></svg>'
};

var METRIC_ICONS = {
  Steps: '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="22 12 18 12 15 21 9 3 6 12 2 12"/></svg>',
  Sleep: '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>',
  HRV: '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M13 2L3 14h9l-1 8 10-12h-9l1-8z"/></svg>',
  'Resting HR': '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M20.84 4.61a5.5 5.5 0 0 0-7.78 0L12 5.67l-1.06-1.06a5.5 5.5 0 0 0-7.78 7.78l1.06 1.06L12 21.23l7.78-7.78 1.06-1.06a5.5 5.5 0 0 0 0-7.78z"/></svg>',
  'Respiratory Rate': '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M17.7 7.7a2.5 2.5 0 1 1 1.8 4.3H2"/><path d="M9.6 4.6A2 2 0 1 1 11 8H2"/><path d="M12.6 19.4A2 2 0 1 0 14 16H2"/></svg>'
};
var METRIC_COLORS = {
  Steps: { bg: '#d1fae5', color: '#059669' },
  Sleep: { bg: '#ede9fe', color: '#7c3aed' },
  HRV: { bg: '#fef3c7', color: '#d97706' },
  'Resting HR': { bg: '#ffe4e6', color: '#e11d48' },
  'Respiratory Rate': { bg: '#dbeafe', color: '#0284c7' }
};
var METRIC_TO_KEY = {
  Steps: 'step_count', Sleep: 'sleep_total', HRV: 'heart_rate_variability',
  'Resting HR': 'resting_heart_rate', 'Respiratory Rate': 'respiratory_rate'
};
`
