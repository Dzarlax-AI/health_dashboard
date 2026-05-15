package ui

import (
	"net/http"
)

// translations holds all UI strings keyed by language and then by key.
var translations = map[string]map[string]string{
	"en": {
		"app_title": "Health",
		"explore": "Explore",
		"loading": "Loading your health data",
		"readiness": "Readiness",
		"recovery": "Recovery",
		"readiness_today_label": "Today",
		"readiness_trend_label": "7-day trend",
		"back": "Back",
		"compare": "Compare",
		"all_metrics":  "All metrics",
		"nav_settings": "Settings",
		"your_trends": "Your trends",
		"search_placeholder": "Search metrics...",
		"esc_hint": "ESC to close",
		"no_metrics_found": "No metrics found",
		"no_data": "No data",
		"no_data_range": "No data for this range",
		"no_sleep_data": "No sleep data for this range",
		"start_syncing": "Start syncing health data to see your readiness score.",
		"data_from": "Data from ",
		"days_ago": "d ago",
		"this_week": "This week",
		"activity_vs_recovery": "Activity vs Recovery",
		"activity_recovery_subtitle": "How physical load affects your HRV",
		"activity_load": "Activity load",
		"sleep_section": "Sleep",
		"sleep_subtitle": "7-night average",
		"deep_sleep": "Deep sleep",
		"rem_sleep": "REM sleep",
		"awake_time": "Awake time",
		"efficiency": "Efficiency",
		"bucket": "Bucket",
		"agg": "Agg",
		"auto": "Auto",
		"minute": "Minute",
		"hour": "Hour",
		"day": "Day",
		"preset_all": "All",
		"avg": "Avg",
		"sum": "Sum",
		"max": "Max",
		"min": "Min",
		"previous_period": "Previous period",
		"vs_yesterday": "vs yesterday",
		"stable": "Stable",
		"load_pct": "Load %",
		"hrv_ms": "HRV ms",
		"nights": "Nights",
		"avg_total": "Avg total",
		"avg_deep": "Avg deep",
		"avg_rem": "Avg REM",
		"points": "Points",
		"stale_prefix": "Data from ",
		"stale_suffix": "d ago",
		"status_good": "Looking good",
		"status_fair": "Needs attention",
		"status_low": "Take care",
		"cat_heart": "Heart & Vitals",
		"cat_activity": "Activity",
		"cat_fitness": "Fitness",
		"cat_sleep": "Sleep",
		"cat_body": "Body",
		"cat_env": "Environment",
		"cat_nutrition": "Nutrition",
		"cat_other": "Other",
		"phase_deep": "Deep",
		"phase_rem": "REM",
		"phase_core": "Core",
		"phase_awake": "Awake",
		"trend_steps": "Steps",
		"trend_heart_rate": "Heart Rate",
		"trend_sleep": "Sleep",
		"trend_hrv": "HRV",
		"trend_readiness": "Readiness",
		"ai_insight_title": "Detailed AI briefing",
		"teps": "Steps",
		"leep": "Sleep",
		"ate": "Respiratory Rate",
		"metric_sleep_total": "Total Sleep",
		"metric_night_sleep_total": "Night Sleep",
		"metric_nap_total": "Naps",
		"metric_sleep_deep": "Deep Sleep",
		"metric_sleep_rem": "REM Sleep",
		"metric_sleep_core": "Core Sleep",
		"metric_sleep_unspecified": "Asleep (no stages)",
		"metric_sleep_awake": "Awake Time",
		"chart_sleep_unspecified_hint": "source did not report deep/REM/core breakdown",
		"metric_heart_rate": "Heart Rate",
		"metric_resting_heart_rate": "Resting HR",
		"metric_walking_heart_rate_average": "Walking HR",
		"metric_heart_rate_variability": "HRV",
		"metric_blood_oxygen_saturation": "Blood Oxygen",
		"metric_respiratory_rate": "Respiratory Rate",
		"metric_step_count": "Steps",
		"metric_walking_running_distance": "Distance",
		"metric_active_energy": "Active Calories",
		"metric_basal_energy_burned": "Resting Calories",
		"metric_apple_exercise_time": "Exercise",
		"metric_apple_stand_time": "Stand Time",
		"metric_apple_stand_hour": "Stand Hours",
		"metric_physical_effort": "Physical Effort",
		"metric_flights_climbed": "Flights Climbed",
		"metric_stair_speed_up": "Stair Speed",
		"metric_walking_speed": "Walking Speed",
		"metric_walking_step_length": "Step Length",
		"metric_walking_double_support_percentage": "Double Support",
		"metric_walking_asymmetry_percentage": "Walking Asymmetry",
		"metric_apple_sleeping_wrist_temperature": "Wrist Temp",
		"metric_breathing_disturbances": "Breathing Disturbances",
		"metric_environmental_audio_exposure": "Noise Exposure",
		"metric_headphone_audio_exposure": "Headphone Volume",
		"metric_time_in_daylight": "Daylight",
		"metric_vo2_max": "VO2 Max",
		"metric_six_minute_walking_test_distance": "6-min Walk",
		"metric_readiness": "Readiness",
		"metric_oxygen_saturation": "Blood Oxygen",
		"metric_heart_rate_variability_sdnn": "HRV",
		"metric_environmental_audio": "Ambient Noise",
		"metric_headphone_audio": "Headphone Volume",
		"metric_walking_double_support": "Double Support",
		"metric_walking_asymmetry": "Walking Asymmetry",
		"metric_environmental_sound_reduction": "Noise Reduction",
		"metric_stair_ascent_speed": "Stair Ascent",
		"metric_stair_descent_speed": "Stair Descent",
		"metric_wrist_temperature": "Wrist Temp",
		"metric_walking_steadiness": "Walking Steadiness",
		"metric_body_mass": "Body Weight",
		"metric_body_mass_index": "BMI",
		"metric_body_fat_percentage": "Body Fat",
		"metric_lean_body_mass": "Lean Mass",
		"metric_height": "Height",
		"metric_blood_pressure_systolic": "Systolic BP",
		"metric_blood_pressure_diastolic": "Diastolic BP",
		"metric_heart_rate_recovery": "HR Recovery",
		"metric_distance_cycling": "Cycling Distance",
		"metric_distance_swimming": "Swimming Distance",
		"metric_swimming_stroke_count": "Swim Strokes",
		"metric_mindful_minutes": "Mindful Minutes",
		"metric_alcoholic_beverages": "Alcoholic Beverages",
		"metric_six_min_walk_distance": "6-min Walk",
		"metric_dietary_energy": "Dietary Calories",
		"metric_dietary_protein": "Protein",
		"metric_dietary_carbs": "Carbohydrates",
		"metric_dietary_fat": "Total Fat",
		"metric_dietary_fat_saturated": "Saturated Fat",
		"metric_dietary_fat_monounsaturated": "Mono Fat",
		"metric_dietary_fat_polyunsaturated": "Poly Fat",
		"metric_dietary_water": "Water",
		"metric_dietary_sodium": "Sodium",
		"metric_dietary_sugar": "Sugar",
		"metric_dietary_fiber": "Fiber",
		"metric_dietary_caffeine": "Caffeine",
		"metric_dietary_calcium": "Calcium",
		"metric_dietary_iron": "Iron",
		"metric_dietary_cholesterol": "Cholesterol",
		"metric_dietary_potassium": "Potassium",
		"metric_dietary_magnesium": "Magnesium",
		"metric_dietary_phosphorus": "Phosphorus",
		"metric_dietary_zinc": "Zinc",
		"metric_dietary_copper": "Copper",
		"metric_dietary_manganese": "Manganese",
		"metric_dietary_selenium": "Selenium",
		"metric_dietary_iodine": "Iodine",
		"metric_dietary_molybdenum": "Molybdenum",
		"metric_dietary_folate": "Folate",
		"metric_dietary_biotin": "Biotin (B7)",
		"metric_dietary_vitamin_a": "Vitamin A",
		"metric_dietary_vitamin_c": "Vitamin C",
		"metric_dietary_vitamin_d": "Vitamin D",
		"metric_dietary_vitamin_e": "Vitamin E",
		"metric_dietary_vitamin_k": "Vitamin K",
		"metric_dietary_vitamin_b6": "Vitamin B6",
		"metric_dietary_vitamin_b12": "Vitamin B12",
		"metric_dietary_niacin": "Niacin (B3)",
		"metric_dietary_riboflavin": "Riboflavin (B2)",
		"metric_dietary_thiamin": "Thiamin (B1)",
		"metric_dietary_pantothenic_acid": "Pantothenic Acid (B5)",
		"by_source": "Sources",
		"source_comparison": "Source comparison",
		"lbl_total": "Total",
		"lbl_deep": "Deep",
		"lbl_core": "Core",
		"how_it_works": "How it works",
		"health_sections": "Health overview",
		"at_a_glance": "At a glance",

		// Dual-baseline trend chips on metric cards
		"trend_vs_7d":  "vs 7d",
		"trend_vs_30d": "vs 30d",

		// Energy Bank widget (Bevel-inspired prescriptive verdict)
		"energy_label":          "Energy Bank",
		"energy_capacity_label": "Today's capacity",
		"energy_current_label":  "Available now",
		"energy_drain_label":    "Used so far",
		"energy_verdict_push_hard":       "Push hard",
		"energy_verdict_moderate":        "Moderate day",
		"energy_verdict_active_recovery": "Active recovery only",
		"energy_verdict_rest":            "Rest day",
		"energy_component_morning_capacity": "Morning capacity",
		"energy_component_activity_load":    "Activity load (today vs 28-day chronic)",
		"energy_component_autonomic_stress": "Autonomic stress (RHR / HRV)",
		"energy_state_good_title":     "Charged",
		"energy_state_good_desc":      "Plenty in the tank — you can challenge yourself today.",
		"energy_state_medium_title":   "Balanced",
		"energy_state_medium_desc":    "Decent reserves — train with intent but watch the volume.",
		"energy_state_low_title":      "Running low",
		"energy_state_low_desc":       "Reserves are thin — keep effort easy and prioritise recovery.",
		"energy_state_critical_title": "Depleted",
		"energy_state_critical_desc":  "Tank near empty — rest is the highest-yield choice today.",
		"details": "Details",

		"admin_title": "Settings",
		"admin_cache_status": "Cache status",
		"admin_refresh": "Refresh",
		"admin_actions": "Actions",
		"admin_raw": "Raw data",
		"admin_minute": "Minute cache",
		"admin_hourly": "Hourly cache",
		"admin_daily": "Daily scores",
		"admin_metrics": "metrics",
		"admin_empty": "empty",
		"admin_score_version": "Score version",
		"admin_last_sync": "Last sync",
		"admin_incremental_title": "Update cache",
		"admin_incremental_desc": "Fill missing entries since last run. Fast, safe to run anytime.",
		"admin_force_title": "Rebuild all",
		"admin_force_desc": "Clear and recompute all caches from raw data. Use after formula changes.",
		"admin_run": "Run",
		"admin_force_run": "Rebuild",
		"admin_target_user": "Target user",
		"admin_target_user_current": "— current user —",
		"admin_notify_title": "Telegram reports",
		"admin_notify_morning_title": "Morning report",
		"admin_notify_morning_desc": "Send a test sleep summary right now.",
		"admin_notify_evening_title": "Evening report",
		"admin_notify_evening_desc": "Send a test day summary right now.",
		"admin_notify_token": "Bot token",
		"admin_notify_chat_id": "Chat ID",
		"admin_notify_lang": "Language",
		"admin_notify_timezone": "Timezone",
		"admin_notify_timezone_hint": "Required for daily reports and historical EnergyBank computation. Use an IANA name (e.g. Europe/Belgrade, America/New_York).",
		"admin_energy_backfill_title": "Historical EnergyBank",
		"admin_energy_backfill_desc": "Compute retrospective EnergyBank snapshots from your imported Apple Health history. Required for the per-user verdict calibration to use your own distribution instead of cold-start defaults.",
		"admin_energy_backfill_loading": "Loading…",
		"admin_energy_backfill_load_error": "Failed to load backfill status.",
		"admin_energy_backfill_summary": "{complete} days of complete health history · {backfilled} EnergyBank snapshots already backfilled · range: {earliest} → {to}",
		"admin_energy_backfill_run": "Compute historical EnergyBank",
		"admin_energy_backfill_running": "A backfill is already running.",
		"admin_energy_backfill_need_tz": "Set your Timezone above first.",
		"admin_energy_backfill_no_data": "No complete days yet — import Apple Health data or wait for live ingest.",
		"admin_energy_backfill_confirm": "This recomputes EnergyBank for every historical day. Continue?",
		"admin_energy_backfill_starting": "Starting…",
		"admin_energy_backfill_progress": "Processing day {done} of {total} · ok={ok}",
		"admin_energy_backfill_done": "Done: {ok} written · {skipped} skipped (insufficient lookback) · {errors} errors",
		"admin_notify_schedule_morning": "Morning report hour",
		"admin_notify_schedule_evening": "Evening report hour",
		"admin_notify_weekday": "Weekdays",
		"admin_notify_weekend": "Weekends",
		"admin_notify_save": "Save",
		"admin_notify_saved": "Settings saved",
		"admin_notify_send": "Send test",
		"admin_notify_test_morning": "Test morning",
		"admin_notify_test_evening": "Test evening",
		"admin_gaps_section_title": "Data integrity",
		"admin_gaps_check":         "Check gaps",
		"admin_quality_title":      "Data quality audit",
		"admin_quality_run":        "Run audit",
		"admin_quality_fix":        "Mark suspects + clean impossibles",
		"admin_quality_digest":     "Send digest now",
		"admin_quality_clean":      "All clean — no anomalies found.",
		"admin_quality_total":      "Total rows",
		"admin_quality_bad":        "Out of range",
		"admin_quality_range":      "Range",
		"admin_quality_metric":     "Metric",
		"admin_quality_sample":     "Sample",
		"admin_quality_week":       "Last 7 days",
		"admin_quality_impossible": "Impossible",
		"admin_quality_suspect":    "Suspect",
		"admin_quality_missed":     "Missed nights",
		"admin_quality_fixed":      "Flagged %d impossible, %d suspect rows.",
		"admin_ai_title":      "AI Morning Briefing",
		"admin_ai_key":        "Gemini API key",
		"admin_ai_model":      "Model",
		"admin_ai_max_tokens": "Max output tokens",
		"admin_ai_save":       "Save",
		"admin_ai_saved":      "Settings saved",
		"admin_energy_title":           "EnergyBank v2.2 — Stress drain",
		"admin_energy_warning":         "Non-production placeholder values until §4.5 validation rubric returns 'validated'. Effective β is 0 while Stress drain is disabled, regardless of β value below.",
		"admin_energy_stress_enabled":  "Stress drain enabled",
		"admin_energy_beta":            "β (stress drain coefficient)",
		"admin_energy_z_threshold":     "z-score threshold",
		"admin_energy_effective_beta":  "Effective β (live)",
		"admin_energy_save":            "Save",
		"admin_energy_saved":           "Energy settings saved",
		"admin_help_summary":           "What is this and when to change?",
		"admin_energy_help_html": `<h4 style="margin:8px 0 4px">β — stress-drain coefficient</h4>
<p>Multiplier that converts <code>sustained_hr_load_z</code> into drain points. Formula: <code>drain = α·active_kcal + <strong>β</strong>·load_z</code>.</p>
<p><strong>Default 0.8</strong> (spec §6 Q3 placeholder). Adjust within 0.4–1.2 once the validation rubric below clears, depending on how aggressively you want the bar to react to sustained autonomic load.</p>
<h4 style="margin:12px 0 4px">z-threshold — per-hour floor</h4>
<p>Only hours where HR runs more than this many SDs above your awake baseline contribute to the load integral.</p>
<p><strong>Default 0.5.</strong> Raise to 0.7–0.8 if you have a sedentary lifestyle and HR sits at z≈0.5 systematically without real stress (false positives from mental activation, not autonomic load).</p>
<h4 style="margin:12px 0 4px">Stress drain enabled — master switch</h4>
<p>When OFF (default), <code>sustained_hr_load_z</code> still computes and lands in the <code>components</code> JSONB audit trail, but never multiplies into the bank — β_effective = 0.</p>
<p><strong>Only switch ON after the validation rubric below returns <code>validated</code>.</strong> Flipping it earlier means drain will react to noise.</p>
<h4 style="margin:12px 0 4px">Workflow</h4>
<ol style="margin:4px 0 0 18px;padding:0">
  <li>Run the validation rubric (button below).</li>
  <li>Verdict ≠ validated → keep β at 0, don't change anything. Monthly Telegram nudge will fire when verdict transitions.</li>
  <li>Verdict = validated → flip Stress drain enabled ON, set β=0.8.</li>
  <li>After ~2 weeks, if drain feels too aggressive, dial β down to 0.4–0.5. Re-run rubric monthly.</li>
</ol>`,
		"admin_stress_validation_help_html": `<h4 style="margin:8px 0 4px">What the rubric measures</h4>
<p>Pearson r on a rolling 30-day window between <code>sustained_hr_load[d]</code> and three independent next-morning recovery signals. Logic: if the formula really catches stress, a high-load day predicts degraded autonomic markers the next morning.</p>
<h4 style="margin:12px 0 4px">Channels</h4>
<dl style="margin:4px 0">
  <dt><strong>Channel 1 — HRV next-morning (primary, anchor)</strong></dt>
  <dd>Correlation of load[d] with overnight HRV[d+1]. Expected sign: <strong>negative</strong> (high load → depressed HRV).</dd>
  <dt style="margin-top:6px"><strong>Channel 2 — RHR next-morning shift (secondary)</strong></dt>
  <dd>load[d] vs (overnight RHR[d+1] − 30d baseline). Expected sign: <strong>positive</strong>. Cross-check.</dd>
  <dt style="margin-top:6px"><strong>Channel 3 — sleep architecture (tertiary)</strong></dt>
  <dd>Vote across 3 sub-correlations: sleep_awake (+), onset latency (+), deep% (−). Each votes when |r|≥0.10 in expected direction; channel "agrees" at ≥2 votes.</dd>
</dl>
<h4 style="margin:12px 0 4px">Verdicts</h4>
<ul style="margin:4px 0 0 18px;padding:0">
  <li><strong style="color:var(--good)">validated</strong> — r ≤ −0.30 on channel 1 AND at least one cross-channel agrees. Safe to flip <code>Stress drain enabled</code>.</li>
  <li><strong style="color:var(--fair)">weak</strong> — −0.30 &lt; r &lt; −0.10. β stays at 0. Re-check next month.</li>
  <li><strong style="color:var(--low)">inconclusive</strong> — |r| &lt; 0.10 on channel 1, or fewer than 15 HRV samples (sparsity fallback). Acquire more nightly HRV (Breathe sessions on Apple Watch help).</li>
  <li><strong style="color:var(--low)">wrong_direction</strong> — r &gt; 0. Formula isn't capturing this user's physiology — escalate to manual review.</li>
</ul>
<h4 style="margin:12px 0 4px">When to run</h4>
<ul style="margin:4px 0 0 18px;padding:0">
  <li>After any major data ingest (Apple Health import, sleep restore).</li>
  <li>Once a month as routine. Monthly Telegram nudge auto-fires when verdict newly transitions to <strong>validated</strong>.</li>
  <li>Before flipping <code>Stress drain enabled</code> ON above.</li>
</ul>`,
		"admin_stress_validation_title":        "Stress validation rubric (§4.5)",
		"admin_stress_validation_desc":         "Runs Pearson r over the rolling 30-day window: HRV next-morning (primary), RHR next-morning shift, sleep architecture. Read-only — does NOT auto-flip stress_drain_enabled. Operator decision after reviewing the verdict.",
		"admin_stress_validation_run":          "Run validation",
		"admin_stress_validation_loading":      "Running rubric over rolling 30-day window…",
		"admin_stress_validation_sparse":       "sparse",
		"admin_stress_validation_no_data":      "no data",
		"admin_stress_validation_flags_label":  "flags:",
		"admin_stress_validation_ch1":          "Channel 1 (HRV next-morning)",
		"admin_stress_validation_ch2":          "Channel 2 (RHR next-morning shift)",
		"admin_stress_validation_ch3":          "Channel 3 (sleep architecture)",
		"admin_stress_validation_votes":        "votes",
		"admin_stress_validation_window_fmt":   "window {window} days, {days} days with data",
		"admin_import_title": "Apple Health Import",
		"admin_import_desc": "Upload your Apple Health export.zip to import historical data. Duplicates are skipped automatically.",
		"admin_import_choose": "Choose fileâ¦",
		"admin_import_batch": "points/batch",
		"admin_import_pause": "ms pause",
		"admin_import_start": "Start import",
		// v2.2 hero-row stress-flag chips — duplicated from
		// internal/health/i18n_en.go because dashboard.html uses
		// ui.T (this map), not health.LangStrings.
		"stress_flags_aria":                         "Stress signal flags",
		// Section labels shared by every flag's detail block.
		"stress_detail_what":   "What it is",
		"stress_detail_cause":  "What caused it",
		"stress_detail_risk":   "Why it matters",
		"stress_detail_action": "What to do",
		// Per-flag chip labels (short) + detail HTML (4 sections
		// for the 5 health flags, 2 for operational state).
		"stress_flag_acute_stress_label": "HR spike",
		"stress_flag_acute_stress_detail_html": `<h5>What it is</h5>
<p>Your heart rate jumped sharply above your normal for one hour today.</p>
<h5>What caused it</h5>
<p>Coffee, an unexpected call, pre-meeting nerves, a startle, a brief conflict — any short sympathetic-nervous-system activation.</p>
<h5>Why it matters</h5>
<p>It doesn't, on its own. An isolated spike doesn't load the system — this is a normal reaction.</p>
<h5>What to do</h5>
<p>Nothing. The flag exists for diagnostics — so you can see it was a one-off, not chronic stress.</p>`,
		"stress_flag_sustained_load_label": "Sustained HR",
		"stress_flag_sustained_load_detail_html": `<h5>What it is</h5>
<p>Your heart rate stayed above your normal for at least 4 hours in a row today.</p>
<h5>What caused it</h5>
<p>A long stressful day (deadline, negotiations), a hard workout with slow recovery, the start of an illness, dehydration, sleep deprivation.</p>
<h5>Why it matters</h5>
<p>Real load on the autonomic nervous system. A few days like this in a row and energy debt accumulates — recovery slows. EnergyBank already factored this in: the bar drained more than usual today.</p>
<h5>What to do</h5>
<p>Tonight: early bed, no alcohol or caffeine after lunch. Tomorrow: light activity, don't repeat the load.</p>`,
		"stress_flag_illness_signature_label": "Possible illness",
		"stress_flag_illness_signature_detail_html": `<h5>What it is</h5>
<p>Body temperature, breathing rate and HRV all moved away from your normal in the "illness" direction together.</p>
<h5>What caused it</h5>
<p>Most often a viral infection in its early stage. Less often — serious overtraining with a depressed immune system.</p>
<h5>Why it matters</h5>
<p>Pushing a hard workout through this usually drags the illness out by 3-5 extra days. Your immune system is working off the infection; loading it with sport on top is double duty.</p>
<h5>What to do</h5>
<p>Sleep today. Skip the workout or make it minimal (a slow walk). Water, warm food. Check again tomorrow — if the flag is gone, ramp back up gradually.</p>`,
		"stress_flag_recovery_debt_label": "Not recovered",
		"stress_flag_recovery_debt_detail_html": `<h5>What it is</h5>
<p>Overnight HRV dropped below your normal, and resting heart rate rose above it. Sleep didn't clear yesterday's load.</p>
<h5>What caused it</h5>
<p>Yesterday's hard training or work day, a late meal, alcohol, late bedtime, emotional stress.</p>
<h5>Why it matters</h5>
<p>You're not in optimal form today. Ignoring this and going hard raises the overtraining risk and the chance of a minor injury.</p>
<h5>What to do</h5>
<p>Easy day: walking, mobility, yoga. Eat well, go to bed early. The flag should clear by tomorrow — if not, two easy days in a row beat earning a real overtraining hole.</p>`,
		"stress_flag_parasympathetic_rebound_label": "Recovering",
		"stress_flag_parasympathetic_rebound_detail_html": `<h5>What it is</h5>
<p>Heart rate was elevated, but HRV was also elevated. The parasympathetic nervous system is active — that's a recovery mode, not stress.</p>
<h5>What caused it</h5>
<p>Usually after a hard workout or intense day. The body spent resources and is now actively rebuilding.</p>
<h5>Why it matters</h5>
<p>It doesn't — this is a healthy reaction. <strong>Don't mistake it for acute stress</strong>: heart rate looks high, but the physiology is entirely different.</p>
<h5>What to do</h5>
<p>Give the body time: easy day, normal sleep. Training is fine, just not at the limit.</p>`,
		"stress_flag_stale_stress_label": "Low data",
		"stress_flag_stale_stress_detail_html": `<h5>What it is</h5>
<p>Less than 8 hours of heart-rate data were collected during your waking hours today. The stress formula doesn't compute for this day.</p>
<h5>What to do</h5>
<p>Wear the watch more consistently. Check that iPhone sync is working. No action for today's numbers — the flag clears automatically as soon as there's >8 hours of data.</p>`,
		"stress_flag_calibration_warmup_label": "Calibrating",
		"stress_flag_calibration_warmup_detail_html": `<h5>What it is</h5>
<p>Your personal baseline (HRV, heart rate, breathing) is still being learned — needs about a week of consistent data. Stress-flag thresholds stay conservative for now.</p>
<h5>What to do</h5>
<p>Just keep wearing the watch. The flag clears automatically after a few more days.</p>`,
		"admin_import_uploading": "Uploadingâ¦",
		"admin_import_running": "Import runningâ¦",
	},
	"ru": {
		"app_title": "Здоровье",
		"explore": "Поиск",
		"loading": "Загрузка данных",
		"readiness": "Готовность",
		"recovery": "Восстановление",
		"readiness_today_label": "Сегодня",
		"readiness_trend_label": "Тренд 7д",
		"back": "Назад",
		"compare": "Сравнить",
		"all_metrics":  "Все метрики",
		"nav_settings": "Настройки",
		"your_trends": "Ваши тренды",
		"search_placeholder": "Поиск метрик...",
		"esc_hint": "ESC — закрыть",
		"no_metrics_found": "Метрики не найдены",
		"no_data": "Нет данных",
		"no_data_range": "Нет данных за этот период",
		"no_sleep_data": "Нет данных о сне за этот период",
		"start_syncing": "Начните синхронизацию данных о здоровье.",
		"data_from": "Данные от ",
		"days_ago": "д. назад",
		"this_week": "Эта неделя",
		"activity_vs_recovery": "Активность и восстановление",
		"activity_recovery_subtitle": "Как нагрузка влияет на ВСР",
		"activity_load": "Нагрузка",
		"sleep_section": "Сон",
		"sleep_subtitle": "Среднее за 7 ночей",
		"deep_sleep": "Глубокий сон",
		"rem_sleep": "REM сон",
		"awake_time": "Бодрствование",
		"efficiency": "Эффективность",
		"bucket": "Период",
		"agg": "Агр.",
		"auto": "Авто",
		"minute": "Минута",
		"hour": "Час",
		"day": "День",
		"preset_all": "Всё",
		"avg": "Ср.",
		"sum": "Сумма",
		"max": "Макс",
		"min": "Мин",
		"previous_period": "Прошлый период",
		"vs_yesterday": "к вчера",
		"stable": "Стабильно",
		"load_pct": "Нагрузка %",
		"hrv_ms": "ВСР мс",
		"nights": "Ночей",
		"avg_total": "Ср. всего",
		"avg_deep": "Ср. глубокий",
		"avg_rem": "Ср. REM",
		"points": "Точки",
		"stale_prefix": "Данные от ",
		"stale_suffix": "д. назад",
		"status_good": "Хорошо",
		"status_fair": "Требует внимания",
		"status_low": "Берегите себя",
		"cat_heart": "Сердце и показатели",
		"cat_activity": "Активность",
		"cat_fitness": "Фитнес",
		"cat_sleep": "Сон",
		"cat_body": "Тело",
		"cat_env": "Окружающая среда",
		"cat_nutrition": "Питание",
		"cat_other": "Прочее",
		"phase_deep": "Глубокий",
		"phase_rem": "REM",
		"phase_core": "Основной",
		"phase_awake": "Бодрствование",
		"trend_steps": "Шаги",
		"trend_heart_rate": "ЧСС",
		"trend_sleep": "Сон",
		"trend_hrv": "ВСР",
		"trend_readiness": "Готовность",
		"ai_insight_title": "Развёрнутый AI-отчёт",
		"teps": "Шаги",
		"leep": "Сон",
		"ate": "ЧДД",
		"metric_sleep_total": "Общий сон",
		"metric_night_sleep_total": "Ночной сон",
		"metric_nap_total": "Дневной сон",
		"metric_sleep_deep": "Глубокий сон",
		"metric_sleep_rem": "REM сон",
		"metric_sleep_core": "Основной сон",
		"metric_sleep_unspecified": "Сон (без стадий)",
		"metric_sleep_awake": "Бодрствование",
		"chart_sleep_unspecified_hint": "источник не предоставил разбивку на фазы",
		"metric_heart_rate": "ЧСС",
		"metric_resting_heart_rate": "Пульс покоя",
		"metric_walking_heart_rate_average": "Пульс при ходьбе",
		"metric_heart_rate_variability": "ВСР",
		"metric_blood_oxygen_saturation": "Кислород крови",
		"metric_respiratory_rate": "ЧДД",
		"metric_step_count": "Шаги",
		"metric_walking_running_distance": "Дистанция",
		"metric_active_energy": "Акт. калории",
		"metric_basal_energy_burned": "Калории покоя",
		"metric_apple_exercise_time": "Упражнения",
		"metric_apple_stand_time": "Время стоя",
		"metric_apple_stand_hour": "Часы стоя",
		"metric_physical_effort": "Физ. нагрузка",
		"metric_flights_climbed": "Пролёты лестниц",
		"metric_stair_speed_up": "Скорость по лестнице",
		"metric_walking_speed": "Скорость ходьбы",
		"metric_walking_step_length": "Длина шага",
		"metric_walking_double_support_percentage": "Двойная опора",
		"metric_walking_asymmetry_percentage": "Асимметрия ходьбы",
		"metric_apple_sleeping_wrist_temperature": "Темп. запястья",
		"metric_breathing_disturbances": "Нарушения дыхания",
		"metric_environmental_audio_exposure": "Шумовая нагрузка",
		"metric_headphone_audio_exposure": "Громкость наушников",
		"metric_time_in_daylight": "Дневной свет",
		"metric_vo2_max": "МПК (VO2 Max)",
		"metric_six_minute_walking_test_distance": "6-мин ходьба",
		"metric_readiness": "Готовность",
		"metric_oxygen_saturation": "Кислород крови",
		"metric_heart_rate_variability_sdnn": "ВСР",
		"metric_environmental_audio": "Шум окружения",
		"metric_headphone_audio": "Громкость наушников",
		"metric_walking_double_support": "Двойная опора",
		"metric_walking_asymmetry": "Асимметрия ходьбы",
		"metric_environmental_sound_reduction": "Шумоподавление",
		"metric_stair_ascent_speed": "Скорость подъёма",
		"metric_stair_descent_speed": "Скорость спуска",
		"metric_wrist_temperature": "Темп. запястья",
		"metric_walking_steadiness": "Устойчивость ходьбы",
		"metric_body_mass": "Масса тела",
		"metric_body_mass_index": "ИМТ",
		"metric_body_fat_percentage": "Жировая масса",
		"metric_lean_body_mass": "Сухая масса",
		"metric_height": "Рост",
		"metric_blood_pressure_systolic": "АД систолическое",
		"metric_blood_pressure_diastolic": "АД диастолическое",
		"metric_heart_rate_recovery": "Восст. ЧСС",
		"metric_distance_cycling": "Велодистанция",
		"metric_distance_swimming": "Дистанция плавания",
		"metric_swimming_stroke_count": "Гребки",
		"metric_mindful_minutes": "Медитация",
		"metric_alcoholic_beverages": "Алкоголь",
		"metric_six_min_walk_distance": "6-мин ходьба",
		"metric_dietary_energy": "Калории (питание)",
		"metric_dietary_protein": "Белки",
		"metric_dietary_carbs": "Углеводы",
		"metric_dietary_fat": "Жиры",
		"metric_dietary_fat_saturated": "Насыщ. жиры",
		"metric_dietary_fat_monounsaturated": "Мононенасыщ. жиры",
		"metric_dietary_fat_polyunsaturated": "Полиненасыщ. жиры",
		"metric_dietary_water": "Вода",
		"metric_dietary_sodium": "Натрий",
		"metric_dietary_sugar": "Сахар",
		"metric_dietary_fiber": "Клетчатка",
		"metric_dietary_caffeine": "Кофеин",
		"metric_dietary_calcium": "Кальций",
		"metric_dietary_iron": "Железо",
		"metric_dietary_cholesterol": "Холестерин",
		"metric_dietary_potassium": "Калий",
		"metric_dietary_magnesium": "Магний",
		"metric_dietary_phosphorus": "Фосфор",
		"metric_dietary_zinc": "Цинк",
		"metric_dietary_copper": "Медь",
		"metric_dietary_manganese": "Марганец",
		"metric_dietary_selenium": "Селен",
		"metric_dietary_iodine": "Йод",
		"metric_dietary_molybdenum": "Молибден",
		"metric_dietary_folate": "Фолат",
		"metric_dietary_biotin": "Биотин (B7)",
		"metric_dietary_vitamin_a": "Витамин A",
		"metric_dietary_vitamin_c": "Витамин C",
		"metric_dietary_vitamin_d": "Витамин D",
		"metric_dietary_vitamin_e": "Витамин E",
		"metric_dietary_vitamin_k": "Витамин K",
		"metric_dietary_vitamin_b6": "Витамин B6",
		"metric_dietary_vitamin_b12": "Витамин B12",
		"metric_dietary_niacin": "Ниацин (B3)",
		"metric_dietary_riboflavin": "Рибофлавин (B2)",
		"metric_dietary_thiamin": "Тиамин (B1)",
		"metric_dietary_pantothenic_acid": "Пантотен. к-та (B5)",
		"by_source": "Источники",
		"source_comparison": "Сравнение источников",
		"lbl_total": "Итого",
		"lbl_deep": "Глубокий",
		"lbl_core": "Основной",
		"how_it_works": "Как это работает",
		"health_sections": "Обзор здоровья",
		"at_a_glance": "Сегодня",

		// Dual-baseline trend chips on metric cards
		"trend_vs_7d":  "к 7д",
		"trend_vs_30d": "к 30д",

		// Energy Bank widget (Bevel-inspired prescriptive verdict)
		"energy_label":          "Энергетический банк",
		"energy_capacity_label": "Капасити на сегодня",
		"energy_current_label":  "Сейчас доступно",
		"energy_drain_label":    "Потрачено",
		"energy_verdict_push_hard":       "Можно жёстко",
		"energy_verdict_moderate":        "Умеренный день",
		"energy_verdict_active_recovery": "Только активное восстановление",
		"energy_verdict_rest":            "День отдыха",
		"energy_component_morning_capacity": "Утренняя капасити",
		"energy_component_activity_load":    "Нагрузка (сегодня vs 28-дн норма)",
		"energy_component_autonomic_stress": "Автономный стресс (RHR / HRV)",
		"energy_state_good_title":     "Заряжен",
		"energy_state_good_desc":      "Запас полный — сегодня можно бросить себе вызов.",
		"energy_state_medium_title":   "В балансе",
		"energy_state_medium_desc":    "Резерв приличный — тренируйся, но следи за объёмом.",
		"energy_state_low_title":      "Резерв на исходе",
		"energy_state_low_desc":       "Запас тонкий — держи лёгкий темп и фокус на восстановление.",
		"energy_state_critical_title": "Истощён",
		"energy_state_critical_desc":  "Бак почти пуст — отдых принесёт больше всего пользы.",
		"details": "Детали",

		"admin_title": "Настройки",
		"admin_cache_status": "Состояние кэша",
		"admin_refresh": "Обновить",
		"admin_actions": "Действия",
		"admin_raw": "Сырые данные",
		"admin_minute": "Минутный кэш",
		"admin_hourly": "Часовой кэш",
		"admin_daily": "Дневные оценки",
		"admin_metrics": "метрик",
		"admin_empty": "пусто",
		"admin_score_version": "Версия скоринга",
		"admin_last_sync": "Последний синх",
		"admin_incremental_title": "Обновить кэш",
		"admin_incremental_desc": "Дозаполнить пропущенные записи. Быстро, безопасно.",
		"admin_force_title": "Перестроить всё",
		"admin_force_desc": "Очистить и пересчитать все кэши. Используйте после изменения формулы.",
		"admin_run": "Запустить",
		"admin_force_run": "Перестроить",
		"admin_target_user": "Целевой пользователь",
		"admin_target_user_current": "— текущий пользователь —",
		"admin_notify_title": "Telegram-отчёты",
		"admin_notify_morning_title": "Утренний отчёт",
		"admin_notify_morning_desc": "Отправить тестовую сводку по сну сейчас.",
		"admin_notify_evening_title": "Вечерний отчёт",
		"admin_notify_evening_desc": "Отправить тестовую сводку за день сейчас.",
		"admin_notify_token": "Токен бота",
		"admin_notify_chat_id": "Chat ID",
		"admin_notify_lang": "Язык",
		"admin_notify_timezone": "Часовой пояс",
		"admin_notify_timezone_hint": "Нужен для ежедневных отчётов и расчёта исторического EnergyBank. Формат IANA (например, Europe/Belgrade, America/New_York).",
		"admin_energy_backfill_title": "Исторический EnergyBank",
		"admin_energy_backfill_desc": "Пересчитать снимки EnergyBank по импортированной истории Apple Health. Без этого персональная калибровка вердиктов работает на дефолтных порогах, а не на твоей реальной шкале.",
		"admin_energy_backfill_loading": "Загрузка…",
		"admin_energy_backfill_load_error": "Не удалось загрузить статус backfill.",
		"admin_energy_backfill_summary": "{complete} полных дней истории · {backfilled} снимков EnergyBank уже backfilled · диапазон: {earliest} → {to}",
		"admin_energy_backfill_run": "Посчитать исторический EnergyBank",
		"admin_energy_backfill_running": "Backfill уже выполняется.",
		"admin_energy_backfill_need_tz": "Сначала выставь часовой пояс выше.",
		"admin_energy_backfill_no_data": "Полных дней нет — импортируй данные Apple Health или дождись накопления через live ingest.",
		"admin_energy_backfill_confirm": "Это пересчитает EnergyBank за каждый исторический день. Продолжить?",
		"admin_energy_backfill_starting": "Запуск…",
		"admin_energy_backfill_progress": "День {done} из {total} · ok={ok}",
		"admin_energy_backfill_done": "Готово: {ok} записано · {skipped} пропущено (нет данных для lookback) · {errors} ошибок",
		"admin_notify_schedule_morning": "Час утреннего отчёта",
		"admin_notify_schedule_evening": "Час вечернего отчёта",
		"admin_notify_weekday": "Будни",
		"admin_notify_weekend": "Выходные",
		"admin_notify_save": "Сохранить",
		"admin_notify_saved": "Настройки сохранены",
		"admin_notify_send": "Отправить тест",
		"admin_notify_test_morning": "Тест утро",
		"admin_notify_test_evening": "Тест вечер",
		"admin_gaps_section_title": "Целостность данных",
		"admin_gaps_check":         "Проверить пробелы",
		"admin_quality_title":      "Проверка качества данных",
		"admin_quality_run":        "Запустить аудит",
		"admin_quality_fix":        "Пометить подозрительные + почистить мусор",
		"admin_quality_digest":     "Отправить дайджест сейчас",
		"admin_quality_clean":      "Всё чисто — аномалий не найдено.",
		"admin_quality_total":      "Всего строк",
		"admin_quality_bad":        "Вне нормы",
		"admin_quality_range":      "Диапазон",
		"admin_quality_metric":     "Метрика",
		"admin_quality_sample":     "Пример",
		"admin_quality_week":       "За 7 дней",
		"admin_quality_impossible": "Невозможные",
		"admin_quality_suspect":    "Подозрительные",
		"admin_quality_missed":     "Пропущенные ночи",
		"admin_quality_fixed":      "Помечено: невозможных %d, подозрительных %d.",
		"admin_ai_title":      "AI-брифинг утром",
		"admin_ai_key":        "Gemini API ключ",
		"admin_ai_model":      "Модель",
		"admin_ai_max_tokens": "Макс. токенов",
		"admin_ai_save":       "Сохранить",
		"admin_ai_saved":      "Настройки сохранены",
		"admin_energy_title":           "EnergyBank v2.2 — стресс-дрейн",
		"admin_energy_warning":         "Non-production значения до тех пор, пока §4.5 валидация не вернёт 'validated'. Effective β = 0, пока stress drain выключен — независимо от значения β ниже.",
		"admin_energy_stress_enabled":  "Stress drain включён",
		"admin_energy_beta":            "β (коэф. стресс-дрейна)",
		"admin_energy_z_threshold":     "z-score порог",
		"admin_energy_effective_beta":  "Effective β (live)",
		"admin_energy_save":            "Сохранить",
		"admin_energy_saved":           "Настройки сохранены",
		"admin_help_summary":           "Что это и когда менять?",
		"admin_energy_help_html": `<h4 style="margin:8px 0 4px">β — коэффициент stress-drain</h4>
<p>Множитель, переводящий <code>sustained_hr_load_z</code> в очки drain. Формула: <code>drain = α·active_kcal + <strong>β</strong>·load_z</code>.</p>
<p><strong>Default 0.8</strong> (placeholder из §6 Q3). Когда validation rubric ниже вернёт <code>validated</code> — подкручивать в диапазоне 0.4–1.2, в зависимости от того насколько резво хочется чтобы бар реагировал на длительную автономную нагрузку.</p>
<h4 style="margin:12px 0 4px">z-threshold — порог по часу</h4>
<p>В load integral идут только часы, где HR выше дневного baseline более чем на это число SD.</p>
<p><strong>Default 0.5.</strong> Поднять до 0.7–0.8 если сидячая работа и HR сидит на z≈0.5 систематически без реального стресса (mental activation ≠ автономная нагрузка → false positives).</p>
<h4 style="margin:12px 0 4px">Stress drain enabled — мастер-переключатель</h4>
<p>Когда ВЫКЛ (default), <code>sustained_hr_load_z</code> всё равно считается и пишется в <code>components</code> JSONB для audit, но в bank не идёт — β_effective = 0.</p>
<p><strong>Включать ТОЛЬКО когда validation rubric ниже вернула <code>validated</code>.</strong> Включишь раньше — drain будет реагировать на шум.</p>
<h4 style="margin:12px 0 4px">Workflow</h4>
<ol style="margin:4px 0 0 18px;padding:0">
  <li>Прогнать validation rubric (кнопка ниже).</li>
  <li>Verdict ≠ validated → β=0, ничего не трогать. Monthly Telegram nudge придёт сам когда verdict перейдёт.</li>
  <li>Verdict = validated → включить Stress drain enabled, β=0.8.</li>
  <li>Через ~2 недели — если drain слишком агрессивен, уменьшить β до 0.4–0.5. Перепрогонять rubric раз в месяц.</li>
</ol>`,
		"admin_stress_validation_help_html": `<h4 style="margin:8px 0 4px">Что считает rubric</h4>
<p>Pearson r на скользящем 30-дневном окне между <code>sustained_hr_load[d]</code> и тремя независимыми next-morning сигналами восстановления. Логика: если формула реально ловит стресс — высокий load сегодня предсказывает деградацию autonomic-маркеров утром.</p>
<h4 style="margin:12px 0 4px">Каналы</h4>
<dl style="margin:4px 0">
  <dt><strong>Channel 1 — утренний HRV (primary, анкер)</strong></dt>
  <dd>Корреляция load[d] с overnight HRV[d+1]. Ожидаемый знак: <strong>отрицательный</strong> (high load → low HRV).</dd>
  <dt style="margin-top:6px"><strong>Channel 2 — сдвиг утреннего RHR (secondary)</strong></dt>
  <dd>load[d] vs (overnight RHR[d+1] − 30d baseline). Ожидаемый знак: <strong>положительный</strong>. Cross-check.</dd>
  <dt style="margin-top:6px"><strong>Channel 3 — архитектура сна (tertiary)</strong></dt>
  <dd>Голосование 3 sub-correlations: sleep_awake (+), onset latency (+), deep% (−). Sub-signal голосует если |r|≥0.10 в ожидаемом направлении; channel "agrees" при ≥2 голосах.</dd>
</dl>
<h4 style="margin:12px 0 4px">Вердикты</h4>
<ul style="margin:4px 0 0 18px;padding:0">
  <li><strong style="color:var(--good)">validated</strong> — r ≤ −0.30 на channel 1 И хотя бы один cross-channel согласен. Можно включать <code>Stress drain enabled</code>.</li>
  <li><strong style="color:var(--fair)">weak</strong> — −0.30 &lt; r &lt; −0.10. β=0. Recheck через месяц.</li>
  <li><strong style="color:var(--low)">inconclusive</strong> — |r| &lt; 0.10 на channel 1, или меньше 15 HRV образцов (sparsity fallback). Нужно больше ночного HRV (Breathe sessions на Apple Watch помогают).</li>
  <li><strong style="color:var(--low)">wrong_direction</strong> — r &gt; 0. Формула не ловит физиологию этого пользователя — manual review.</li>
</ul>
<h4 style="margin:12px 0 4px">Когда запускать</h4>
<ul style="margin:4px 0 0 18px;padding:0">
  <li>После major data ingest (импорт Apple Health, восстановление sleep).</li>
  <li>Раз в месяц как routine. Monthly Telegram nudge придёт сам когда verdict перейдёт в <strong>validated</strong>.</li>
  <li>Перед включением <code>Stress drain enabled</code> выше.</li>
</ul>`,
		"admin_stress_validation_title":        "Валидация stress-формулы (§4.5)",
		"admin_stress_validation_desc":         "Pearson r на скользящем 30-дневном окне: утренний HRV (основной), сдвиг утреннего RHR, архитектура сна. Только чтение — НЕ переключает stress_drain_enabled автоматически. Решение оператора после ознакомления с вердиктом.",
		"admin_stress_validation_run":          "Запустить",
		"admin_stress_validation_loading":      "Считаю rubric на 30-дневном окне…",
		"admin_stress_validation_sparse":       "мало данных",
		"admin_stress_validation_no_data":      "нет данных",
		"admin_stress_validation_flags_label":  "флаги:",
		"admin_stress_validation_ch1":          "Канал 1 (утренний HRV)",
		"admin_stress_validation_ch2":          "Канал 2 (сдвиг утреннего RHR)",
		"admin_stress_validation_ch3":          "Канал 3 (архитектура сна)",
		"admin_stress_validation_votes":        "голоса",
		"admin_stress_validation_window_fmt":   "окно {window} дней, {days} дней с данными",
		"admin_import_title": "Импорт Apple Health",
		"admin_import_desc": "Загрузите export.zip для импорта исторических данных. Дубликаты пропускаются.",
		"admin_import_choose": "Выбрать файл…",
		"admin_import_batch": "записей/батч",
		"admin_import_pause": "мс пауза",
		"admin_import_start": "Начать импорт",
		// v2.2 hero-row stress-flag chips.
		"stress_flags_aria":    "Сигналы стресса",
		"stress_detail_what":   "Что это",
		"stress_detail_cause":  "Что вызвало",
		"stress_detail_risk":   "Чем важно",
		"stress_detail_action": "Что делать",
		"stress_flag_acute_stress_label": "Скачок пульса",
		"stress_flag_acute_stress_detail_html": `<h5>Что это</h5>
<p>Один час за день пульс резко подскочил выше вашей нормы.</p>
<h5>Что вызвало</h5>
<p>Кофе, неожиданный звонок, нервы перед встречей, испуг, кратковременный конфликт — любая короткая активация симпатической нервной системы.</p>
<h5>Чем важно</h5>
<p>Само по себе — ничем. Изолированный пик за день не нагружает систему, это нормальная реакция организма.</p>
<h5>Что делать</h5>
<p>Ничего. Флаг показан для диагностики — чтобы было видно, что это разовое событие, а не хронический стресс.</p>`,
		"stress_flag_sustained_load_label": "Долгий рост пульса",
		"stress_flag_sustained_load_detail_html": `<h5>Что это</h5>
<p>Пульс держался выше вашей нормы как минимум 4 часа подряд сегодня.</p>
<h5>Что вызвало</h5>
<p>Длинный стрессовый день (deadline, переговоры), тяжёлая тренировка с долгим восстановлением, начало болезни, обезвоживание, недосып.</p>
<h5>Чем важно</h5>
<p>Реальная нагрузка на автономную нервную систему. Несколько таких дней подряд — расход энергии накапливается, восстановление замедляется. EnergyBank уже учёл это: бар сегодня сел больше обычного.</p>
<h5>Что делать</h5>
<p>Вечером — ранний отбой, без алкоголя и кофеина после обеда. Завтра — лёгкая активность, не повторять нагрузку.</p>`,
		"stress_flag_illness_signature_label": "Похоже на болезнь",
		"stress_flag_illness_signature_detail_html": `<h5>Что это</h5>
<p>Температура тела, частота дыхания и HRV — все три отклонились от вашей нормы в "болезненную" сторону одновременно.</p>
<h5>Что вызвало</h5>
<p>Чаще всего — вирусная инфекция в начальной стадии. Реже — серьёзная перетренированность с подсаженным иммунитетом.</p>
<h5>Чем важно</h5>
<p>Жёсткая тренировка на этом фоне обычно затягивает простуду на лишние 3-5 дней. Иммунка работает на отбой инфекции — занимать её спортом сверху значит давать организму двойную нагрузку.</p>
<h5>Что делать</h5>
<p>Сегодня — спать. Тренировку отменить или сделать максимально лёгкой (медленная прогулка). Вода, тёплая еда. Завтра проверить состояние: если флаг ушёл, можно постепенно возвращаться к плану.</p>`,
		"stress_flag_recovery_debt_label": "Не восстановились",
		"stress_flag_recovery_debt_detail_html": `<h5>Что это</h5>
<p>Ночью HRV упал ниже вашей нормы, а пульс покоя — поднялся выше. Сон не сбросил вчерашнюю нагрузку.</p>
<h5>Что вызвало</h5>
<p>Вчерашняя интенсивная тренировка или рабочий день, поздний приём пищи, алкоголь, поздний отбой, эмоциональный стресс.</p>
<h5>Чем важно</h5>
<p>Сегодня вы уже не в оптимальной форме. Если игнорировать и пойти на жёсткую тренировку — растёт риск перетренироваться, и шансы получить мелкую травму выше обычного.</p>
<h5>Что делать</h5>
<p>Лёгкий день: прогулка, мобилка, йога. Хорошо поесть, рано лечь. Завтра флаг должен уйти — если нет, два лёгких дня подряд лучше, чем заработать настоящий перетрен.</p>`,
		"stress_flag_parasympathetic_rebound_label": "Восстановление",
		"stress_flag_parasympathetic_rebound_detail_html": `<h5>Что это</h5>
<p>Пульс был выше нормы, но HRV тоже был выше нормы. Парасимпатическая нервная система активна — это режим восстановления, не стресс.</p>
<h5>Что вызвало</h5>
<p>Обычно — после тяжёлой тренировки или интенсивного дня. Тело потратило ресурс и теперь активно его восстанавливает.</p>
<h5>Чем важно</h5>
<p>Ничем плохим — это здоровая реакция. <strong>Не путать с острым стрессом</strong>: внешне пульс высокий, но физиология совсем другая.</p>
<h5>Что делать</h5>
<p>Дать организму время: лёгкий день, нормальный сон. Можно тренироваться, но не на пределе.</p>`,
		"stress_flag_stale_stress_label": "Мало данных",
		"stress_flag_stale_stress_detail_html": `<h5>Что это</h5>
<p>За день собрано меньше 8 часов записей пульса в часы, когда вы не спали. Стресс-формула на этот день не считается.</p>
<h5>Что делать</h5>
<p>Стабильнее носить часы. Проверить, что синхронизация с iPhone работает. Для цифр сегодня ничего делать не нужно — флаг уйдёт автоматически, как только данных станет больше 8 часов.</p>`,
		"stress_flag_calibration_warmup_label": "Учим норму",
		"stress_flag_calibration_warmup_detail_html": `<h5>Что это</h5>
<p>Личная норма (HRV, пульс, дыхание) ещё учится — нужно около недели непрерывных данных. Пороги стресс-флагов пока консервативные.</p>
<h5>Что делать</h5>
<p>Просто носить часы. Через несколько дней флаг уйдёт автоматически.</p>`,
		"admin_import_uploading": "Загрузка…",
		"admin_import_running": "Импорт выполняется…",
	},
	"sr": {
		"app_title": "Zdravlje",
		"explore": "Pretraži",
		"loading": "Učitavanje podataka",
		"readiness": "Spremnost",
		"recovery": "Oporavak",
		"readiness_today_label": "Danas",
		"readiness_trend_label": "7-dnevni trend",
		"back": "Nazad",
		"compare": "Uporedi",
		"all_metrics":  "Sve metrike",
		"nav_settings": "Podešavanja",
		"your_trends": "Vaši trendovi",
		"search_placeholder": "Pretraži metrike...",
		"esc_hint": "ESC — zatvori",
		"no_metrics_found": "Nema metrika",
		"no_data": "Nema podataka",
		"no_data_range": "Nema podataka za ovaj period",
		"no_sleep_data": "Nema podataka o snu za ovaj period",
		"start_syncing": "Počnite sinhronizaciju podataka o zdravlju.",
		"data_from": "Podaci od ",
		"days_ago": "d ranije",
		"this_week": "Ova nedelja",
		"activity_vs_recovery": "Aktivnost i oporavak",
		"activity_recovery_subtitle": "Kako fizičko opterećenje utiče na HRV",
		"activity_load": "Opterećenje",
		"sleep_section": "San",
		"sleep_subtitle": "Prosek za 7 noći",
		"deep_sleep": "Duboki san",
		"rem_sleep": "REM san",
		"awake_time": "Vreme budnosti",
		"efficiency": "Efikasnost",
		"bucket": "Period",
		"agg": "Agr.",
		"auto": "Auto",
		"minute": "Minut",
		"hour": "Sat",
		"day": "Dan",
		"preset_all": "Sve",
		"avg": "Pros.",
		"sum": "Zbir",
		"max": "Maks",
		"min": "Min",
		"previous_period": "Prethodni period",
		"vs_yesterday": "vs juče",
		"stable": "Stabilno",
		"load_pct": "Opterećenje %",
		"hrv_ms": "HRV ms",
		"nights": "Noći",
		"avg_total": "Pros. ukupno",
		"avg_deep": "Pros. duboki",
		"avg_rem": "Pros. REM",
		"points": "Tačke",
		"stale_prefix": "Podaci od ",
		"stale_suffix": "d ranije",
		"status_good": "Odlično",
		"status_fair": "Treba pažnje",
		"status_low": "Čuvajte se",
		"cat_heart": "Srce i vitalni znaci",
		"cat_activity": "Aktivnost",
		"cat_fitness": "Fitnes",
		"cat_sleep": "San",
		"cat_body": "Telo",
		"cat_env": "Okruženje",
		"cat_nutrition": "Ishrana",
		"cat_other": "Ostalo",
		"phase_deep": "Duboki",
		"phase_rem": "REM",
		"phase_core": "Osnovni",
		"phase_awake": "Budan",
		"trend_steps": "Koraci",
		"trend_heart_rate": "Puls",
		"trend_sleep": "San",
		"trend_hrv": "HRV",
		"trend_readiness": "Spremnost",
		"ai_insight_title": "Detaljan AI izveštaj",
		"teps": "Koraci",
		"leep": "San",
		"ate": "Respiratorni ritam",
		"metric_sleep_total": "Ukupan san",
		"metric_night_sleep_total": "Noćni san",
		"metric_nap_total": "Dremke",
		"metric_sleep_deep": "Duboki san",
		"metric_sleep_rem": "REM san",
		"metric_sleep_core": "Osnovni san",
		"metric_sleep_unspecified": "San (bez faza)",
		"metric_sleep_awake": "Vreme budnosti",
		"chart_sleep_unspecified_hint": "izvor nije pružio podelu na faze",
		"metric_heart_rate": "Puls",
		"metric_resting_heart_rate": "Puls u miru",
		"metric_walking_heart_rate_average": "Puls pri hodu",
		"metric_heart_rate_variability": "HRV",
		"metric_blood_oxygen_saturation": "Kiseonik u krvi",
		"metric_respiratory_rate": "Respiratorni ritam",
		"metric_step_count": "Koraci",
		"metric_walking_running_distance": "Distanca",
		"metric_active_energy": "Akt. kalorije",
		"metric_basal_energy_burned": "Kalorije u miru",
		"metric_apple_exercise_time": "Vežbanje",
		"metric_apple_stand_time": "Vreme stajanja",
		"metric_apple_stand_hour": "Sati stajanja",
		"metric_physical_effort": "Fizički napor",
		"metric_flights_climbed": "Penjanje uz stepenice",
		"metric_stair_speed_up": "Brzina na stepenicama",
		"metric_walking_speed": "Brzina hoda",
		"metric_walking_step_length": "Dužina koraka",
		"metric_walking_double_support_percentage": "Dvostrana podrška",
		"metric_walking_asymmetry_percentage": "Asimetrija hoda",
		"metric_apple_sleeping_wrist_temperature": "Temp. zgloba",
		"metric_breathing_disturbances": "Poremećaji disanja",
		"metric_environmental_audio_exposure": "Izloženost buci",
		"metric_headphone_audio_exposure": "Glasnoća slušalica",
		"metric_time_in_daylight": "Dnevna svetlost",
		"metric_vo2_max": "VO2 Maks",
		"metric_six_minute_walking_test_distance": "6-min hod",
		"metric_readiness": "Spremnost",
		"metric_oxygen_saturation": "Kiseonik u krvi",
		"metric_heart_rate_variability_sdnn": "HRV",
		"metric_environmental_audio": "Buka okoline",
		"metric_headphone_audio": "Glasnoća slušalica",
		"metric_walking_double_support": "Dvostrana podrška",
		"metric_walking_asymmetry": "Asimetrija hoda",
		"metric_environmental_sound_reduction": "Redukcija buke",
		"metric_stair_ascent_speed": "Brzina penjanja",
		"metric_stair_descent_speed": "Brzina silaska",
		"metric_wrist_temperature": "Temp. zgloba",
		"metric_walking_steadiness": "Stabilnost hoda",
		"metric_body_mass": "Telesna masa",
		"metric_body_mass_index": "BMI",
		"metric_body_fat_percentage": "Procenat masti",
		"metric_lean_body_mass": "Mišićna masa",
		"metric_height": "Visina",
		"metric_blood_pressure_systolic": "Sistolni pritisak",
		"metric_blood_pressure_diastolic": "Dijastolni pritisak",
		"metric_heart_rate_recovery": "Oporavak pulsa",
		"metric_distance_cycling": "Distanca kolesarenja",
		"metric_distance_swimming": "Distanca plivanja",
		"metric_swimming_stroke_count": "Zaveslaji",
		"metric_mindful_minutes": "Meditacija",
		"metric_alcoholic_beverages": "Alkohol",
		"metric_six_min_walk_distance": "6-min hod",
		"metric_dietary_energy": "Kalorije (ishrana)",
		"metric_dietary_protein": "Proteini",
		"metric_dietary_carbs": "Ugljeni hidrati",
		"metric_dietary_fat": "Ukupne masti",
		"metric_dietary_fat_saturated": "Zasićene masti",
		"metric_dietary_fat_monounsaturated": "Mononezasićene masti",
		"metric_dietary_fat_polyunsaturated": "Polinezasićene masti",
		"metric_dietary_water": "Voda",
		"metric_dietary_sodium": "Natrijum",
		"metric_dietary_sugar": "Šećer",
		"metric_dietary_fiber": "Vlakna",
		"metric_dietary_caffeine": "Kofein",
		"metric_dietary_calcium": "Kalcijum",
		"metric_dietary_iron": "Gvožđe",
		"metric_dietary_cholesterol": "Holesterol",
		"metric_dietary_potassium": "Kalijum",
		"metric_dietary_magnesium": "Magnezijum",
		"metric_dietary_phosphorus": "Fosfor",
		"metric_dietary_zinc": "Cink",
		"metric_dietary_copper": "Bakar",
		"metric_dietary_manganese": "Mangan",
		"metric_dietary_selenium": "Selen",
		"metric_dietary_iodine": "Jod",
		"metric_dietary_molybdenum": "Molibden",
		"metric_dietary_folate": "Folat",
		"metric_dietary_biotin": "Biotin (B7)",
		"metric_dietary_vitamin_a": "Vitamin A",
		"metric_dietary_vitamin_c": "Vitamin C",
		"metric_dietary_vitamin_d": "Vitamin D",
		"metric_dietary_vitamin_e": "Vitamin E",
		"metric_dietary_vitamin_k": "Vitamin K",
		"metric_dietary_vitamin_b6": "Vitamin B6",
		"metric_dietary_vitamin_b12": "Vitamin B12",
		"metric_dietary_niacin": "Niacin (B3)",
		"metric_dietary_riboflavin": "Riboflavin (B2)",
		"metric_dietary_thiamin": "Tijamin (B1)",
		"metric_dietary_pantothenic_acid": "Pantotenska kis. (B5)",
		"by_source": "Izvori",
		"source_comparison": "Poređenje izvora",
		"lbl_total": "Ukupno",
		"lbl_deep": "Duboki",
		"lbl_core": "Osnovni",
		"how_it_works": "Kako to radi",
		"health_sections": "Pregled zdravlja",
		"at_a_glance": "Danas",

		// Dual-baseline trend chips on metric cards
		"trend_vs_7d":  "vs 7d",
		"trend_vs_30d": "vs 30d",

		// Energy Bank widget (Bevel-inspired prescriptive verdict)
		"energy_label":          "Energetski bank",
		"energy_capacity_label": "Današnji kapacitet",
		"energy_current_label":  "Dostupno sada",
		"energy_drain_label":    "Potrošeno",
		"energy_verdict_push_hard":       "Slobodno tvrdo",
		"energy_verdict_moderate":        "Umeren dan",
		"energy_verdict_active_recovery": "Samo aktivni oporavak",
		"energy_verdict_rest":            "Dan odmora",
		"energy_component_morning_capacity": "Jutarnji kapacitet",
		"energy_component_activity_load":    "Opterećenje (danas vs 28d hronični)",
		"energy_component_autonomic_stress": "Autonomni stres (RHR / HRV)",
		"energy_state_good_title":     "Napunjen",
		"energy_state_good_desc":      "Rezerve su pune — danas slobodno guraj jače.",
		"energy_state_medium_title":   "U balansu",
		"energy_state_medium_desc":    "Pristojne rezerve — treniraj sa namerom, ali pazi na obim.",
		"energy_state_low_title":      "Rezerve niske",
		"energy_state_low_desc":       "Rezerva je tanka — lakši napor i fokus na oporavak.",
		"energy_state_critical_title": "Ispražnjen",
		"energy_state_critical_desc":  "Rezervoar gotovo prazan — odmor je danas najbolji izbor.",
		"details": "Detalji",
		"admin_notify_title": "Telegram izveštaji",
		"admin_notify_morning_title": "Jutarnji izveštaj",
		"admin_notify_morning_desc": "Pošalji test san sada.",
		"admin_notify_evening_title": "Večernji izveštaj",
		"admin_notify_evening_desc": "Pošalji test pregled dana sada.",
		"admin_notify_token": "Token bota",
		"admin_notify_chat_id": "Chat ID",
		"admin_notify_lang": "Jezik",
		"admin_notify_timezone": "Vremenska zona",
		"admin_notify_timezone_hint": "Potrebna za dnevne izveštaje i izračunavanje istorijskog EnergyBank-a. IANA format (npr. Europe/Belgrade).",
		"admin_energy_backfill_title": "Istorijski EnergyBank",
		"admin_energy_backfill_desc": "Izračunaj retrospektivne EnergyBank snapshot-ove iz uvezene Apple Health istorije. Bez ovoga personalna kalibracija radi na default pragovima umesto na tvojoj realnoj distribuciji.",
		"admin_energy_backfill_loading": "Učitavanje…",
		"admin_energy_backfill_load_error": "Greška pri učitavanju statusa.",
		"admin_energy_backfill_summary": "{complete} kompletnih dana · {backfilled} snapshot-ova već backfilled · opseg: {earliest} → {to}",
		"admin_energy_backfill_run": "Izračunaj istorijski EnergyBank",
		"admin_energy_backfill_running": "Backfill je već u toku.",
		"admin_energy_backfill_need_tz": "Prvo podesi vremensku zonu iznad.",
		"admin_energy_backfill_no_data": "Nema kompletnih dana — uvezi Apple Health ili sačekaj live ingest.",
		"admin_energy_backfill_confirm": "Ovo će preračunati EnergyBank za svaki istorijski dan. Nastaviti?",
		"admin_energy_backfill_starting": "Pokretanje…",
		"admin_energy_backfill_progress": "Dan {done}/{total} · ok={ok}",
		"admin_energy_backfill_done": "Gotovo: {ok} upisano · {skipped} preskočeno · {errors} grešaka",
		"admin_notify_schedule_morning": "Sat jutarnjeg izveštaja",
		"admin_notify_schedule_evening": "Sat večernjeg izveštaja",
		"admin_notify_weekday": "Radni dani",
		"admin_notify_weekend": "Vikend",
		"admin_notify_save": "Sačuvaj",
		"admin_notify_saved": "Postavke sačuvane",
		"admin_notify_send": "Pošalji test",
		"admin_notify_test_morning": "Test jutro",
		"admin_notify_test_evening": "Test veče",
		"admin_target_user":         "Ciljni korisnik",
		"admin_target_user_current": "— trenutni korisnik —",
		"admin_gaps_section_title":  "Integritet podataka",
		"admin_gaps_check":          "Proveri praznine",
		"admin_quality_title":       "Provera kvaliteta podataka",
		"admin_quality_run":         "Pokreni audit",
		"admin_quality_fix":         "Označi sumnjive + očisti nemoguće",
		"admin_quality_digest":      "Pošalji digest sada",
		"admin_quality_clean":       "Sve čisto — nema anomalija.",
		"admin_quality_total":       "Ukupno redova",
		"admin_quality_bad":         "Van opsega",
		"admin_quality_range":       "Opseg",
		"admin_quality_metric":      "Metrika",
		"admin_quality_sample":      "Primer",
		"admin_quality_week":        "Za 7 dana",
		"admin_quality_impossible":  "Nemoguće",
		"admin_quality_suspect":     "Sumnjivo",
		"admin_quality_missed":      "Propuštene noći",
		"admin_quality_fixed":       "Označeno: nemoguće %d, sumnjive %d.",
		"admin_ai_title":      "AI jutarnji izveštaj",
		"admin_ai_key":        "Gemini API ključ",
		"admin_ai_model":      "Model",
		"admin_ai_max_tokens": "Maks. tokena",
		"admin_ai_save":       "Sačuvaj",
		"admin_ai_saved":      "Postavke sačuvane",
		"admin_energy_title":           "EnergyBank v2.2 — stres drain",
		"admin_energy_warning":         "Non-production placeholder vrednosti dok §4.5 validacija ne vrati 'validated'. Effective β je 0 dok je stres drain isključen, bez obzira na β ispod.",
		"admin_energy_stress_enabled":  "Stres drain uključen",
		"admin_energy_beta":            "β (koef. stres draina)",
		"admin_energy_z_threshold":     "z-score prag",
		"admin_energy_effective_beta":  "Effective β (uživo)",
		"admin_energy_save":            "Sačuvaj",
		"admin_energy_saved":           "Postavke sačuvane",
		"admin_help_summary":           "Šta je ovo i kada menjati?",
		"admin_energy_help_html": `<h4 style="margin:8px 0 4px">β — koeficijent stres-drain</h4>
<p>Množilac koji pretvara <code>sustained_hr_load_z</code> u drain poene. Formula: <code>drain = α·active_kcal + <strong>β</strong>·load_z</code>.</p>
<p><strong>Default 0.8</strong> (placeholder iz §6 Q3). Kada validation rubric ispod vrati <code>validated</code> — podesi u opsegu 0.4–1.2, u zavisnosti od toga koliko agresivno želiš da bar reaguje na trajno autonomno opterećenje.</p>
<h4 style="margin:12px 0 4px">z-threshold — prag po satu</h4>
<p>U load integral idu samo sati gde je HR viši od dnevnog baseline-a za više od ovog broja SD.</p>
<p><strong>Default 0.5.</strong> Podigni na 0.7–0.8 ako imaš sedentarni stil i HR sistematski stoji na z≈0.5 bez stvarnog stresa (mental activation ≠ autonomno opterećenje → false positives).</p>
<h4 style="margin:12px 0 4px">Stress drain enabled — master prekidač</h4>
<p>Kada je ISKLJUČEN (default), <code>sustained_hr_load_z</code> i dalje računa i piše se u <code>components</code> JSONB za audit, ali ne ulazi u bank — β_effective = 0.</p>
<p><strong>Uključi SAMO kada validation rubric ispod vrati <code>validated</code>.</strong> Uključiš ranije — drain će reagovati na šum.</p>
<h4 style="margin:12px 0 4px">Workflow</h4>
<ol style="margin:4px 0 0 18px;padding:0">
  <li>Pokreni validation rubric (dugme ispod).</li>
  <li>Verdict ≠ validated → β=0, ne diraj ništa. Monthly Telegram nudge će stići sam kada verdict pređe.</li>
  <li>Verdict = validated → uključi Stress drain enabled, β=0.8.</li>
  <li>Nakon ~2 nedelje — ako je drain previše agresivan, smanji β na 0.4–0.5. Pokreni rubric mesečno.</li>
</ol>`,
		"admin_stress_validation_help_html": `<h4 style="margin:8px 0 4px">Šta meri rubric</h4>
<p>Pearson r na rotirajućem 30-dnevnom prozoru između <code>sustained_hr_load[d]</code> i tri nezavisna next-morning signala oporavka. Logika: ako formula stvarno hvata stres — visok load danas predviđa degradaciju autonomnih markera ujutru.</p>
<h4 style="margin:12px 0 4px">Kanali</h4>
<dl style="margin:4px 0">
  <dt><strong>Kanal 1 — jutarnji HRV (primary, anchor)</strong></dt>
  <dd>Korelacija load[d] sa overnight HRV[d+1]. Očekivani znak: <strong>negativan</strong> (high load → low HRV).</dd>
  <dt style="margin-top:6px"><strong>Kanal 2 — pomeraj jutarnjeg RHR (secondary)</strong></dt>
  <dd>load[d] vs (overnight RHR[d+1] − 30d baseline). Očekivani znak: <strong>pozitivan</strong>. Cross-check.</dd>
  <dt style="margin-top:6px"><strong>Kanal 3 — arhitektura sna (tertiary)</strong></dt>
  <dd>Glasanje 3 sub-korelacije: sleep_awake (+), onset latency (+), deep% (−). Sub-signal glasa ako |r|≥0.10 u očekivanom smeru; kanal "se slaže" sa ≥2 glasa.</dd>
</dl>
<h4 style="margin:12px 0 4px">Verdikti</h4>
<ul style="margin:4px 0 0 18px;padding:0">
  <li><strong style="color:var(--good)">validated</strong> — r ≤ −0.30 na kanalu 1 I bar jedan cross-channel se slaže. Možeš uključiti <code>Stress drain enabled</code>.</li>
  <li><strong style="color:var(--fair)">weak</strong> — −0.30 &lt; r &lt; −0.10. β=0. Recheck za mesec.</li>
  <li><strong style="color:var(--low)">inconclusive</strong> — |r| &lt; 0.10 na kanalu 1, ili manje od 15 HRV uzoraka (sparsity fallback). Treba više noćnog HRV-a (Breathe sesije na Apple Watch-u pomažu).</li>
  <li><strong style="color:var(--low)">wrong_direction</strong> — r &gt; 0. Formula ne hvata fiziologiju ovog korisnika — manual review.</li>
</ul>
<h4 style="margin:12px 0 4px">Kada pokrenuti</h4>
<ul style="margin:4px 0 0 18px;padding:0">
  <li>Posle major data ingest-a (Apple Health uvoz, obnova sna).</li>
  <li>Jednom mesečno kao routine. Monthly Telegram nudge dolazi sam kada verdict pređe u <strong>validated</strong>.</li>
  <li>Pre nego što uključiš <code>Stress drain enabled</code> iznad.</li>
</ul>`,
		"admin_stress_validation_title":        "Validacija stres-formule (§4.5)",
		"admin_stress_validation_desc":         "Pearson r na rotirajućem 30-dnevnom prozoru: jutarnji HRV (osnovni), pomeraj jutarnjeg RHR, arhitektura sna. Samo čitanje — NE prebacuje stress_drain_enabled automatski. Operator odlučuje nakon pregleda verdikta.",
		"admin_stress_validation_run":          "Pokreni",
		"admin_stress_validation_loading":      "Računam rubric na 30-dnevnom prozoru…",
		"admin_stress_validation_sparse":       "malo podataka",
		"admin_stress_validation_no_data":      "nema podataka",
		"admin_stress_validation_flags_label":  "oznake:",
		"admin_stress_validation_ch1":          "Kanal 1 (jutarnji HRV)",
		"admin_stress_validation_ch2":          "Kanal 2 (pomeraj jutarnjeg RHR)",
		"admin_stress_validation_ch3":          "Kanal 3 (arhitektura sna)",
		"admin_stress_validation_votes":        "glasovi",
		"admin_stress_validation_window_fmt":   "prozor {window} dana, {days} dana sa podacima",
		"admin_import_title": "Uvoz Apple Health",
		"admin_import_desc": "Otpremite export.zip za uvoz istorijskih podataka. Duplikati se automatski preskaÄu.",
		"admin_import_choose": "Izaberi fajl…",
		"admin_import_batch": "zapisa/batch",
		"admin_import_pause": "ms pauza",
		"admin_import_start": "Pokreni uvoz",
		// v2.2 hero-row stress-flag chips.
		"stress_flags_aria":    "Signali stresa",
		"stress_detail_what":   "Šta je",
		"stress_detail_cause":  "Šta je izazvalo",
		"stress_detail_risk":   "Zašto je važno",
		"stress_detail_action": "Šta raditi",
		"stress_flag_acute_stress_label": "Skok pulsa",
		"stress_flag_acute_stress_detail_html": `<h5>Šta je</h5>
<p>Vaš puls je naglo skočio iznad vaše norme jedan sat danas.</p>
<h5>Šta je izazvalo</h5>
<p>Kafa, neočekivan poziv, nervi pred sastanak, iznenađenje, kratki konflikt — bilo koja kratka aktivacija simpatičkog nervnog sistema.</p>
<h5>Zašto je važno</h5>
<p>Samo po sebi — ničim. Izolovani skok ne opterećuje sistem, to je normalna reakcija organizma.</p>
<h5>Šta raditi</h5>
<p>Ništa. Oznaka služi za dijagnostiku — da se vidi da je bio jednokratan događaj, ne hronični stres.</p>`,
		"stress_flag_sustained_load_label": "Dug rast pulsa",
		"stress_flag_sustained_load_detail_html": `<h5>Šta je</h5>
<p>Vaš puls je stajao iznad vaše norme bar 4 sata zaredom danas.</p>
<h5>Šta je izazvalo</h5>
<p>Dug stresan dan (deadline, pregovori), težak trening sa sporim oporavkom, početak bolesti, dehidracija, nedovoljno sna.</p>
<h5>Zašto je važno</h5>
<p>Stvarno opterećenje za autonomni nervni sistem. Više takvih dana zaredom — dug energije se gomila, oporavak usporava. EnergyBank je već uračunao ovo: bar se danas više potrošio nego obično.</p>
<h5>Šta raditi</h5>
<p>Veče: rano u krevet, bez alkohola i kofeina posle ručka. Sutra: laka aktivnost, ne ponavljati opterećenje.</p>`,
		"stress_flag_illness_signature_label": "Možda bolest",
		"stress_flag_illness_signature_detail_html": `<h5>Šta je</h5>
<p>Telesna temperatura, frekvencija disanja i HRV — sve tri su se pomerile od vaše norme u "bolesnom" smeru istovremeno.</p>
<h5>Šta je izazvalo</h5>
<p>Najčešće virusna infekcija u ranoj fazi. Ređe — ozbiljan pretrening sa već oslabljenim imunitetom.</p>
<h5>Zašto je važno</h5>
<p>Težak trening na ovoj pozadini obično odugovlači prehladu za dodatnih 3-5 dana. Imunitet radi na odbijanju infekcije; opterećivati ga sportom = dvostruko opterećenje za organizam.</p>
<h5>Šta raditi</h5>
<p>Danas: spavanje. Trening otkazati ili maksimalno olakšati (spora šetnja). Voda, topla hrana. Sutra proveriti stanje: ako je oznaka nestala, postepeno se vraćati u plan.</p>`,
		"stress_flag_recovery_debt_label": "Niste se oporavili",
		"stress_flag_recovery_debt_detail_html": `<h5>Šta je</h5>
<p>Tokom noći HRV je pao ispod vaše norme, a puls u mirovanju je porastao. San nije skinuo jučerašnje opterećenje.</p>
<h5>Šta je izazvalo</h5>
<p>Jučerašnji intenzivan trening ili radni dan, kasan obrok, alkohol, kasno spavanje, emocionalni stres.</p>
<h5>Zašto je važno</h5>
<p>Danas niste u optimalnoj formi. Ako ignorišete i ipak idete na težak trening — raste rizik od pretreniranosti i šanse za sitnu povredu.</p>
<h5>Šta raditi</h5>
<p>Lak dan: šetnja, mobilnost, joga. Dobro pojesti, rano u krevet. Sutra oznaka treba da nestane — ako ne, dva laka dana zaredom su bolja od ozbiljnog pretreninga.</p>`,
		"stress_flag_parasympathetic_rebound_label": "Oporavak",
		"stress_flag_parasympathetic_rebound_detail_html": `<h5>Šta je</h5>
<p>Puls je bio povišen, ali je HRV takođe bio iznad norme. Parasimpatički nervni sistem je aktivan — to je režim oporavka, ne stres.</p>
<h5>Šta je izazvalo</h5>
<p>Obično posle teškog treninga ili intenzivnog dana. Telo je potrošilo resurs i sad ga aktivno obnavlja.</p>
<h5>Zašto je važno</h5>
<p>Ničim lošim — to je zdrava reakcija. <strong>Ne brkati sa akutnim stresom</strong>: spolja puls izgleda visok, ali je fiziologija sasvim drugačija.</p>
<h5>Šta raditi</h5>
<p>Dati telu vreme: lak dan, normalan san. Trening je u redu, samo ne do krajnjih granica.</p>`,
		"stress_flag_stale_stress_label": "Malo podataka",
		"stress_flag_stale_stress_detail_html": `<h5>Šta je</h5>
<p>Manje od 8 sati podataka o pulsu prikupljeno tokom budnih sati danas. Formula stresa se ne računa za ovaj dan.</p>
<h5>Šta raditi</h5>
<p>Stabilnije nositi sat. Proveriti da li sinhronizacija sa iPhone-om radi. Za današnje brojke akcija nije potrebna — oznaka nestaje automatski čim podataka bude više od 8 sati.</p>`,
		"stress_flag_calibration_warmup_label": "Kalibracija",
		"stress_flag_calibration_warmup_detail_html": `<h5>Šta je</h5>
<p>Lična norma (HRV, puls, disanje) se još uči — treba oko nedelju dana neprekidnih podataka. Pragovi oznaka stresa za sada ostaju konzervativni.</p>
<h5>Šta raditi</h5>
<p>Samo nosite sat. Nakon nekoliko dana oznaka nestaje sama.</p>`,
		"admin_import_uploading": "Otpremanje…",
		"admin_import_running": "Uvoz u toku…",
	},
}

// T returns the translation for key in the given language, falling back to English.
func T(lang, key string) string {
	if m, ok := translations[lang]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	if m, ok := translations["en"]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	return key
}

// MetricName returns a human-friendly metric name in the given language.
func MetricName(lang, key string) string {
	v := T(lang, "metric_"+key)
	if v != "metric_"+key {
		return v
	}
	return key
}

// langFromRequest determines the UI language from the request.
func langFromRequest(r *http.Request) string {
	if q := r.URL.Query().Get("lang"); q == "en" || q == "ru" || q == "sr" {
		return q
	}
	if c, err := r.Cookie("lang"); err == nil {
		if c.Value == "en" || c.Value == "ru" || c.Value == "sr" {
			return c.Value
		}
	}
	return "en"
}
