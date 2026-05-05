package health

// RawMetrics holds pre-fetched time-series data for all health metrics.
// Values are ordered most-recent-first. All []float64 slices come from 30-day windows.
// StepsWithDates and HRVWithDates are from a 7-day window for the correlation chart.
type RawMetrics struct {
	LastDate string
	HRV      []float64
	RHR      []float64
	Sleep    []float64
	Deep     []float64
	REM      []float64
	Awake    []float64
	Steps    []float64
	Cal      []float64
	Exercise []float64
	SpO2      []float64
	VO2       []float64
	Resp      []float64
	WristTemp []float64
	// For correlation chart
	StepsWithDates []DatedValue
	HRVWithDates   []DatedValue

	// Intraday partial-day totals — used by Energy Bank to drain capacity in
	// proportion to today's accumulating activity. Read from hourly_metrics
	// where SUBSTRING(hour,1,10) = today, source-deduplicated.
	StepsToday        float64
	ActiveEnergyToday float64
	// Chronic 28-day averages (excluding today) — denominators for ACWR-style
	// load ratio (Gabbett 2016). Read from daily_scores.
	StepsChronic28d        float64
	ActiveEnergyChronic28d float64
}

// DatedValue is a single metric data point paired with its calendar date.
type DatedValue struct {
	Date string
	Val  float64
}

type BriefingDetail struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Note  string `json:"note"`
	Trend string `json:"trend"` // "up", "down", "stable"
}

type BriefingSection struct {
	Key     string           `json:"key"`
	Title   string           `json:"title"`
	Icon    string           `json:"icon"`
	Status  string           `json:"status"` // "good", "fair", "low"
	Summary string           `json:"summary"`
	Details []BriefingDetail `json:"details"`
}

type CorrelationPoint struct {
	Date string  `json:"date"`
	Load float64 `json:"load"`
	HRV  float64 `json:"hrv"`
}

type Insight struct {
	Text string `json:"text"`
	Type string `json:"type"` // "positive" or "warning"
}

type SleepSourceSummary struct {
	Source string  `json:"source"`
	Total  float64 `json:"total"`
	Deep   float64 `json:"deep"`
	REM    float64 `json:"rem"`
	Core   float64 `json:"core"`
	Awake  float64 `json:"awake"`
}

type SleepAnalysis struct {
	Nights     int                  `json:"nights"`
	TotalAvg   float64              `json:"total_avg"`
	DeepAvg    float64              `json:"deep_avg"`
	REMAvg     float64              `json:"rem_avg"`
	AwakeAvg   float64              `json:"awake_avg"`
	Efficiency float64              `json:"efficiency"`
	Sources    []SleepSourceSummary `json:"sources,omitempty"`
}

type MetricCard struct {
	Name       string  `json:"name"`
	Metric     string  `json:"metric"`
	Value      string  `json:"value"`
	Unit       string  `json:"unit"`
	// Existing single-baseline trend (vs full 30-day average) — kept for
	// backwards-compatibility with the current dashboard template.
	TrendPct    float64 `json:"trend_pct"`
	TrendLabel  string  `json:"trend_label"`
	TrendStatus string  `json:"trend_status"`
	// Bevel-style dual baseline view: short-term (7d) and long-term (30d)
	// deltas displayed side-by-side so users see both acute response and
	// longer-term drift (Altini 2021, Beattie 2024).
	Trend7dPct     float64 `json:"trend_7d_pct"`
	Trend7dLabel   string  `json:"trend_7d_label,omitempty"`
	Trend7dStatus  string  `json:"trend_7d_status,omitempty"`
	Trend30dPct    float64 `json:"trend_30d_pct"`
	Trend30dLabel  string  `json:"trend_30d_label,omitempty"`
	Trend30dStatus string  `json:"trend_30d_status,omitempty"`
}

// ReadinessPoint is a single historical readiness data point.
type ReadinessPoint struct {
	Date  string `json:"date"`
	Score int    `json:"score"`
}

// Alert is a health anomaly notification (not a score component).
type Alert struct {
	Text     string `json:"text"`
	Severity string `json:"severity"` // "warning", "critical"
	Metric   string `json:"metric"`   // "respiratory_rate", "wrist_temperature", "hrv_cv"
}

// HeadlineSignal is the most notable cross-metric signal of the day.
// Surfaced at the top of the briefing so a single insight doesn't get
// buried among "all good" section cards. Built on the converging-evidence
// principle (Meeusen 2013, Plews 2014, MDPI HF 2025): single-metric "good"
// in the presence of multiple stress signals is clinically misleading.
type HeadlineSignal struct {
	// Semantic key for UI: "stress", "sleep_debt", "elevated_rhr",
	// "depressed_hrv", "good_recovery", "stable" (or empty if no headline).
	Key      string                 `json:"key"`
	Severity string                 `json:"severity"` // "warning", "info", "positive"
	Title    string                 `json:"title"`    // short one-liner
	Detail   string                 `json:"detail"`   // 1-2 sentence explanation citing concrete numbers
	Metrics  []HeadlineMetricDelta  `json:"metrics,omitempty"` // contributing deltas
}

// EnergyBank is a Bevel-inspired prescriptive metric: the user's "energy
// budget" for the day. Capacity is set at briefing time from sleep + recovery
// scores, drains throughout the day from observed activity (ACWR-style ratio
// vs 28-day chronic) and autonomic stress (one-sided HRV/RHR z-scores), and
// maps to a plain-language action verdict via Plews-style HRV-guided thresholds.
//
// Headline answers "what's notable today?"; EnergyBank answers "what should
// you do?". Intentionally a separate field so the two signals stay legible.
type EnergyBank struct {
	Capacity      int                   `json:"capacity"`        // 0-100, set at briefing time
	Current       int                   `json:"current"`         // 0-100, capacity - drain
	DrainSoFar    int                   `json:"drain_so_far"`    // 0-100, total accumulated drain
	Strain        int                   `json:"strain"`          // 0-100, ACWR-flavoured activity load
	Stress        int                   `json:"stress"`          // 0-100, autonomic z-score deviation
	ActionVerdict string                `json:"action_verdict"`  // enum: push_hard|moderate|active_recovery|rest
	VerdictReason string                `json:"verdict_reason"`  // localised one-sentence rationale
	Components    []EnergyBankComponent `json:"components,omitempty"`
}

// EnergyBankComponent breaks down the capacity/strain/stress numbers so the
// dashboard and AI briefing can show *why* the verdict is what it is.
type EnergyBankComponent struct {
	Name  string `json:"name"`  // morning_capacity | activity_load | autonomic_stress
	Value int    `json:"value"`
	Note  string `json:"note"`  // free-form provenance ("steps 8200 vs 28d avg 7400")
}

// HeadlineMetricDelta carries the concrete number behind a headline:
// metric name, today's value, baseline, and the deviation expressed
// both as absolute and as z-score (preferred per Plews 2014, Dial 2025).
type HeadlineMetricDelta struct {
	Metric   string  `json:"metric"`
	Value    float64 `json:"value"`
	Baseline float64 `json:"baseline"`
	DeltaAbs float64 `json:"delta_abs"` // value - baseline, in metric's native unit
	DeltaPct float64 `json:"delta_pct"`
	ZScore   float64 `json:"z_score"`
	Unit     string  `json:"unit"`
}

type BriefingResponse struct {
	Date           string             `json:"date"`
	Greeting       string             `json:"greeting"`
	Overall        string             `json:"overall"` // "good", "fair", "low"
	Headline       *HeadlineSignal    `json:"headline,omitempty"`
	Sections       []BriefingSection  `json:"sections"`
	Highlights     []BriefingDetail   `json:"highlights"`
	ReadinessScore int                `json:"readiness_score"`      // 7-day sliding window
	ReadinessLabel string             `json:"readiness_label"`
	ReadinessTip   string             `json:"readiness_tip"`
	RecoveryPct    int                `json:"recovery_pct"`
	ReadinessToday int                `json:"readiness_today"`      // today only vs baseline
	ReadinessTodayLabel string        `json:"readiness_today_label"`
	Correlation    []CorrelationPoint `json:"correlation"`
	Insights       []Insight          `json:"insights"`
	Alerts         []Alert            `json:"alerts,omitempty"`
	Sleep          *SleepAnalysis     `json:"sleep"`
	MetricCards    []MetricCard       `json:"metric_cards"`
	EnergyBank     *EnergyBank        `json:"energy_bank,omitempty"`
}
