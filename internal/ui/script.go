package ui

// jsScript contains all frontend JavaScript.
// Note: Go raw string literals cannot contain backticks, so we avoid JS template literals.
const jsScript = `
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
    trend_steps:'Steps', trend_heart_rate:'Heart Rate', trend_sleep:'Sleep', trend_hrv:'HRV',
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
    metric_six_minute_walking_test_distance:'6-min Walk'
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
    trend_steps:'\u0428\u0430\u0433\u0438', trend_heart_rate:'\u0427\u0421\u0421', trend_sleep:'\u0421\u043e\u043d', trend_hrv:'\u0412\u0421\u0420',
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
    metric_six_minute_walking_test_distance:'6-\u043c\u0438\u043d \u0445\u043e\u0434\u044c\u0431\u0430'
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
    trend_steps:'Koraci', trend_heart_rate:'Puls', trend_sleep:'San', trend_hrv:'HRV',
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
    metric_six_minute_walking_test_distance:'6-min hod'
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
  var si = $('search-input');
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
  if (dashboardData) renderDashboardCards(dashboardData);
  loadTrendCharts();
  if (currentMetric) loadChart();
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
  { metric:'heart_rate_variability', labelKey:'trend_hrv',        color:'#d97706', type:'line' }
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

// ---- State ----
var chart = null, corrChart = null, currentMetric = null, compareEnabled = false, lastPoints = [], unitsMap = {}, allMetrics = [], dashboardData = null, briefingData = null;
var trendCharts = [];
var fromExplore = false;
function $(id) { return document.getElementById(id); }
function todayStr() { return new Date().toISOString().slice(0,10); }
function daysAgoStr(n) { var d = new Date(); d.setDate(d.getDate()-n); return d.toISOString().slice(0,10); }

// ---- Init ----
$('from').value = daysAgoStr(29);
$('to').value = todayStr();
readHash();
applyLang();
init();

function init() {
  Promise.all([
    fetch('/api/health-briefing?lang=' + LANG).then(function(r){return r.json()}).catch(function(){return null}),
    fetch('/api/dashboard').then(function(r){return r.json()}).catch(function(){return null}),
    fetch('/api/metrics').then(function(r){return r.json()}).catch(function(){return []})
  ]).then(function(results) {
    var briefingRes = results[0], dashRes = results[1], metricsRes = results[2];
    if (metricsRes) {
      allMetrics = metricsRes;
      metricsRes.forEach(function(m) { unitsMap[m.Name] = m.Units; });
    }
    briefingData = briefingRes;
    dashboardData = dashRes;
    $('briefing-loading').style.display = 'none';
    $('briefing-content').style.display = '';
    renderBriefing(briefingRes);
    renderDashboardCards(dashRes);
    loadTrendCharts();
    if (location.hash) {
      var p = new URLSearchParams(location.hash.slice(1));
      var m = p.get('metric');
      if (m) selectMetric(m);
    }
  });
}

// ---- Render briefing ----
function renderBriefing(data) {
  if (!data) {
    $('readiness-score').textContent = '--';
    $('readiness-status').textContent = t('no_data');
    $('readiness-tip').textContent = t('start_syncing');
    return;
  }

  // Readiness card
  $('readiness-score').textContent = data.readiness_score || '--';
  $('readiness-status').textContent = data.readiness_label || '';
  $('readiness-tip').textContent = data.readiness_tip || '';
  $('recovery-pct-label').textContent = (data.recovery_pct || 0) + '%';
  $('recovery-bar-fill').style.width = (data.recovery_pct || 0) + '%';

  // Date in hero
  if (data.date) {
    var d = new Date(data.date + 'T12:00:00');
    var localeCode = LANG === 'ru' ? 'ru' : LANG === 'sr' ? 'sr-Latn' : 'en';
    var dateLabel = d.toLocaleDateString(localeCode, { weekday:'long', month:'long', day:'numeric' });
    var heroDate = dateLabel;
    var isToday = data.date === todayStr();
    if (!isToday) {
      var daysAgo = Math.round((new Date() - d) / 86400000);
      heroDate += '<span class="stale-badge">' + t('stale_prefix') + daysAgo + t('stale_suffix') + '</span>';
    }
    $('hero-date-strip').innerHTML = heroDate;
  }

  // Overall status
  if (data.overall) {
    var statusEl = $('overall-status');
    statusEl.className = data.overall;
    statusEl.style.display = '';
    $('overall-label').textContent = t('status_' + data.overall) || data.overall;
  }

  // Metric cards
  var mcGrid = $('metric-cards-grid');
  mcGrid.innerHTML = '';
  if (data.metric_cards && data.metric_cards.length) {
    data.metric_cards.forEach(function(mc) {
      var colors = METRIC_COLORS[mc.name] || { bg: '#f5f5f4', color: '#57534e' };
      var icon = METRIC_ICONS[mc.name] || '';
      var metricKey = METRIC_TO_KEY[mc.name] || '';
      var trendCls = mc.trend_pct > 3 ? 'positive' : mc.trend_pct < -3 ? 'negative' : 'neutral';
      var trendTxt = mc.trend_pct > 0 ? '+' + mc.trend_pct.toFixed(1) + '%' : mc.trend_pct < 0 ? mc.trend_pct.toFixed(1) + '%' : t('stable');
      var card = document.createElement('div');
      card.className = 'metric-card';
      if (metricKey) { card.onclick = function() { selectMetric(metricKey); }; }
      card.innerHTML = '<div class="metric-card-top"><div class="metric-card-icon" style="background:' + colors.bg + ';color:' + colors.color + '">' + icon + '</div><span class="metric-card-trend ' + trendCls + '">' + trendTxt + '</span></div><div class="metric-card-name">' + cardName(mc.name) + '</div><div class="metric-card-value">' + mc.value + '</div><div class="metric-card-unit">' + mc.unit + '</div>';
      mcGrid.appendChild(card);
    });
  }

  // Correlation chart
  if (data.correlation && data.correlation.length > 1) {
    $('correlation-section').style.display = '';
    renderCorrelationChart(data.correlation);
  }

  // Insights
  if (data.insights && data.insights.length) {
    $('insights-panel').style.display = '';
    var list = $('insights-list');
    list.innerHTML = '';
    data.insights.forEach(function(ins) {
      var li = document.createElement('li');
      li.innerHTML = '<div class="insight-dot ' + ins.type + '"></div><span>' + ins.text + '</span>';
      list.appendChild(li);
    });
  }

  // Sleep analysis
  if (data.sleep) {
    $('sleep-section').style.display = '';
    var sg = $('sleep-stats-grid');
    sg.innerHTML = '';
    var sleepItems = [
      { label: t('deep_sleep'), value: formatHM(data.sleep.deep_avg) },
      { label: t('rem_sleep'), value: formatHM(data.sleep.rem_avg) },
      { label: t('awake_time'), value: formatHM(data.sleep.awake_avg) },
      { label: t('efficiency'), value: data.sleep.efficiency.toFixed(0) + '%', accent: data.sleep.efficiency >= 85 }
    ];
    sleepItems.forEach(function(item) {
      sg.innerHTML += '<div class="sleep-stat"><div class="sleep-stat-label">' + item.label + '</div><div class="sleep-stat-value' + (item.accent ? ' accent' : '') + '">' + item.value + '</div></div>';
    });
  }

  // Section detail cards
  var cardsEl = $('section-cards');
  cardsEl.innerHTML = '';
  if (data.sections && data.sections.length) {
    data.sections.forEach(function(s) {
      var card = document.createElement('div');
      card.className = 'insight-card status-' + s.status;
      card.dataset.key = s.key;
      var html = '<div class="insight-header"><div class="insight-icon">' + (ICON_MAP[s.icon] || '') + '</div><div class="insight-title">' + s.title + '</div><div class="insight-badge">' + t('status_' + s.status) + '</div></div><div class="insight-summary">' + s.summary + '</div>';
      if (s.details && s.details.length) {
        html += '<div class="insight-details">';
        s.details.forEach(function(d) {
          html += '<div class="insight-detail"><span class="detail-indicator ' + (d.trend || 'stable') + '"></span><span class="detail-label">' + d.label + '</span><span class="detail-value">' + d.value + '</span><span class="detail-note">' + (d.note || '') + '</span></div>';
        });
        html += '</div>';
      }
      card.innerHTML = html;
      cardsEl.appendChild(card);
    });
  }
}

function formatHM(hours) {
  if (!hours) return '0m';
  var h = Math.floor(hours);
  var m = Math.round((hours - h) * 60);
  if (h > 0 && m > 0) return h + 'h ' + m + 'm';
  if (h > 0) return h + 'h';
  return m + 'm';
}

// ---- Correlation chart ----
function renderCorrelationChart(data) {
  if (corrChart) { corrChart.destroy(); corrChart = null; }
  var sorted = data.slice().sort(function(a, b) { return a.date > b.date ? 1 : -1; });
  var localeCode = LANG === 'ru' ? 'ru' : LANG === 'sr' ? 'sr-Latn' : 'en';
  var labels = sorted.map(function(p) {
    var d = new Date(p.date + 'T12:00:00');
    return d.toLocaleDateString(localeCode, { weekday: 'short', month: 'short', day: 'numeric' });
  });
  var loadVals = sorted.map(function(p) { return p.load; });
  var hrvVals = sorted.map(function(p) { return p.hrv; });

  var ctx = $('corr-chart').getContext('2d');
  corrChart = new Chart(ctx, {
    type: 'line',
    data: {
      labels: labels,
      datasets: [
        {
          label: t('activity_load'),
          data: loadVals,
          borderColor: '#059669',
          backgroundColor: 'rgba(5,150,105,0.1)',
          fill: true,
          tension: 0.4,
          borderWidth: 2.5,
          pointRadius: 4,
          pointBackgroundColor: '#059669',
          yAxisID: 'y'
        },
        {
          label: t('metric_heart_rate_variability'),
          data: hrvVals,
          borderColor: '#e11d48',
          backgroundColor: 'rgba(225,29,72,0.08)',
          fill: true,
          tension: 0.4,
          borderWidth: 2.5,
          pointRadius: 4,
          pointBackgroundColor: '#e11d48',
          yAxisID: 'y1'
        }
      ]
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      interaction: { mode: 'index', intersect: false },
      plugins: {
        legend: { display: false },
        tooltip: {
          backgroundColor: '#fff', borderColor: '#e7e5e4', borderWidth: 1,
          titleColor: '#78716c', bodyColor: '#1c1917',
          callbacks: {
            label: function(ctx) {
              return ' ' + ctx.dataset.label + ': ' + ctx.parsed.y.toFixed(1);
            }
          }
        }
      },
      scales: {
        x: { ticks: { color: '#78716c', font: { size: 11 } }, grid: { color: '#f0efed' } },
        y: { position: 'left', ticks: { color: '#059669', font: { size: 11 } }, grid: { color: '#f0efed' }, title: { display: true, text: t('load_pct'), color: '#059669', font: { size: 11 } } },
        y1: { position: 'right', ticks: { color: '#e11d48', font: { size: 11 } }, grid: { drawOnChartArea: false }, title: { display: true, text: t('hrv_ms'), color: '#e11d48', font: { size: 11 } } }
      }
    }
  });
}

// ---- Explore cards ----
function renderDashboardCards(data) {
  if (!data || !data.cards || !data.cards.length) return;
  var content = $('explore-content');
  content.innerHTML = '';
  var grouped = {};
  data.cards.forEach(function(c) {
    var cat = catOf(c.metric);
    var key = cat ? cat.cat : 'other';
    if (!grouped[key]) grouped[key] = { cat: cat, cards: [] };
    grouped[key].cards.push(c);
  });
  var catOrder = ['heart','activity','mobility','sleep','env','other'];
  var colorMap = { heart:'var(--heart)', activity:'var(--activity)', mobility:'#f59e0b', sleep:'var(--sleep)', env:'#06b6d4', other:'var(--muted)' };
  catOrder.forEach(function(key) {
    if (!grouped[key]) return;
    var g = grouped[key];
    var section = document.createElement('div');
    section.className = 'explore-category';
    section.innerHTML = '<div class="explore-cat-label"><span class="explore-cat-dot" style="background:' + (colorMap[key] || 'var(--muted)') + '"></span>' + getCatLabel(key) + '</div>';
    var grid = document.createElement('div');
    grid.className = 'explore-grid';
    g.cards.forEach(function(c) {
      var div = document.createElement('div');
      div.className = 'explore-card';
      var trendHtml = '';
      if (c.prev > 0) {
        var pct = ((c.value - c.prev) / c.prev * 100);
        var cls = pct > 1 ? 'up' : pct < -1 ? 'down' : 'neutral';
        var arrow = pct > 1 ? '\u2191' : pct < -1 ? '\u2193' : '\u2192';
        trendHtml = '<div class="explore-card-trend ' + cls + '">' + arrow + ' ' + Math.abs(pct).toFixed(0) + '% ' + t('vs_yesterday') + '</div>';
      }
      div.innerHTML = '<div class="explore-card-label">' + name(c.metric) + '</div><div class="explore-card-value">' + fmtVal(c.metric, c.value) + '</div><div class="explore-card-unit">' + fmtUnit(c.unit) + '</div>' + trendHtml;
      div.onclick = function() { selectMetric(c.metric, true); };
      grid.appendChild(div);
    });
    section.appendChild(grid);
    content.appendChild(section);
  });
}

// ---- Trend charts ----
function loadTrendCharts() {
  var container = $('trend-charts');
  container.innerHTML = '';
  trendCharts.forEach(function(c) { c.destroy(); });
  trendCharts.length = 0;
  var from30 = daysAgoStr(29), to30 = todayStr();
  Promise.all(TRENDS.map(function(f) {
    return fetch('/api/metrics/data?metric=' + encodeURIComponent(f.metric) + '&from=' + from30 + '&to=' + to30 + '&bucket=day')
      .then(function(r){return r.json()})
      .then(function(d) { return { f: f, pts: (d.points || []).filter(function(p){return p.qty > 0}) }; })
      .catch(function() { return { f: f, pts: [] }; });
  })).then(function(results) {
    results.forEach(function(r) {
      var f = r.f, pts = r.pts;
      if (!pts.length) return;
      var wrap = document.createElement('div');
      wrap.className = 'trend-card';
      wrap.onclick = function() { selectMetric(f.metric); };
      var vals = pts.map(function(p){return p.qty});
      var latestVal = vals[vals.length-1];
      wrap.innerHTML = '<div class="trend-card-header"><div class="trend-card-title">' + t(f.labelKey) + '</div><div class="trend-card-value">' + fmtVal(f.metric, latestVal) + '</div></div><div class="trend-card-canvas"><canvas></canvas></div>';
      container.appendChild(wrap);
      var canvas = wrap.querySelector('canvas');
      var labels = pts.map(function(p){return fmtAxisDate(p.date)});
      var c = new Chart(canvas, {
        type: f.type,
        data: { labels: labels, datasets: [{ data: vals, borderColor: f.color, backgroundColor: f.type === 'bar' ? f.color + '55' : f.color + '15', fill: f.type === 'line', borderWidth: f.type === 'line' ? 2 : 1, pointRadius: 0, tension: 0.35, borderRadius: f.type === 'bar' ? 3 : 0 }] },
        options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { display: false }, tooltip: { backgroundColor: '#fff', borderColor: '#e7e5e4', borderWidth: 1, titleColor: '#78716c', bodyColor: '#1c1917', padding: 8, callbacks: { title: function(items) { return fmtAxisDate(items[0].label); }, label: function(ctx) { return ' ' + fmt2(ctx.parsed.y) + (fmtUnit(unitsMap[f.metric] || '') ? ' ' + fmtUnit(unitsMap[f.metric] || '') : ''); } } } }, scales: { x: { display: false }, y: { display: false, beginAtZero: f.type === 'bar' } }, elements: { point: { radius: 0, hoverRadius: 4 } } }
      });
      trendCharts.push(c);
    });
  });
}

// ---- UI toggles ----
function toggleExplore() {
  $('explore-toggle').classList.toggle('open');
  $('explore-content').classList.toggle('open');
}
var searchSelectedIndex = -1;

function openSearch() {
  $('search-overlay').style.display = 'block';
  $('search-modal').style.display = 'block';
  $('search-input').value = '';
  searchSelectedIndex = -1;
  filterSearch('');
  setTimeout(function() { $('search-input').focus(); }, 50);
}
function closeSearch() {
  $('search-overlay').style.display = 'none';
  $('search-modal').style.display = 'none';
  searchSelectedIndex = -1;
}
function filterSearch(q) {
  var results = $('search-results');
  q = q.toLowerCase().trim();
  searchSelectedIndex = -1;

  var valueMap = {};
  if (dashboardData && dashboardData.cards) {
    dashboardData.cards.forEach(function(c) {
      valueMap[c.metric] = c;
    });
  }

  var known = CATEGORIES.reduce(function(a,c){return a.concat(c.metrics)},[]);
  var extra = allMetrics.map(function(m){return m.Name}).filter(function(n){return !known.includes(n)});
  var all = known.concat(extra);
  var filtered = q ? all.filter(function(m) { return name(m).toLowerCase().includes(q) || m.includes(q); }) : all;

  results.innerHTML = '';
  if (!filtered.length) {
    results.innerHTML = '<div class="search-empty">' + t('no_metrics_found') + '</div>';
    return;
  }

  var colorMap = { heart:'var(--heart)', activity:'var(--activity)', mobility:'#f59e0b', sleep:'var(--sleep)', env:'#06b6d4', other:'var(--muted)' };
  var grouped = {}, catOrder = ['heart','activity','mobility','sleep','env','other'];
  filtered.forEach(function(m) {
    var cat = catOf(m);
    var key = cat ? cat.cat : 'other';
    if (!grouped[key]) grouped[key] = [];
    grouped[key].push(m);
  });

  var allItems = [];
  catOrder.forEach(function(key) {
    if (!grouped[key] || !grouped[key].length) return;
    var catKeys = catOrder.filter(function(k){ return grouped[k] && grouped[k].length; });
    if (!q || catKeys.length > 1) {
      var header = document.createElement('div');
      header.className = 'search-cat-header';
      header.innerHTML = '<span class="search-cat-dot" style="background:' + (colorMap[key]||'var(--muted)') + '"></span>' + getCatLabel(key);
      results.appendChild(header);
    }

    grouped[key].forEach(function(m) {
      var card = valueMap[m];
      var div = document.createElement('div');
      div.className = 'search-result-row';
      div.dataset.metric = m;

      var rightHtml = '';
      if (card && card.value != null) {
        var pct = card.prev > 0 ? ((card.value - card.prev) / card.prev * 100) : null;
        var trendCls = '', trendArrow = '';
        if (pct !== null) {
          trendCls = pct > 1 ? 'up' : pct < -1 ? 'down' : 'neutral';
          trendArrow = pct > 1 ? '\u2191' : pct < -1 ? '\u2193' : '';
        }
        rightHtml = '<span class="search-result-value">' + fmtVal(m, card.value) + '<span class="search-result-unit">' + fmtUnit(card.unit) + '</span></span>';
        if (trendArrow) rightHtml += '<span class="search-result-trend ' + trendCls + '">' + trendArrow + '</span>';
      }

      div.innerHTML = '<span class="search-result-name">' + name(m) + '</span>' + (rightHtml ? '<span class="search-result-right">' + rightHtml + '</span>' : '');
      div.onmouseenter = function() { setSearchSelected(allItems.indexOf(div)); };
      div.onclick = function() { closeSearch(); selectMetric(m); };
      results.appendChild(div);
      allItems.push(div);
    });
  });
}

function setSearchSelected(idx) {
  var items = $('search-results').querySelectorAll('.search-result-row');
  searchSelectedIndex = Math.max(0, Math.min(idx, items.length - 1));
  items.forEach(function(el, i) { el.classList.toggle('selected', i === searchSelectedIndex); });
  if (items[searchSelectedIndex]) items[searchSelectedIndex].scrollIntoView({ block: 'nearest' });
}

// ---- Formatters ----
function fmtVal(metric, v) {
  if (metric === 'walking_running_distance') return v.toFixed(2);
  if (SLEEP_METRICS.has(metric)) return v.toFixed(1);
  if (v >= 1000) return Math.round(v).toLocaleString();
  if (v % 1 === 0) return v;
  return v.toFixed(1);
}
function fmtUnit(u) {
  var map = {'count/min':'bpm','count':'','kcal':'kcal','km':'km','%':'%','ms':'ms','min':'min','hr':'h','degC':'C','dBASPL':'dB','ml/(kg*min)':'ml/kg/min','m':'m','m/s':'m/s'};
  return map[u] !== undefined ? map[u] : (u || '');
}
function fmt2(v) {
  if (v == null || isNaN(v)) return '\u2014';
  if (v >= 1000) return Math.round(v).toLocaleString();
  return v % 1 === 0 ? String(v) : v.toFixed(1);
}
function fmtAxisDate(label) {
  if (!label) return '';
  var dateStr = label.slice(0,10);
  var timeStr = label.length > 10 ? label.slice(11,16) : '';
  var d = new Date(dateStr + 'T12:00:00');
  var localeCode = LANG === 'ru' ? 'ru' : LANG === 'sr' ? 'sr-Latn' : 'en';
  var weekday = d.toLocaleDateString(localeCode, { weekday:'short' });
  var md = d.toLocaleDateString(localeCode, { month:'short', day:'numeric' });
  if (!timeStr || timeStr === '00:00') return weekday + ' ' + md;
  return md + ' ' + timeStr;
}
function chip(label, value, unit) {
  return '<div class="stat-chip"><div class="s-label">' + label + '</div><div class="s-value">' + value + (unit ? ' <span style="font-size:12px;color:var(--muted)">' + unit + '</span>' : '') + '</div></div>';
}

// ---- Navigation ----
function selectMetric(metric, fromExploreSection) {
  fromExplore = !!fromExploreSection;
  currentMetric = metric;
  compareEnabled = false;
  $('chart-metric-name').textContent = name(metric);
  $('compare-btn').classList.remove('active');
  $('compare-btn').style.display = SLEEP_METRICS.has(metric) ? 'none' : '';
  $('bucket').value = '';
  $('agg').value = '';
  $('from').value = daysAgoStr(29);
  $('to').value = todayStr();
  document.querySelectorAll('.preset-btn').forEach(function(b, i) { b.classList.toggle('active', i === 2); });
  $('dashboard-view').style.display = 'none';
  $('chart-view').style.display = 'block';
  window.scrollTo(0, 0);
  loadChart();
}
function showDashboard() {
  var wasFromExplore = fromExplore;
  currentMetric = null;
  compareEnabled = false;
  fromExplore = false;
  $('dashboard-view').style.display = 'block';
  $('chart-view').style.display = 'none';
  history.replaceState(null, '', location.pathname);
  if (wasFromExplore) {
    var toggle = $('explore-toggle');
    var content = $('explore-content');
    if (!content.classList.contains('open')) {
      toggle.classList.add('open');
      content.classList.add('open');
    }
    setTimeout(function() {
      $('explore-section').scrollIntoView({ behavior: 'smooth', block: 'start' });
    }, 50);
  } else {
    window.scrollTo(0, 0);
  }
}
function pushHash() {
  if (!currentMetric) { history.replaceState(null,'', location.pathname); return; }
  var p = new URLSearchParams({ metric: currentMetric, from: $('from').value, to: $('to').value });
  var b = $('bucket').value, a = $('agg').value;
  if (b) p.set('bucket', b);
  if (a) p.set('agg', a);
  history.replaceState(null,'', '#' + p.toString());
}
function readHash() {
  if (!location.hash) return;
  var p = new URLSearchParams(location.hash.slice(1));
  if (p.get('from')) $('from').value = p.get('from');
  if (p.get('to'))   $('to').value   = p.get('to');
  if (p.get('bucket')) $('bucket').value = p.get('bucket');
  if (p.get('agg'))    $('agg').value    = p.get('agg');
}

// ---- Controls ----
function applyPreset(days) {
  document.querySelectorAll('.preset-btn').forEach(function(b) { b.classList.remove('active'); });
  if (event && event.target) event.target.classList.add('active');
  $('from').value = daysAgoStr(days - 1);
  $('to').value = todayStr();
  $('bucket').value = '';
  loadChart();
}
function onDateChange() {
  document.querySelectorAll('.preset-btn').forEach(function(b) { b.classList.remove('active'); });
  loadChart();
}
function shiftRange(dir) {
  var from = new Date($('from').value + 'T12:00:00');
  var to   = new Date($('to').value + 'T12:00:00');
  var days = Math.round((to - from) / 86400000) + 1;
  from.setDate(from.getDate() + dir * days);
  to.setDate(to.getDate() + dir * days);
  $('from').value = from.toISOString().slice(0,10);
  $('to').value = to.toISOString().slice(0,10);
  document.querySelectorAll('.preset-btn').forEach(function(b) { b.classList.remove('active'); });
  loadChart();
}
function toggleCompare() {
  compareEnabled = !compareEnabled;
  $('compare-btn').classList.toggle('active', compareEnabled);
  loadChart();
}
function downloadCSV() {
  if (!lastPoints.length) return;
  var unit = fmtUnit(unitsMap[currentMetric] || '');
  var rows = [['date', unit || 'value']];
  lastPoints.forEach(function(p) { rows.push([p.date, p.qty]); });
  var csv = rows.map(function(r) { return r.join(','); }).join('\n');
  var a = document.createElement('a');
  a.href = 'data:text/csv;charset=utf-8,' + encodeURIComponent(csv);
  a.download = currentMetric + '_' + $('from').value + '_' + $('to').value + '.csv';
  a.click();
}

// ---- Keyboard ----
document.addEventListener('keydown', function(e) {
  var tag = document.activeElement.tagName;
  if (tag === 'INPUT' || tag === 'SELECT' || tag === 'TEXTAREA') {
    if (e.key === 'Escape') { document.activeElement.blur(); closeSearch(); return; }
    if ($('search-modal').style.display !== 'none') {
      var items = $('search-results').querySelectorAll('.search-result-row');
      if (e.key === 'ArrowDown') { e.preventDefault(); setSearchSelected(searchSelectedIndex + 1); return; }
      if (e.key === 'ArrowUp')   { e.preventDefault(); setSearchSelected(searchSelectedIndex - 1); return; }
      if (e.key === 'Enter' && items[searchSelectedIndex]) {
        var m = items[searchSelectedIndex].dataset.metric;
        closeSearch(); selectMetric(m); return;
      }
    }
    return;
  }
  switch(e.key) {
    case '/': e.preventDefault(); openSearch(); break;
    case 'Escape':
      if ($('search-modal').style.display !== 'none') closeSearch();
      else showDashboard();
      break;
    case 'ArrowLeft':  if (currentMetric) { e.preventDefault(); shiftRange(-1); } break;
    case 'ArrowRight': if (currentMetric) { e.preventDefault(); shiftRange(1); } break;
    case '1': case '2': case '3': case '4':
      if (currentMetric) {
        var days = [1,7,30,90][+e.key-1];
        document.querySelectorAll('.preset-btn').forEach(function(b,i) { b.classList.toggle('active', i===+e.key-1); });
        $('from').value = daysAgoStr(days - 1);
        $('to').value = todayStr();
        $('bucket').value = '';
        loadChart();
      }
      break;
  }
});

// ---- Time bands plugin ----
var TIME_BANDS = [
  { start:0, end:6, color:'rgba(100,80,140,0.06)', label:'Night' },
  { start:6, end:12, color:'rgba(255,190,60,0.05)', label:'Morning' },
  { start:12, end:18, color:'rgba(100,180,255,0.04)', label:'Day' },
  { start:18, end:24, color:'rgba(255,120,40,0.05)', label:'Evening' }
];
Chart.register({
  id: 'timeBands',
  beforeDraw: function(chart) {
    var labels = chart.data.labels;
    if (!labels || labels.length < 2 || labels[0].length <= 10) return;
    var ctx = chart.ctx, x = chart.scales.x, y = chart.scales.y;
    var top = y.top, bottom = y.bottom;
    var half = (x.getPixelForValue(1) - x.getPixelForValue(0)) / 2;
    function hourOf(lbl) { return parseInt(lbl.slice(11,13), 10); }
    function bandOf(h) { return TIME_BANDS.find(function(b) { return h >= b.start && h < b.end; }); }
    ctx.save();
    ctx.beginPath(); ctx.rect(x.left, top, x.right - x.left, bottom - top); ctx.clip();
    var cur = null, gStart = 0;
    function flush(endIdx) {
      if (!cur || endIdx < gStart) return;
      var x1 = x.getPixelForValue(gStart) - half;
      var x2 = x.getPixelForValue(endIdx) + half;
      ctx.fillStyle = cur.color;
      ctx.fillRect(x1, top, x2 - x1, bottom - top);
    }
    for (var i = 0; i < labels.length; i++) {
      var b = bandOf(hourOf(labels[i]));
      if (b !== cur) { flush(i - 1); cur = b; gStart = i; }
    }
    flush(labels.length - 1);
    ctx.restore();
  }
});

// ---- Sleep stacked chart ----
function loadSleepChart(from, to) {
  setLoading(true);
  Promise.all(SLEEP_PHASES.map(function(ph) {
    return fetch('/api/metrics/data?metric=' + ph.metric + '&from=' + from + '&to=' + to + '&bucket=day&agg=AVG').then(function(r){return r.json()});
  })).then(function(results) {
    setLoading(false);
    var labelSet = new Set();
    results.forEach(function(r) { (r.points || []).forEach(function(p) { labelSet.add(p.date); }); });
    var labels = Array.from(labelSet).sort();
    if (!labels.length) {
      $('stats-row').innerHTML = '<div style="color:var(--muted);padding:8px">' + t('no_sleep_data') + '</div>';
      if (chart) { chart.destroy(); chart = null; }
      return;
    }
    var ptMap = results.map(function(r) {
      var m = {}; (r.points||[]).forEach(function(p) { m[p.date] = p.qty; }); return m;
    });
    var datasets = SLEEP_PHASES.map(function(ph, i) {
      return { label: t(ph.labelKey), data: labels.map(function(l) { return ptMap[i][l] || 0; }), backgroundColor: ph.color + 'cc', borderColor: ph.color, borderWidth: 1, stack: 'sleep', borderRadius: 3 };
    });
    function avg(arr) { return arr.length ? arr.reduce(function(a,b){return a+b},0)/arr.length : 0; }
    $('stats-row').innerHTML =
      chip(t('nights'), labels.length, '') +
      chip(t('avg_total'), fmt2(avg(labels.map(function(l) { return SLEEP_PHASES.reduce(function(s,_,i) { return s+(ptMap[i][l]||0); },0); }))), 'h') +
      chip(t('avg_deep'), fmt2(avg((results[0].points||[]).map(function(p){return p.qty}))), 'h') +
      chip(t('avg_rem'), fmt2(avg((results[1].points||[]).map(function(p){return p.qty}))), 'h');

    if (chart) { chart.destroy(); chart = null; }
    chart = new Chart($('chart').getContext('2d'), {
      type: 'bar',
      data: { labels: labels.map(fmtAxisDate), datasets: datasets },
      options: {
        responsive: true, maintainAspectRatio: false,
        interaction: { mode: 'index', intersect: false },
        plugins: {
          legend: { display: true, labels: { color:'#78716c', boxWidth: 12, font: { size: 12 } } },
          tooltip: { backgroundColor:'#fff', borderColor:'#e7e5e4', borderWidth:1, titleColor:'#78716c', bodyColor:'#1c1917', callbacks: { label: function(ctx) { return ' ' + ctx.dataset.label + ': ' + fmt2(ctx.parsed.y) + ' h'; } } }
        },
        scales: {
          x: { stacked:true, ticks:{ color:'#78716c', font:{size:11} }, grid:{ color:'#f0efed' } },
          y: { stacked:true, ticks:{ color:'#78716c', font:{size:11}, callback: function(v) { return v+'h'; } }, grid:{ color:'#f0efed' } }
        }
      }
    });
    pushHash();
  });
}

// ---- Main chart ----
function loadChart() {
  if (!currentMetric) return;
  var from = $('from').value, to = $('to').value;
  var bucket = $('bucket').value, agg = $('agg').value;
  if (SLEEP_METRICS.has(currentMetric) && !bucket && !agg) return loadSleepChart(from, to);
  setLoading(true);
  var url = '/api/metrics/data?metric=' + encodeURIComponent(currentMetric) + '&from=' + from + '&to=' + to;
  if (bucket) url += '&bucket=' + bucket;
  if (agg) url += '&agg=' + agg;
  var mainPromise = fetch(url).then(function(r){return r.json()});
  var prevPromise = Promise.resolve({ points: [] });
  if (compareEnabled) {
    var fromD = new Date($('from').value + 'T12:00:00');
    var toD = new Date($('to').value + 'T12:00:00');
    var span = Math.round((toD - fromD) / 86400000) + 1;
    var prevFrom = new Date(fromD); prevFrom.setDate(prevFrom.getDate() - span);
    var prevTo = new Date(toD); prevTo.setDate(prevTo.getDate() - span);
    var prevUrl = url.replace('from='+$('from').value, 'from='+prevFrom.toISOString().slice(0,10)).replace('to='+$('to').value, 'to='+prevTo.toISOString().slice(0,10));
    prevPromise = fetch(prevUrl).then(function(r){return r.json()}).catch(function(){return {points:[]};});
  }
  Promise.all([mainPromise, prevPromise]).then(function(res) {
    var data = res[0], prevData = res[1];
    setLoading(false);
    var pts = data.points || [];
    var prevPts = prevData.points || [];
    lastPoints = pts;
    if (pts.length) {
      var vals = pts.map(function(p){return p.qty});
      var avgV = vals.reduce(function(a,b){return a+b},0) / vals.length;
      var unit = fmtUnit(unitsMap[currentMetric] || '');
      $('stats-row').innerHTML = chip(t('points'), pts.length.toLocaleString(), '') + chip(t('avg'), fmt2(avgV), unit) + chip(t('min'), fmt2(Math.min.apply(null,vals)), unit) + chip(t('max'), fmt2(Math.max.apply(null,vals)), unit);
    } else {
      $('stats-row').innerHTML = '<div style="color:var(--muted);padding:8px">' + t('no_data_range') + '</div>';
    }
    if (!pts.length) { if (chart) { chart.destroy(); chart = null; } pushHash(); return; }
    var labels = pts.map(function(p){return p.date});
    var avgVals = pts.map(function(p){return p.qty});
    var minVals = pts.map(function(p){return p.min});
    var maxVals = pts.map(function(p){return p.max});
    var isBar = BAR_METRICS.has(currentMetric);
    var hasRange = !isBar && pts.length > 1 && (maxVals[0] - minVals[0]) > 0.01;
    var sparse = pts.length > 200;
    var cat = catOf(currentMetric);
    var cmap = { heart:'#e11d48', activity:'#059669', mobility:'#f59e0b', sleep:'#7c3aed', env:'#06b6d4' };
    var lineColor = cat ? (cmap[cat.cat] || '#2563eb') : '#2563eb';
    if (chart) { chart.destroy(); chart = null; }
    var ctx = $('chart').getContext('2d');
    var prevVals = prevPts.length ? (function() { var step = prevPts.length / pts.length; return pts.map(function(_,i) { var pi = Math.round(i*step); return pi < prevPts.length ? prevPts[pi].qty : null; }); })() : [];
    var datasets = [];
    if (hasRange && !compareEnabled) {
      datasets.push({ data: maxVals, borderWidth:0, pointRadius:0, fill:'+1', backgroundColor: lineColor+'15', tension:0.2, label:'_max' });
      datasets.push({ data: minVals, borderWidth:0, pointRadius:0, fill:false, tension:0.2, label:'_min' });
    }
    datasets.push({ label: name(currentMetric), data: avgVals, borderColor: lineColor, backgroundColor: isBar ? lineColor+'77' : lineColor+'12', borderWidth: isBar ? 0 : 2, pointRadius: sparse ? 0 : 2, tension: 0.2, fill: isBar ? false : (!hasRange && !compareEnabled), type: isBar ? 'bar' : 'line', order: 1, borderRadius: isBar ? 4 : 0 });
    if (prevVals.length) {
      datasets.push({ label: t('previous_period'), data: prevVals, borderColor: lineColor+'55', backgroundColor: 'transparent', borderWidth: 1.5, borderDash:[4,3], pointRadius:0, tension:0.2, fill:false, type:'line', order:2 });
    }
    chart = new Chart(ctx, {
      type: isBar ? 'bar' : 'line',
      data: { labels: labels, datasets: datasets },
      options: {
        responsive: true, maintainAspectRatio: false,
        interaction: { mode:'index', intersect:false },
        plugins: {
          legend: { display: compareEnabled, labels: { color:'#78716c', boxWidth:12, font:{size:12}, filter: function(item) { return !item.text.startsWith('_'); } } },
          tooltip: { backgroundColor:'#fff', borderColor:'#e7e5e4', borderWidth:1, titleColor:'#78716c', bodyColor:'#1c1917', callbacks: { title: function(items) { return fmtAxisDate(items[0].label); }, label: function(ctx) { if (ctx.dataset.label && ctx.dataset.label.startsWith('_')) return null; var u = fmtUnit(unitsMap[currentMetric]||''); return ' ' + ctx.dataset.label + ': ' + fmt2(ctx.parsed.y) + (u ? ' '+u : ''); } } }
        },
        scales: {
          x: { ticks: { color:'#78716c', maxTicksLimit:10, font:{size:11}, callback: function(_,i) { return fmtAxisDate(labels[i]); } }, grid: { color:'#f0efed' } },
          y: { beginAtZero: isBar, ticks:{ color:'#78716c', font:{size:11} }, grid:{ color:'#f0efed' } }
        }
      }
    });
    pushHash();
  });
}

function setLoading(on) { $('chart-loading').style.display = on ? 'flex' : 'none'; }
`
