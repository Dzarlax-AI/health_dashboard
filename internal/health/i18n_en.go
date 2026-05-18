package health

var en = LangStrings{
	// Readiness
	"readiness_optimal": "Optimal",
	"readiness_fair":    "Fair",
	"readiness_low":     "Low",
	"tip_optimal":       "Great day for a challenging workout or important tasks.",
	"tip_fair":          "Some deviation from your norm. Moderate activity is a good choice.",
	"tip_low":           "Focus on recovery: hydrate, rest, and avoid intense exercise.",

	// Per-section status labels (BriefingSection.Status) — surfaced via
	// EnrichLabels (internal/health/labels.go) so iOS / other consumers
	// don't maintain a parallel i18n table for the good/fair/low enum.
	"section_status_good": "Good",
	"section_status_fair": "Fair",
	"section_status_low":  "Low",

	// Section titles
	"sec_recovery": "Recovery",
	"sec_sleep":    "Sleep",
	"sec_activity": "Activity",
	"sec_cardio":   "Heart & Lungs",

	// AI block headers — surfaced on /api/ai-briefing as the `header`
	// field of each entry in `sections[]` so iOS / other JSON consumers
	// don't maintain a parallel localization table. Per-block keys
	// rather than reusing `sec_*` because the briefing section names
	// (Sleep, Recovery) overlap with two of the four AI blocks but
	// not the other two (Yesterday, Plan for today) — keeping the
	// vocabularies independent avoids accidental drift when one of
	// the four AI block keys gets renamed.
	"ai_block_sleep_header":          "Sleep",
	"ai_block_yesterday_header":      "Yesterday",
	"ai_block_recovery_header":       "Recovery",
	"ai_block_recommendation_header": "Plan for today",

	// Detail labels
	"lbl_hrv":        "HRV",
	"lbl_vs_avg":     "vs avg",
	"lbl_resting_hr": "Resting HR",
	"lbl_duration":   "Duration",
	"lbl_deep_sleep": "Deep sleep",
	"lbl_rem":        "REM",
	"lbl_nap_badge":  "+%dm nap",
	"lbl_steps":      "Steps",
	"lbl_active_cal": "Active calories",
	"lbl_exercise":   "Exercise",
	"lbl_blood_o2":   "Blood oxygen",
	"lbl_vo2":        "VO2 Max",
	"lbl_resp":       "Respiratory rate",

	// HRV detail notes
	"hrv_note_stable": "stable compared to your baseline",
	"hrv_note_good":   "above your usual range — good sign",
	"hrv_note_low":    "below your baseline — could indicate fatigue",

	// RHR detail notes
	"rhr_note_normal": "within your normal range",
	"rhr_note_low":    "lower than usual — well rested",
	"rhr_note_high":   "elevated — may indicate stress or poor recovery",

	// Recovery section summaries
	"rec_summary_good":        "You're well recovered. Your body's ready for activity.",
	"rec_summary_fair":        "Recovery is moderate. Listen to your body today.",
	"rec_summary_low":         "Your body needs more rest. Take it easy if you can.",
	"rec_summary_fair_stress": "HRV/RHR look ok individually, but other markers point to accumulated stress — see headline above.",

	// Sleep duration detail notes
	"sleep_dur_stable": "consistent with your pattern",
	"sleep_dur_more":   "more than usual — nice",
	"sleep_dur_less":   "less than you usually get",

	// Sleep deep detail notes
	"sleep_deep_good": "good ratio for restorative sleep",
	"sleep_deep_low":  "below the ideal 15%+ — quality may suffer",

	// Sleep REM detail notes
	"sleep_rem_good": "healthy range for memory & learning",
	"sleep_rem_low":  "a bit low — REM helps with memory consolidation",

	// Sleep regularity detail
	"lbl_sleep_regularity": "Consistency",
	"sleep_reg_regular":    "very consistent schedule — a strong longevity signal",
	"sleep_reg_moderate":   "some variability — try to keep a fixed bedtime",
	"sleep_reg_irregular":  "high variability — irregular sleep raises health risk",

	// Sleep section summaries (use fmt.Sprintf with one float64)
	"sleep_summary_good": "Averaging %.1f hours — you're sleeping well.",
	"sleep_summary_fair": "Averaging %.1f hours — decent, but there's room to improve.",
	"sleep_summary_low":  "Only %.1f hours on average. Try to get to bed earlier.",

	// Activity steps detail notes
	"steps_note_normal": "on par with your usual activity",
	"steps_note_good":   "more active than usual — keep it up",
	"steps_note_low":    "noticeably below your baseline",

	// Activity calories detail notes
	"cal_note_high":   "burning more than usual",
	"cal_note_low":    "lower burn than your baseline",
	"cal_note_normal": "consistent with your routine",

	// Activity exercise detail notes
	"ex_note_good": "meeting the daily guideline",
	"ex_note_low":  "aim for 30+ min of activity",

	// Activity section summaries (use fmt.Sprintf with one string)
	"act_summary_good": "Averaging %s steps — you're staying active.",
	"act_summary_fair": "Around %s steps — a bit below your usual pace.",
	"act_summary_low":  "Only %s steps recently. Try to move more today.",

	// Cardio SpO2 detail notes
	"spo2_note_good": "healthy range",
	"spo2_note_low":  "slightly low — worth monitoring",

	// Cardio VO2 detail notes
	"vo2_note_stable":  "stable cardio fitness",
	"vo2_note_good":    "improving — your fitness is trending up",
	"vo2_note_decline": "slight decline — stay consistent with cardio",

	// Cardio resp detail notes
	"resp_note_normal":  "normal range (12-20)",
	"resp_note_outside": "outside normal range — keep an eye on it",

	// Cardio section summaries
	"cardio_summary_good": "Cardiovascular indicators look healthy.",
	"cardio_summary_fair": "Some markers are slightly off — keep monitoring.",
	"cardio_summary_low":  "A few indicators need attention. Consider checking with a doctor.",

	// Metric value suffixes
	"unit_steps_day": "%s/day",
	"unit_min_day":   "%s min/day",
	"unit_hrs_night": "%.1f hrs/night",
	"unit_pct_total": "%.0f%% of total",

	// Insights
	"insight_steps_good":    "You hit your average step count on %d of the last 7 days. Nice consistency!",
	"insight_steps_low":     "Only %d of 7 days above your average steps. Try to move more consistently.",
	"insight_hrv_drop":      "Your HRV tends to drop after high-activity days. Make sure to schedule recovery.",
	"insight_hrv_resilient": "Your HRV stays resilient after active days — your recovery is solid.",
	"insight_sleep_active":  "You sleep %.1f hrs on active days vs %.1f hrs on rest days — activity helps your sleep.",
	"insight_sleep_rest":    "You sleep better on rest days (%.1f hrs vs %.1f hrs). Evening activity might be affecting sleep.",
	"insight_overtrain":     "Your activity is high despite signs of exhaustion. Risk of overtraining is elevated.",

	// Alerts
	"alert_rr_anomaly":         "Respiratory rate deviates significantly from your baseline. This can be an early sign of illness or stress.",
	"alert_wrist_temp_anomaly": "Wrist temperature deviates significantly from your baseline. This may indicate fever, inflammation, or hormonal changes.",
	"alert_hrv_cv_high":        "Your 7-day HRV variability is high (CV %.0f%%), suggesting inconsistent recovery. Consider reviewing sleep quality and stress levels.",

	// Headline (cross-metric signal of the day)
	"headline_stress_title":         "Recovery debt building up",
	"headline_stress_detail":        "Several markers point in the same direction: %s. Consider lighter training today.",
	"headline_part_rhr":             "RHR %.0f bpm (+%.0f vs your norm)",
	"headline_part_hrv":             "HRV %.0f ms (z=%.1f)",
	"headline_part_sleep":           "sleep %.1fh",
	"headline_part_awake":           "wake time %.1fh",
	"headline_sleep_debt_title":     "Sleep debt",
	"headline_sleep_debt_detail":    "%.1fh last night is below the 7h target. One short night is fine; watch that it doesn't become a pattern.",
	"headline_stable_title":         "Everything in range",
	"headline_stable_detail":        "All key metrics close to your personal baseline.",
	"headline_dev_heart_rate_variability_above_baseline": "HRV above your norm",
	"headline_dev_heart_rate_variability_below_baseline": "HRV below your norm",
	"headline_dev_resting_heart_rate_above_baseline":     "Resting HR elevated",
	"headline_dev_resting_heart_rate_below_baseline":     "Resting HR below your norm",
	"headline_dev_sleep_total_above_baseline":            "Slept more than usual",
	"headline_dev_sleep_total_below_baseline":            "Slept less than usual",
	"headline_dev_generic":                               "Notable deviation from baseline",
	"headline_dev_detail":                                "%.1f%s — %+.1f%% from your average %.1f%s.",

	// Energy Bank (action prescription)
	"energy_label":          "Energy Bank",
	"energy_hourly_label":   "Last 72 hours",
	"energy_capacity_label": "Today's capacity",
	"energy_current_label":  "Available now",
	"energy_drain_label":    "Used so far",
	"trend_vs_7d":           "vs 7d",
	"trend_vs_30d":          "vs 30d",

	"energy_verdict_push_hard":       "Push hard",
	"energy_verdict_moderate":        "Moderate day",
	"energy_verdict_active_recovery": "Active recovery only",
	"energy_verdict_rest":            "Rest day",

	"energy_reason_full_capacity": "Plenty in the tank and HRV is at or above your norm — green light for a hard session.",
	"energy_reason_optimal":       "Reserve is solid and stress markers are clean — a normal training day is fine.",
	"energy_reason_low_capacity":  "Reserve is low after today's load — keep intensity light.",
	"energy_reason_high_stress":   "HRV %.1f SD off baseline and stress score %d — autonomic load is elevated.",
	"energy_reason_acwr_spike":    "Today's load is already %.0f%% of your 28-day average — a hard session would push the spike further.",

	// v2.2 stress-flag verdict overrides — STRESS_MEASUREMENT.md §4.3.
	"energy_reason_illness_signature": "Temperature, breathing rate and HRV all point to your body fighting something — rest is the recovery move today.",
	"energy_reason_recovery_debt":     "Yesterday's load showed up overnight (HRV down, RHR up) — keep today light to clear the debt before pushing again.",
	"energy_reason_rebound_addon":     "Note: HR was elevated but HRV is also above baseline — that's a recovery-phase pattern, not acute stress.",

	// v2.2 hero-row stress-flag chips.
	"stress_flags_aria":                       "Stress signal flags",
	"stress_flag_illness_signature_label":     "Illness signs",
	"stress_flag_illness_signature_desc":      "Temp, breathing rate and HRV all diverging in the illness direction. Rest aligns with the physiology.",
	"stress_flag_recovery_debt_label":         "Recovery debt",
	"stress_flag_recovery_debt_desc":          "Overnight HRV down and RHR up — yesterday's load caught up. Keep today light.",
	"stress_flag_parasympathetic_rebound_label": "Vagal rebound",
	"stress_flag_parasympathetic_rebound_desc":  "HR elevated but HRV is also above baseline — recovery-phase pattern, not acute stress.",
	"stress_flag_acute_stress_label":          "Acute spike",
	"stress_flag_acute_stress_desc":           "One hour with HR > 2 SD above your awake baseline. Transient, no action needed.",
	"stress_flag_sustained_load_label":        "Sustained load",
	"stress_flag_sustained_load_desc":         "4+ consecutive hours with HR > 1 SD above your awake baseline. Real autonomic load.",
	"stress_flag_stale_stress_label":          "Stress data incomplete",
	"stress_flag_stale_stress_desc":           "Day ended with <8h of HR samples in the awake window (watch off / sync gap) — sustained-load drain disabled for this day.",
	"stress_flag_data_accruing_label":         "Gathering data",
	"stress_flag_data_accruing_desc":          "Day still in progress: need ≥8h of HR samples in the awake window to score sustained cardiovascular load. The metric will appear once enough hours accumulate.",
	"stress_flag_calibration_warmup_label":    "Calibrating",
	"stress_flag_calibration_warmup_desc":     "Personal baseline still in warmup (3-6 samples). Flag thresholds may be conservative.",

	"energy_note_capacity":              "morning capacity carried over from sleep %.1fh + recovery markers",
	"energy_component_morning_capacity": "Morning capacity",
	"energy_component_activity_load":    "Activity load (today vs 28-day chronic)",
	"energy_component_autonomic_stress": "Autonomic stress (RHR / HRV)",

	"details": "Details",

	// Telegram report sections
	"tg_morning_header":      "Morning report",
	"tg_evening_header":      "Day so far",
	"tg_readiness":           "Readiness",
	"tg_readiness_today":     "today",
	"tg_readiness_trend":     "7-day trend",
	"tg_today":               "Today so far",
	"tg_yesterday":           "Yesterday",
	"tg_recommendation":      "Plan for today",
	"tg_alerts":              "Alerts",
	"tg_insights":            "Insights",
	"tg_sources":             "Sources",
	"tg_energy":              "Energy",
	"tg_no_data":             "No fresh data yet.",
	"tg_warn_stale":          "<i>Data is %d day(s) old — Apple Health may not have synced yet.</i>",
	"tg_warn_no_sleep":       "<i>No sleep data for last night yet — phone may not have synced after waking up.</i>",
	"tg_warn_no_activity":    "<i>No activity data for today yet.</i>",
	"tg_vs_yesterday_up":     "+%.0f%% vs yesterday",
	"tg_vs_yesterday_down":   "%.0f%% vs yesterday",

	// Smart-retry stale-data banners (prepended when the morning report fires
	// past the deadline without complete sleep data).
	"tg_stale_no_data":        "⏰ <i>Morning deadline reached, but no sleep data arrived from your watch — open the Health app or check your Apple Watch sync.</i>",
	"tg_stale_recent_segment": "⏰ <i>Morning deadline reached, but the watch is still recording sleep — values below may be incomplete.</i>",
	"tg_stale_still_writing":  "⏰ <i>Morning deadline reached, but sleep fragments are still arriving — values below may be incomplete.</i>",

	// Per-metric "device off" banners. %s is the localised duration ("36h",
	// "2 days") computed at render time.
	"tg_watch_off":     "🔕 <b>Apple Watch off</b> — last HRV/RHR was %s ago. Recovery section skipped.",
	"tg_phone_off":     "📵 <b>Phone not syncing</b> — no step data for %s. Activity skipped.",
	"tg_sleep_silence": "😴 <b>No sleep recorded</b> — last night with sleep data was %s ago.",
	"tg_dur_hours":     "%dh",
	"tg_dur_days":      "%d days",

	// Weekly data-quality digest
	"tg_digest_header":      "🔬 Weekly data quality",
	"tg_digest_clean":       "All clean — no anomalies in the last %d days.",
	"tg_digest_impossible":  "🚫 <b>Impossible values</b> (sensor errors)",
	"tg_digest_suspect":     "⚠️ <b>Suspect values</b> (>3σ from your baseline)",
	"tg_digest_missed":      "😴 <b>Nights with no sleep data</b>",
	"tg_digest_watch_off":   "🔕 <b>Watch off:</b> roughly %dh in this window",
	"tg_digest_more_in_ui":  "<i>Open the admin page for full per-row details.</i>",

	// Onboarding nudge — sent once per week while a tenant has ≥30 days
	// of complete daily_scores but <10 backfilled energy_snapshots.
	"tg_energy_backfill_nudge_header": "📊 Historical EnergyBank is waiting",
	"tg_energy_backfill_nudge_body":   "You have {complete} days of complete health history but only {backfilled} EnergyBank snapshots from retrospective backfill. Until you run it, the verdict thresholds for rest / moderate / push hard use generic defaults instead of your personal distribution — meaning recommendations are biased and Telegram reports under-tune to your real fitness.",
	"tg_energy_backfill_nudge_cta":    "Open Settings → Historical EnergyBank",

	// Monthly stress-validation nudge — fires once when the §4.5
	// rubric verdict transitions to "validated". Surfaces the
	// actionable signal that β may now be tuned per §6 Q3.
	"tg_stress_validation_header": "🎯 Stress-formula validated",
	"tg_stress_validation_body":   "The §4.5 four-channel rubric just returned <b>{verdict}</b> for your tenant. {reason} You may now flip <code>settings.energy.stress_drain_enabled</code> on if you want EnergyBank drain to react to sustained autonomic load.",
	"tg_stress_validation_cta":    "Open /admin → Stress validation",
}
