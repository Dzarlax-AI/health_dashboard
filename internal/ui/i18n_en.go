package ui

// translationsEn holds the English UI strings. The map is composed
// into the multi-language `translations` table in i18n.go. Missing
// keys in other languages fall back here via T().
var translationsEn = map[string]string{
	"app_title":             "Health",
	"explore":               "Explore",
	"loading":               "Loading your health data",
	"readiness":             "Readiness",
	"recovery":              "Recovery",
	"readiness_today_label": "Today",

	// Section catalogue (GET /api/sections) — stable list of detail
	// pages with localized title + subtitle for native clients that
	// want a navigation list without hardcoding strings. See
	// sectionCatalogue in handler.go.
	"section_cardio_title":      "Cardio",
	"section_cardio_subtitle":   "RHR · HRV · VO2 · respiratory",
	"section_activity_title":    "Activity",
	"section_activity_subtitle": "Steps · energy · exercise · distance",
	"section_recovery_title":    "Recovery",
	"section_recovery_subtitle": "Sleep summary · HRV CV · wrist temp",

	"readiness_trend_label":      "7-day trend",
	"back":                       "Back",
	"compare":                    "Compare",
	"all_metrics":                "Metrics",
	"nav_settings":               "Settings",
	"your_trends":                "Your trends",
	"search_placeholder":         "Search metrics...",
	"esc_hint":                   "ESC to close",
	"no_metrics_found":           "No metrics found",
	"no_data":                    "No data",
	"no_data_range":              "No data for this range",
	"no_sleep_data":              "No sleep data for this range",
	"start_syncing":              "Start syncing health data to see your readiness score.",
	"data_from":                  "Data from ",
	"days_ago":                   "d ago",
	"this_week":                  "This week",
	"activity_vs_recovery":       "Activity vs Recovery",
	"activity_recovery_subtitle": "How physical load affects your HRV",
	"activity_load":              "Activity load",
	"sleep_section":              "Sleep",
	"sleep_subtitle":             "7-night average",
	"deep_sleep":                 "Deep sleep",
	"rem_sleep":                  "REM sleep",
	"awake_time":                 "Awake time",
	"efficiency":                 "Efficiency",
	"bucket":                     "Bucket",
	"agg":                        "Agg",
	"auto":                       "Auto",
	"minute":                     "Minute",
	"hour":                       "Hour",
	"day":                        "Day",
	"preset_all":                 "All",
	"avg":                        "Avg",
	"sum":                        "Sum",
	"max":                        "Max",
	"min":                        "Min",
	"previous_period":            "Previous period",
	"vs_yesterday":               "vs yesterday",
	"stable":                     "Stable",
	"load_pct":                   "Load %",
	"hrv_ms":                     "HRV ms",
	"nights":                     "Nights",
	"avg_total":                  "Avg total",
	"avg_deep":                   "Avg deep",
	"avg_rem":                    "Avg REM",
	"points":                     "Points",
	"stale_prefix":               "Data from ",
	"stale_suffix":               "d ago",
	"status_good":                "Looking good",
	"status_fair":                "Needs attention",
	"status_low":                 "Take care",
	"cat_heart":                  "Heart & Vitals",
	"cat_activity":               "Activity",
	"cat_fitness":                "Fitness",
	"cat_sleep":                  "Sleep",
	"cat_body":                   "Body",
	"cat_env":                    "Environment",
	"cat_nutrition":              "Nutrition",
	"cat_other":                  "Other",
	"phase_deep":                 "Deep",
	"phase_rem":                  "REM",
	"phase_core":                 "Core",
	"phase_awake":                "Awake",
	"trend_steps":                "Steps",
	"trend_heart_rate":           "Heart Rate",
	"trend_sleep":                "Sleep",
	"trend_hrv":                  "HRV",
	"trend_readiness":            "Readiness",
	"ai_insight_title":           "Detailed AI briefing",
	"teps":                       "Steps",
	"leep":                       "Sleep",
	"ate":                        "Respiratory Rate",

	// Metric labels.
	"metric_sleep_total":                       "Total Sleep",
	"metric_night_sleep_total":                 "Night Sleep",
	"metric_nap_total":                         "Naps",
	"metric_sleep_deep":                        "Deep Sleep",
	"metric_sleep_rem":                         "REM Sleep",
	"metric_sleep_core":                        "Core Sleep",
	"metric_sleep_unspecified":                 "Asleep (no stages)",
	"metric_sleep_awake":                       "Awake Time",
	"chart_sleep_unspecified_hint":             "source did not report deep/REM/core breakdown",
	"metric_heart_rate":                        "Heart Rate",
	"metric_resting_heart_rate":                "Resting HR",
	"metric_walking_heart_rate_average":        "Walking HR",
	"metric_heart_rate_variability":            "HRV",
	"metric_blood_oxygen_saturation":           "Blood Oxygen",
	"metric_respiratory_rate":                  "Respiratory Rate",
	"metric_step_count":                        "Steps",
	"metric_walking_running_distance":          "Distance",
	"metric_active_energy":                     "Active Calories",
	"metric_basal_energy_burned":               "Resting Calories",
	"metric_apple_exercise_time":               "Exercise",
	"metric_apple_stand_time":                  "Stand Time",
	"metric_apple_stand_hour":                  "Stand Hours",
	"metric_physical_effort":                   "Physical Effort",
	"metric_flights_climbed":                   "Flights Climbed",
	"metric_stair_speed_up":                    "Stair Speed",
	"metric_walking_speed":                     "Walking Speed",
	"metric_walking_step_length":               "Step Length",
	"metric_walking_double_support_percentage": "Double Support",
	"metric_walking_asymmetry_percentage":      "Walking Asymmetry",
	"metric_apple_sleeping_wrist_temperature":  "Wrist Temp",
	"metric_breathing_disturbances":            "Breathing Disturbances",
	"metric_environmental_audio_exposure":      "Noise Exposure",
	"metric_headphone_audio_exposure":          "Headphone Volume",
	"metric_time_in_daylight":                  "Daylight",
	"metric_vo2_max":                           "VO2 Max",
	"metric_six_minute_walking_test_distance":  "6-min Walk",
	"metric_readiness":                         "Readiness",
	"metric_oxygen_saturation":                 "Blood Oxygen",
	"metric_heart_rate_variability_sdnn":       "HRV",
	"metric_environmental_audio":               "Ambient Noise",
	"metric_headphone_audio":                   "Headphone Volume",
	"metric_walking_double_support":            "Double Support",
	"metric_walking_asymmetry":                 "Walking Asymmetry",
	"metric_environmental_sound_reduction":     "Noise Reduction",
	"metric_stair_ascent_speed":                "Stair Ascent",
	"metric_stair_descent_speed":               "Stair Descent",
	"metric_wrist_temperature":                 "Wrist Temp",
	"metric_walking_steadiness":                "Walking Steadiness",
	"metric_body_mass":                         "Body Weight",
	"metric_body_mass_index":                   "BMI",
	"metric_body_fat_percentage":               "Body Fat",
	"metric_lean_body_mass":                    "Lean Mass",
	"metric_height":                            "Height",
	"metric_blood_pressure_systolic":           "Systolic BP",
	"metric_blood_pressure_diastolic":          "Diastolic BP",
	"metric_heart_rate_recovery":               "HR Recovery",
	"metric_distance_cycling":                  "Cycling Distance",
	"metric_distance_swimming":                 "Swimming Distance",
	"metric_swimming_stroke_count":             "Swim Strokes",
	"metric_mindful_minutes":                   "Mindful Minutes",
	"metric_alcoholic_beverages":               "Alcoholic Beverages",
	"metric_six_min_walk_distance":             "6-min Walk",
	"metric_dietary_energy":                    "Dietary Calories",
	"metric_dietary_protein":                   "Protein",
	"metric_dietary_carbs":                     "Carbohydrates",
	"metric_dietary_fat":                       "Total Fat",
	"metric_dietary_fat_saturated":             "Saturated Fat",
	"metric_dietary_fat_monounsaturated":       "Mono Fat",
	"metric_dietary_fat_polyunsaturated":       "Poly Fat",
	"metric_dietary_water":                     "Water",
	"metric_dietary_sodium":                    "Sodium",
	"metric_dietary_sugar":                     "Sugar",
	"metric_dietary_fiber":                     "Fiber",
	"metric_dietary_caffeine":                  "Caffeine",
	"metric_dietary_calcium":                   "Calcium",
	"metric_dietary_iron":                      "Iron",
	"metric_dietary_cholesterol":               "Cholesterol",
	"metric_dietary_potassium":                 "Potassium",
	"metric_dietary_magnesium":                 "Magnesium",
	"metric_dietary_phosphorus":                "Phosphorus",
	"metric_dietary_zinc":                      "Zinc",
	"metric_dietary_copper":                    "Copper",
	"metric_dietary_manganese":                 "Manganese",
	"metric_dietary_selenium":                  "Selenium",
	"metric_dietary_iodine":                    "Iodine",
	"metric_dietary_molybdenum":                "Molybdenum",
	"metric_dietary_folate":                    "Folate",
	"metric_dietary_biotin":                    "Biotin (B7)",
	"metric_dietary_vitamin_a":                 "Vitamin A",
	"metric_dietary_vitamin_c":                 "Vitamin C",
	"metric_dietary_vitamin_d":                 "Vitamin D",
	"metric_dietary_vitamin_e":                 "Vitamin E",
	"metric_dietary_vitamin_k":                 "Vitamin K",
	"metric_dietary_vitamin_b6":                "Vitamin B6",
	"metric_dietary_vitamin_b12":               "Vitamin B12",
	"metric_dietary_niacin":                    "Niacin (B3)",
	"metric_dietary_riboflavin":                "Riboflavin (B2)",
	"metric_dietary_thiamin":                   "Thiamin (B1)",
	"metric_dietary_pantothenic_acid":          "Pantothenic Acid (B5)",
	"by_source":                                "Sources",
	"source_comparison":                        "Source comparison",
	"lbl_total":                                "Total",
	"lbl_deep":                                 "Deep",
	"lbl_core":                                 "Core",
	"how_it_works":                             "How it works",
	"health_sections":                          "Health overview",
	"at_a_glance":                              "At a glance",

	// Dual-baseline trend chips on metric cards
	"trend_vs_7d":  "vs 7d",
	"trend_vs_30d": "vs 30d",

	// Energy Bank widget (Bevel-inspired prescriptive verdict)
	"energy_label":                      "Energy Bank",
	"energy_capacity_label":             "Today's capacity",
	"energy_current_label":              "Available now",
	"energy_drain_label":                "Used so far",
	"energy_verdict_push_hard":          "Push hard",
	"energy_verdict_moderate":           "Moderate day",
	"energy_verdict_active_recovery":    "Active recovery only",
	"energy_verdict_rest":               "Rest day",
	"energy_component_morning_capacity": "Morning capacity",
	"energy_component_activity_load":    "Activity load (today vs 28-day chronic)",
	"energy_component_autonomic_stress": "Autonomic stress (RHR / HRV)",
	"energy_state_good_title":           "Charged",
	"energy_state_good_desc":            "Plenty in the tank — you can challenge yourself today.",
	"energy_state_medium_title":         "Balanced",
	"energy_state_medium_desc":          "Decent reserves — train with intent but watch the volume.",
	"energy_state_low_title":            "Running low",
	"energy_state_low_desc":             "Reserves are thin — keep effort easy and prioritise recovery.",
	"energy_state_critical_title":       "Depleted",
	"energy_state_critical_desc":        "Tank near empty — rest is the highest-yield choice today.",
	"details":                           "Details",

	// Subjective morning check-in confirmation on the dashboard hero.
	// Empty answer (status=prompted/expired) → line is not rendered.
	"checkin_today_label":  "Your morning answer:",
	"checkin_answer_great": "Great",
	"checkin_answer_ok":    "OK",
	"checkin_answer_meh":   "Meh",
	"checkin_answer_sick":  "Sick",

	// Methodology status badges — surface the honest provenance of each
	// score on the dashboard. Manus methodology review (2026-05-17)
	// recommends labelling scores as heuristic / validated_floor /
	// experimental / labeling-framework so users can distinguish
	// expert-tuned formulae from leakage-aware floors. Mapping:
	//   readiness v1     -> heuristic_personalized
	//   energy_bank v1   -> heuristic_prescriptive
	//   energy_bank v2   -> experimental_formula
	//   recovery floor   -> validated_floor_candidate
	//   acute risk       -> labeling_framework_ready
	//   chronic load     -> experimental_not_production
	"methodology_status_heuristic_personalized":           "Heuristic",
	"methodology_status_heuristic_personalized_desc":      "Expert-tuned formula using your personal baselines. Not a validated forecasting model.",
	"methodology_status_heuristic_prescriptive":           "Heuristic",
	"methodology_status_heuristic_prescriptive_desc":      "Rule-based advice from heuristic capacity and drain. Not yet validated against subjective state.",
	"methodology_status_experimental_formula":             "Experimental",
	"methodology_status_experimental_formula_desc":        "Formula kernel under evaluation. Not the production decision layer yet.",
	"methodology_status_validated_floor_candidate":        "Validated floor",
	"methodology_status_validated_floor_candidate_desc":   "Leakage-aware target with a feasibility-checked baseline floor (EWMA45). Production-grade plumbing; no learned model on top yet.",
	"methodology_status_labeling_framework_ready":         "Labeling framework",
	"methodology_status_labeling_framework_ready_desc":    "Leakage-free event labels ready for downstream models. The label itself is the deliverable.",
	"methodology_status_experimental_not_production":      "Experimental",
	"methodology_status_experimental_not_production_desc": "Under evaluation. Do not act on this score as if it were validated.",

	// ─── Admin: basic settings + cache ──────────────────────────
	"admin_title":               "Admin",
	"admin_cache_status":        "Cache status",
	"admin_refresh":             "Refresh",
	"admin_actions":             "Actions",
	"admin_raw":                 "Raw data",
	"admin_minute":              "Minute cache",
	"admin_hourly":              "Hourly cache",
	"admin_daily":               "Daily scores",
	"admin_metrics":             "metrics",
	"admin_empty":               "empty",
	"admin_score_version":       "Score version",
	"admin_last_sync":           "Last sync",
	"admin_incremental_title":   "Update cache",
	"admin_incremental_desc":    "Fill missing entries since last run. Fast, safe to run anytime.",
	"admin_force_title":         "Rebuild all",
	"admin_force_desc":          "Clear and recompute all caches from raw data. Use after formula changes.",
	"admin_run":                 "Run",
	"admin_force_run":           "Rebuild",
	"admin_target_user":         "Target user",
	"admin_target_user_current": "— current user —",

	// ─── Admin: Telegram notifications ─────────────────────────
	"admin_notify_title":            "Telegram reports",
	"admin_notify_morning_title":    "Morning report",
	"admin_notify_morning_desc":     "Send a test sleep summary right now.",
	"admin_notify_evening_title":    "Evening report",
	"admin_notify_evening_desc":     "Send a test day summary right now.",
	"admin_notify_token":            "Bot token",
	"admin_notify_chat_id":          "Chat ID",
	"admin_notify_webhook_label":    "Webhook",
	"admin_notify_webhook_retry":    "Retry",
	"webhook_badge_ok":              "✓ registered",
	"webhook_badge_pending":         "⏳ registering",
	"webhook_badge_failed":          "✗ failed",
	"webhook_badge_deleted":         "— deleted",
	"webhook_badge_unknown":         "— unknown",
	"admin_notify_lang":             "Language",
	"admin_notify_timezone":         "Timezone",
	"admin_notify_timezone_hint":    "Required for daily reports and historical EnergyBank computation. Use an IANA name (e.g. Europe/Belgrade, America/New_York).",
	"admin_notify_schedule_morning": "Morning report hour",
	"admin_notify_schedule_evening": "Evening report hour",
	"admin_notify_weekday":          "Weekdays",
	"admin_notify_weekend":          "Weekends",
	"admin_notify_save":             "Save",
	"admin_notify_saved":            "Settings saved",
	"admin_notify_send":             "Send test",
	"admin_notify_test_morning":     "Test morning",
	"admin_notify_test_evening":     "Test evening",

	// ─── Admin: EnergyBank backfill ────────────────────────────
	"admin_energy_backfill_title":      "Historical EnergyBank",
	"admin_energy_backfill_desc":       "Compute retrospective EnergyBank snapshots from your imported Apple Health history. Required for the per-user verdict calibration to use your own distribution instead of cold-start defaults.",
	"admin_energy_backfill_loading":    "Loading…",
	"admin_energy_backfill_load_error": "Failed to load backfill status.",
	"admin_energy_backfill_summary":    "{complete} days of complete health history · {backfilled} EnergyBank snapshots already backfilled · range: {earliest} → {to}",
	"admin_energy_backfill_run":        "Compute historical EnergyBank",
	"admin_energy_backfill_running":    "A backfill is already running.",
	"admin_energy_backfill_need_tz":    "Set your Timezone above first.",
	"admin_energy_backfill_no_data":    "No complete days yet — import Apple Health data or wait for live ingest.",
	"admin_energy_backfill_confirm":    "This recomputes EnergyBank for every historical day. Continue?",
	"admin_energy_backfill_starting":   "Starting…",
	"admin_energy_backfill_progress":   "Processing day {done} of {total} · ok={ok}",
	"admin_energy_backfill_done":       "Done: {ok} written · {skipped} skipped (insufficient lookback) · {errors} errors",

	// ─── Admin: data integrity / quality ───────────────────────
	"admin_gaps_section_title":        "Data integrity",
	"admin_gaps_check":                "Check gaps",
	"admin_quality_title":             "Data quality audit",
	"admin_quality_run":               "Run audit",
	"admin_quality_fix":               "Mark suspects + clean impossibles",
	"admin_quality_digest":            "Send digest now",
	"admin_quality_clean":             "All clean — no anomalies found.",
	"admin_quality_total":             "Total rows",
	"admin_quality_bad":               "Out of range",
	"admin_quality_range":             "Range",
	"admin_quality_metric":            "Metric",
	"admin_quality_sample":            "Sample",
	"admin_quality_week":              "Last 7 days",
	"admin_quality_impossible":        "Impossible",
	"admin_quality_suspect":           "Suspect",
	"admin_quality_missed":            "Missed nights",
	"admin_quality_fixed":             "Flagged %d impossible, %d suspect rows.",
	"admin_checkin_coverage_title":    "Subjective check-in coverage",
	"admin_checkin_coverage_desc":     "Read-only view of the latest Telegram morning prompts and response latency.",
	"admin_checkin_total":             "Days",
	"admin_checkin_prompted_coverage": "Prompted",
	"admin_checkin_answered_coverage": "Answered",
	"admin_checkin_avg_response":      "Avg response",
	"admin_checkin_no_response":       "No answers",
	"admin_checkin_answers":           "Answers:",
	"admin_checkin_date":              "Date",
	"admin_checkin_status":            "Status",
	"admin_checkin_answer":            "Answer",
	"admin_checkin_latency":           "Latency",
	"admin_checkin_status_prompted":   "Prompted",
	"admin_checkin_status_answered":   "Answered",
	"admin_checkin_status_late":       "Late",
	"admin_checkin_status_expired":    "Expired",
	"admin_checkin_status_missing":    "Missing",

	// ─── Admin: AI briefing settings ───────────────────────────
	"admin_ai_title":      "AI Morning Briefing",
	"admin_ai_key":        "Gemini API key",
	"admin_ai_model":      "Model",
	"admin_ai_max_tokens": "Max output tokens",
	"admin_ai_save":       "Save",
	"admin_ai_saved":      "Settings saved",

	// ─── Admin: EnergyBank stress-drain config + validation ────
	"admin_energy_title":          "EnergyBank v2.2 — Stress drain",
	"admin_energy_warning":        "Non-production placeholder values until §4.5 validation rubric returns 'validated'. Effective β is 0 while Stress drain is disabled, regardless of β value below.",
	"admin_energy_stress_enabled": "Stress drain enabled",
	"admin_energy_beta":           "β (stress drain coefficient)",
	"admin_energy_z_threshold":    "z-score threshold",
	"admin_energy_effective_beta": "Effective β (live)",
	"admin_energy_save":           "Save",
	"admin_energy_saved":          "Energy settings saved",
	"admin_help_summary":          "What is this and when to change?",
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
	"admin_stress_validation_title":       "Stress validation rubric (§4.5)",
	"admin_stress_validation_desc":        "Runs Pearson r over the rolling 30-day window: HRV next-morning (primary), RHR next-morning shift, sleep architecture. Read-only — does NOT auto-flip stress_drain_enabled. Operator decision after reviewing the verdict.",
	"admin_stress_validation_run":         "Run validation",
	"admin_stress_validation_loading":     "Running rubric over rolling 30-day window…",
	"admin_stress_validation_sparse":      "sparse",
	"admin_stress_validation_no_data":     "no data",
	"admin_stress_validation_flags_label": "flags:",
	"admin_stress_validation_ch1":         "Channel 1 (HRV next-morning)",
	"admin_stress_validation_ch2":         "Channel 2 (RHR next-morning shift)",
	"admin_stress_validation_ch3":         "Channel 3 (sleep architecture)",
	"admin_stress_validation_votes":       "votes",
	"admin_stress_validation_window_fmt":  "window {window} days, {days} days with data",

	// ─── Admin: Apple Health import ────────────────────────────
	"admin_import_title":     "Apple Health Import",
	"admin_import_desc":      "Upload your Apple Health export.zip to import historical data. Duplicates are skipped automatically.",
	"admin_import_choose":    "Choose file…",
	"admin_import_batch":     "points/batch",
	"admin_import_pause":     "ms pause",
	"admin_import_start":     "Start import",
	"admin_import_uploading": "Uploading…",
	"admin_import_running":   "Import running…",

	// ─── Stress-flag chips (dashboard hero row) ────────────────
	// Duplicated from internal/health/i18n_en.go because dashboard.html
	// uses ui.T (this map), not health.LangStrings.
	"stress_flags_aria":    "Stress signal flags",
	"stress_detail_what":   "What it is",
	"stress_detail_cause":  "What caused it",
	"stress_detail_risk":   "Why it matters",
	"stress_detail_action": "What to do",

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
	"stress_flag_stale_stress_label": "Coverage gap",
	"stress_flag_stale_stress_detail_html": `<h5>What it is</h5>
<p>The day ended with less than 8 hours of heart-rate data in your waking window. The sustained-load drain disabled for this day.</p>
<h5>What caused it</h5>
<p>Most likely the watch was off the wrist for an extended stretch, or there was a sync gap with iPhone.</p>
<h5>What to do</h5>
<p>Wear the watch more consistently and check that iPhone sync is working. No action for today's numbers — past days are not recomputed.</p>`,
	"stress_flag_data_accruing_label": "Gathering data",
	"stress_flag_data_accruing_detail_html": `<h5>What it is</h5>
<p>Your day is still in progress. The sustained-load score needs at least 8 hours of heart-rate samples across your waking window before it can compute, and not enough hours have accumulated yet.</p>
<h5>Why it matters</h5>
<p>The score is asking the question "did your heart run elevated for a real chunk of the day?" That answer only makes sense once a real chunk of the day has actually happened. Showing a number early would be misleading.</p>
<h5>What to do</h5>
<p>Nothing — keep wearing the watch through your usual day. The flag clears automatically once enough hours have been collected.</p>`,
	"stress_flag_calibration_warmup_label": "Calibrating",
	"stress_flag_calibration_warmup_detail_html": `<h5>What it is</h5>
<p>Your personal baseline (HRV, heart rate, breathing) is still being learned — needs about a week of consistent data. Stress-flag thresholds stay conservative for now.</p>
<h5>What to do</h5>
<p>Just keep wearing the watch. The flag clears automatically after a few more days.</p>`,

	// ─── Admin: Readiness redesign — operational contract preview ───
	"admin_contract_title": "Readiness redesign — contract preview",
	"admin_contract_desc":  "Day-by-day table of what each readiness chip would show. The two binary chips (Acute, Chronic) read their cutoff from <code>chip_calibrations</code>; use the \"Recompute chip calibrations\" button in Operations below to refresh them. Hover any cell to see the underlying value, cutoff, baseline, target and epoch.",

	// ─── Admin: Operations group ────────────────────────────────────
	"admin_ops_group":            "Operations",
	"admin_ops_group_desc":       "Actions in this group affect the active profile tab only.",
	"admin_ops_redesign_title":   "Readiness redesign",
	"admin_ops_redesign_desc":    "Per-tenant chip thresholds are derived from the last 180 days of data. Click <strong>Recompute</strong> after a config change or a fresh backfill. The result shows up in the contract preview above.",
	"admin_chip_recompute_title": "Recompute chip calibrations",
	"admin_chip_recompute_desc":  "Reads the last 180 eligible days, computes the cutoff and base-rate guard for each binary chip, and writes them to <code>chip_calibrations</code>. Affects the active profile tab only.",
	"admin_chip_recompute_btn":   "Recompute",

	"admin_quality_maintenance_title": "Data quality maintenance",
	"admin_quality_maintenance_desc":  "These buttons change <code>metric_points.quality</code> flags or send a Telegram digest. The read-only audit lives in <strong>Status &amp; diagnostics</strong> above — open that first to see what would change.",
	"admin_quality_fix_desc":          "Marks out-of-range points as <code>impossible</code> and z-score outliers as <code>suspect</code>, so they are excluded from scoring.",
	"admin_quality_digest_desc":       "Send the weekly quality digest to Telegram right now (normally fires on the configured day of week).",

	// ─── Admin: Onboarding wizard — page shell ──────────────────────
	"admin_wizard_title":       "Readiness redesign — tenant onboarding wizard",
	"admin_wizard_desc":        "A 7-step guided flow for the active profile tab. Every step reads the latest state from the database, so you can close the page and come back — nothing here is stored in cookies. <code>schema=all</code> is rejected on every step that writes data. Use a step's <em>Refresh</em> button to re-render it.",
	"admin_wizard_load_all":    "Load wizard for active profile",
	"admin_wizard_pick_tenant": "Open a profile tab and click <em>Load</em>.",
	"admin_wizard_refresh":     "Refresh",
	"admin_wizard_show_plan":   "Show plan",
	"admin_wizard_recompute":   "Recompute",
	"admin_wizard_step1_title": "Tenant check",
	"admin_wizard_step2_title": "Chronic load config",
	"admin_wizard_step3_title": "Coverage and base rate",
	"admin_wizard_step4_title": "Phase 0 backfill",
	"admin_wizard_step5_title": "Verify",
	"admin_wizard_step6_title": "Recompute chip calibrations",
	"admin_wizard_step7_title": "Final preview (last 7 days)",
	"admin_wizard_step6_intro": "After Step 4 has run and Step 5 looks healthy, click <em>Recompute</em> to derive chip cutoffs.",

	// ─── Wizard step 1 (tenant check) ───────────────────────────────
	"wiz_schema":             "Schema",
	"wiz_active_epoch":       "Active epoch",
	"wiz_active_epoch_from":  "from",
	"wiz_schema_health":      "Schema health",
	"wiz_schema_ok":          "✓ ok",
	"wiz_unknown_epoch_warn": "rows are tagged with the sentinel <code>unknown</code> epoch. Fix them before running calibration — the writers fell back to the sentinel, which means something is off in the <code>source_epochs</code> table.",
	"wiz_col_sub_score":      "sub_score",
	"wiz_col_targets":        "targets",
	"wiz_col_eligible":       "eligible",
	"wiz_col_baselines":      "baselines",
	"wiz_col_w_value":        "with value",
	"wiz_col_features":       "features",
	"wiz_col_latest":         "latest",
	"wiz_col_target_kind":    "target_kind",
	"wiz_step1_have_rows":    "Phase 0 rows are present — go to <strong>Step 2</strong>. If the counts look stale, Step 4 (backfill) will refresh them.",
	"wiz_step1_no_rows":      "No Phase 0 rows for this tenant yet. <strong>Step 4 backfill</strong> is the next thing to run; Steps 2–3 will show defaults / empty coverage until then.",

	// ─── Wizard step 2 (chronic config) ─────────────────────────────
	"wiz_row_effective":        "effective",
	"wiz_row_defaults":         "defaults",
	"wiz_step2_using_defaults": "Using the defaults calibrated against the <code>health</code> tenant (PR #97). If Step 3's Acute OR base rate falls outside the 15–30% band, retune <code>min_acute_density</code> via <code>/api/admin/readiness-redesign/config</code> before Step 4. Otherwise the defaults are safe.",
	"wiz_step2_custom":         "Per-tenant overrides are applied. Check that they still make sense given Step 3's base rate before backfilling.",
	"wiz_step2_clamped":        "⚠ A settings row had a non-positive value and was reset to the default. Inspect the <code>settings</code> rows before proceeding.",

	// ─── Wizard step 3 (coverage) ───────────────────────────────────
	"wiz_step3_acute_eligible":   "Acute eligible rows (current epoch)",
	"wiz_step3_acute_baserate":   "Acute OR base rate",
	"wiz_step3_chronic_eligible": "Chronic eligible rows",
	"wiz_step3_no_rows":          "No eligible Acute OR rows in the current epoch yet. Step 4 backfill will produce them; come back here afterwards to read the rate.",
	"wiz_step3_in_band":          "The Acute OR base rate is inside the 15–30% band — the defaults from Step 2 are fine, no retune needed.",
	"wiz_step3_out_band":         "⚠ Acute OR base rate is <strong>outside the 15–30% band</strong>. Consider retuning <code>min_acute_density</code> via <code>POST /api/admin/readiness-redesign/config</code> before Step 4 — otherwise <code>chronic_acute_density</code> labels may end up too rare or too frequent to be useful.",

	// ─── Wizard step 4 (plan + run + result) ────────────────────────
	"wiz_step4_tenant":          "Tenant",
	"wiz_step4_from":            "From",
	"wiz_step4_to":              "To",
	"wiz_step4_to_local":        "tenant local",
	"wiz_step4_days":            "Days",
	"wiz_step4_force":           "Force",
	"wiz_step4_subscores":       "Sub-scores",
	"wiz_step4_subscores_order": "Runs in dependency order Recovery → Passive → Acute → Chronic. Idempotent on the row keys, so it is safe to rerun.",
	"wiz_step4_run_btn":         "Run backfill",
	"wiz_step4_run_hint":        "Synchronous — keep this tab open until the result table appears. The button stays disabled while a backfill is in flight.",
	"wiz_step4_progress":        "Running Phase 0 backfill — synchronous, usually 1–2 min per tenant. Keep this tab open.",
	"wiz_step4_range":           "Range",
	"wiz_step4_col_written":     "written",
	"wiz_step4_col_error":       "error",
	"wiz_step4_done_hint":       "Step 5 (verify) and Step 7 (preview) refreshed automatically. The next action is Step 6 (recompute chip calibrations).",

	// ─── Wizard step 5 (verify + threshold echo) ────────────────────
	"wiz_step5_unknown_epoch_warn": "rows are still tagged with the <code>unknown</code> epoch. Investigate <code>source_epochs</code> before trusting the chip cells in Step 7.",
	"wiz_step5_threshold_title":    "Chronic threshold echo",
	"wiz_step5_threshold_desc":     "Compares the thresholds the chronic writer stamped onto a sampled row's <code>data_coverage</code> with the effective config from Step 2. A mismatch means the writer ran with stale settings — rerun Step 4.",
	"wiz_step5_threshold_load_err": "Failed to load the echo:",
	"wiz_step5_threshold_no_rows":  "No eligible chronic rows yet — run Step 4 first, then come back.",
	"wiz_step5_field":              "field",
	"wiz_step5_sampled_from":       "sampled from",
	"wiz_step5_effective_config":   "effective config",
	"wiz_step5_match":              "✓",
	"wiz_step5_mismatch":           "✗ mismatch",
	"wiz_step5_writer_drift":       "⚠ The writer used different thresholds than the current config. Rerun Step 4 to bring chronic rows back in line, then come here.",

	// ─── Wizard step 6 (recompute result) ───────────────────────────
	"wiz_step6_progress":   "Recomputing chip calibrations — usually under a second per tenant.",
	"wiz_step6_col_status": "status",
	"wiz_step6_col_cutoff": "cutoff",
	"wiz_step6_col_p80":    "p80",
	"wiz_step6_col_base":   "base rate",
	"wiz_step6_col_neli":   "n_eligible",
	"wiz_step6_col_npos":   "n_positive",
	"wiz_step6_done_hint":  "Step 7 (final preview) refreshed automatically. Hover any chip cell there to see the cutoff and reason chain.",

	// ─── Admin: registered users (multi-tenant) ─────────────────────
	"admin_users_title":        "Registered users",
	"admin_users_empty":        "No users yet.",
	"admin_users_col_username": "Username",
	"admin_users_col_email":    "Email",
	"admin_users_col_api_key":  "API key",
	"admin_users_col_role":     "Role",
	"admin_users_role_admin":   "admin",
	"admin_users_role_user":    "user",
	"admin_users_add_title":    "Add user",
	"admin_users_username":     "Username",
	"admin_users_email":        "Email",
	"admin_users_email_hint":   "(optional, for SSO)",
	"admin_users_password":     "Password",
	"admin_users_add_btn":      "Add user",
	"admin_users_reveal_key":   "Reveal",

	// ─── Admin: top-level group headers + section titles ────────────
	"admin_group_status":          "Status & diagnostics",
	"admin_group_configuration":   "Configuration",
	"admin_group_users":           "Users",
	"admin_tab_admin":             "Admin",
	"admin_tab_general":           "General settings",
	"admin_tab_current_user":      "Current user",
	"admin_scope_global":          "Global",
	"admin_scope_profiles":        "Profiles",
	"admin_profile_diagnostics":   "Diagnostics",
	"admin_profile_readiness":     "Readiness",
	"admin_profile_energy":        "EnergyBank",
	"admin_overview_cache_desc":   "Sync and cache freshness",
	"admin_overview_gaps_desc":    "Look for missing ingest days",
	"admin_overview_quality_desc": "Review impossible and suspect points",
	"admin_overview_checkin_desc": "Telegram morning prompt coverage",
	"admin_user_scope_label":      "User scope",
	"admin_user_scope_desc":       "Actions in this tab affect only this user's tenant schema.",
	"admin_general_scope_desc":    "These settings affect the whole Health Dashboard installation.",
	"admin_admin_scope_desc":      "User management for the whole Health Dashboard installation.",
	"admin_open_and_refresh":      "Open this section and click Refresh to load the table.",

	// ─── Admin: read-only quality audit blurb ───────────────────────
	"admin_quality_audit_desc": "Read-only audit: lists points the writers would flag as impossible / suspect. The maintenance actions (Fix / Digest) live in <strong>Operations</strong> below — they're not read-only and shouldn't be one click away from a refresh.",

	// ─── Admin: operational-contract preview fragment ───────────────
	"admin_contract_window_label":  "Window",
	"admin_contract_window_suffix": "per-tenant local TZ",
	"admin_contract_col_tenant":    "tenant",
	"admin_contract_col_date":      "date",
	"admin_contract_col_recovery":  "recovery",
	"admin_contract_col_passive":   "passive",
	"admin_contract_col_chronic":   "chronic",
	"admin_contract_col_acute":     "acute",
	"admin_contract_empty":         "no rows yet — run the readiness redesign backfill first",

	// ─── Admin: readiness naive-layer monitoring fragment ───────────
	"admin_monitoring_title":                 "Readiness naive-layer monitoring",
	"admin_monitoring_desc":                  "Read-only §6.4 checks over target coverage, classifier drift, source epochs, and chip unknown rates.",
	"admin_monitoring_empty":                 "no monitoring rows yet",
	"admin_monitoring_as_of":                 "as of",
	"admin_monitoring_col_signal":            "signal",
	"admin_monitoring_col_target":            "target",
	"admin_monitoring_col_status":            "status",
	"admin_monitoring_col_value":             "value",
	"admin_monitoring_col_reference":         "reference",
	"admin_monitoring_signal_coverage":       "coverage",
	"admin_monitoring_signal_drift":          "positive-rate drift",
	"admin_monitoring_signal_unknown":        "unknown rate",
	"admin_monitoring_floor":                 "floor",
	"admin_monitoring_window":                "window",
	"admin_monitoring_inputs_stable_through": "inputs stable through %s",
	"admin_monitoring_inputs_stale_reason":   "%s: %s",
	"admin_monitoring_inputs_stale_by":       "%s: inputs stale by %dd, last stable %s",

	// ─── Admin: user-management JS feedback messages ────────────────
	"admin_users_msg_required":      "Username and password are required.",
	"admin_users_msg_created":       "User {username} created. API key: {apiKey}",
	"admin_users_msg_created_warn":  "User {username} created. API key: {apiKey} (warning: {warning})",
	"admin_users_msg_error_generic": "Error creating user.",
}
