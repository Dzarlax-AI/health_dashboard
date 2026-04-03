package health

var en = LangStrings{
	// Readiness
	"readiness_optimal": "Optimal",
	"readiness_fair":    "Fair",
	"readiness_low":     "Low",
	"tip_optimal":       "Great day for a challenging workout or important tasks.",
	"tip_fair":          "Some deviation from your norm. Moderate activity is a good choice.",
	"tip_low":           "Focus on recovery: hydrate, rest, and avoid intense exercise.",

	// Section titles
	"sec_recovery": "Recovery",
	"sec_sleep":    "Sleep",
	"sec_activity": "Activity",
	"sec_cardio":   "Heart & Lungs",

	// Detail labels
	"lbl_hrv":        "HRV",
	"lbl_vs_avg":     "vs avg",
	"lbl_resting_hr": "Resting HR",
	"lbl_duration":   "Duration",
	"lbl_deep_sleep": "Deep sleep",
	"lbl_rem":        "REM",
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
	"rec_summary_good": "You're well recovered. Your body's ready for activity.",
	"rec_summary_fair": "Recovery is moderate. Listen to your body today.",
	"rec_summary_low":  "Your body needs more rest. Take it easy if you can.",

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
}
