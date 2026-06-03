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
	// NightSleep and Nap are written by the iOS client (health-sync) when
	// it can decompose sessions into one main night vs. naps. Only the
	// most-recent day is consumed today (dashboard sleep card override +
	// nap badge); slice form is kept consistent with the other sleep
	// fields so future trend uses fall in naturally. Empty when the iOS
	// app hasn't synced new-format data yet — caller must fall back to
	// `Sleep` in that case.
	NightSleep []float64
	Nap        []float64
	// NapToday is today's nap_total only (0 when no nap today). Separate
	// from Nap[0] because the slice filters qty>0 — the latest entry can
	// be from any prior day someone napped, which would mis-attribute the
	// dashboard nap badge to today.
	NapToday  float64
	Steps     []float64
	Cal       []float64
	Exercise  []float64
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

	// SleepRegularityIndex (Phillips & Czeisler 2017) over a rolling 14-day
	// window of minute-level sleep state. Range 0–100 (negative theoretically
	// possible but practically unseen). Higher = more consistent sleep/wake
	// times. Nil when the user does not yet have ≥7 days of per-segment
	// sleep data (HAE midnight-summary nights cannot drive this — only iOS
	// per-segment pushes can — see Todoist 6gXg6hFjPwmJXchf for the iOS
	// task). Tier mapping per UK Biobank 2025 (ref 33 in SCORING.md): >75
	// green, 50–75 amber, <50 red.
	SleepRegularityIndex *float64
	// SleepRegularityNights is the number of distinct calendar days that
	// contributed minute-level state to the SRI computation. Surfaced so
	// the section detail can render "(n=12)" when partial.
	SleepRegularityNights int
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
	Key    string `json:"key"`
	Title  string `json:"title"`
	Icon   string `json:"icon"`
	Status string `json:"status"` // "good", "fair", "low"
	// StatusLabel is the localized rendering of Status for clients
	// (especially iOS) that should not maintain a parallel i18n table
	// for server enums. Populated by EnrichLabels at briefing time.
	StatusLabel string           `json:"status_label,omitempty"`
	Summary     string           `json:"summary"`
	Details     []BriefingDetail `json:"details"`
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
	Source      string  `json:"source"`
	Total       float64 `json:"total"`
	Deep        float64 `json:"deep"`
	REM         float64 `json:"rem"`
	Core        float64 `json:"core"`
	Unspecified float64 `json:"unspecified,omitempty"`
	Awake       float64 `json:"awake"`
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
	Name   string `json:"name"`
	Metric string `json:"metric"`
	Value  string `json:"value"`
	Unit   string `json:"unit"`
	// Badge is an optional small annotation rendered next to the value —
	// e.g. "+45m nap" on the sleep card when the user napped today.
	// Empty string means no badge.
	Badge string `json:"badge,omitempty"`
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
// Band is the same stable enum key used on BriefingResponse — exposed
// here too so consumers of /api/readiness-history (and the MCP
// `get_readiness_history` tool) don't have to reapply the threshold
// logic locally. Per-call cost is negligible (one ReadinessBand()
// switch per row), and it keeps the "single source of truth" property
// from drifting between endpoints.
type ReadinessPoint struct {
	Date  string `json:"date"`
	Score int    `json:"score"`
	Band  string `json:"band,omitempty"`
}

// Alert is a health anomaly notification (not a score component).
type Alert struct {
	Text     string `json:"text"`
	Severity string `json:"severity"` // "warning", "critical"
	Metric   string `json:"metric"`   // "respiratory_rate", "wrist_temperature", "hrv_cv"
}

// IllnessSuspicion is an experimental, evidence-bound signal for acute
// same-day patterns that are consistent with illness or respiratory stress.
// It is not a diagnosis and must not drive EnergyBank verdict overrides.
type IllnessSuspicion struct {
	Date         string                  `json:"date"`
	Confidence   string                  `json:"confidence"` // none | low | moderate | high
	Reason       string                  `json:"reason"`
	Experimental bool                    `json:"experimental"`
	Signals      []IllnessEvidenceSignal `json:"signals,omitempty"`
	StressFlags  []string                `json:"stress_flags,omitempty"`
}

// IllnessEvidenceSignal is a single auditable contributor to
// IllnessSuspicion. Missing/warmup channels can appear here with Status set
// so clients do not mistake absent evidence for normal evidence.
type IllnessEvidenceSignal struct {
	Metric          string               `json:"metric"`
	Kind            string               `json:"kind"`      // metric | spo2_cluster | stress_flag | subjective
	Role            string               `json:"role"`      // primary | support | context
	Category        string               `json:"category"`  // respiratory | oxygen | autonomic | temperature | sleep | subjective | stress_flag
	Direction       string               `json:"direction"` // high | low | present | missing
	Strength        string               `json:"strength"`  // weak | mild | strong | missing | warmup
	Status          string               `json:"status"`    // ok | missing | warmup | confounded
	Contributes     bool                 `json:"contributes"`
	Value           *float64             `json:"value,omitempty"`
	Baseline        *float64             `json:"baseline,omitempty"`
	DeltaAbs        *float64             `json:"delta_abs,omitempty"`
	ZScore          *float64             `json:"z_score,omitempty"`
	Unit            string               `json:"unit,omitempty"`
	Method          string               `json:"method,omitempty"`
	ActivityContext string               `json:"activity_context,omitempty"` // normal | high | unknown
	Evidence        string               `json:"evidence"`
	Sources         []SpO2SourceEvidence `json:"sources,omitempty"`
}

// IllnessEvidenceInput is the date-aligned input shape for
// ComputeIllnessSuspicion. Storage owns constructing this for the evaluated
// local date; callers must not infer acute evidence from compressed
// RawMetrics slice indexes.
type IllnessEvidenceInput struct {
	Date                 string
	RespiratoryRate      *MetricEvidenceInput
	SpO2Average          *MetricEvidenceInput
	SpO2LowCluster       *SpO2ClusterEvidence
	RHR                  *MetricEvidenceInput
	HRV                  *MetricEvidenceInput
	SleepDisruption      *MetricEvidenceInput
	SustainedHRLoad      *MetricEvidenceInput
	WristTempDeviation   *MetricEvidenceInput
	StressFlags          []string
	SubjectiveCheckin    *SubjectiveCheckinSummary
	ObjectivePatternDays int
}

type MetricEvidenceInput struct {
	Metric          string
	Value           float64
	Baseline        float64
	ZScore          float64
	Unit            string
	Method          string
	Status          string // ok | missing | warmup
	ActivityContext string // normal | high | unknown; used by sustained_hr_load
}

type SpO2ClusterEvidence struct {
	Status        string // ok | missing | warmup
	ValidReadings int
	Below94Count  int
	Below92Count  int
	Min           float64
	Avg           float64
	Sources       []SpO2SourceEvidence
}

type SpO2SourceEvidence struct {
	Source       string  `json:"source"`
	Count        int     `json:"count"`
	Below94Count int     `json:"below_94_count"`
	Below92Count int     `json:"below_92_count"`
	Min          float64 `json:"min"`
	Avg          float64 `json:"avg"`
	Window       string  `json:"window,omitempty"` // sleep | daytime | mixed | unknown
}

// HeadlineSignal is the most notable cross-metric signal of the day.
// Surfaced at the top of the briefing so a single insight doesn't get
// buried among "all good" section cards. Built on the converging-evidence
// principle (Meeusen 2013, Plews 2014, MDPI HF 2025): single-metric "good"
// in the presence of multiple stress signals is clinically misleading.
type HeadlineSignal struct {
	// Semantic key for UI: "stress", "sleep_debt", "elevated_rhr",
	// "depressed_hrv", "good_recovery", "stable" (or empty if no headline).
	Key      string                `json:"key"`
	Severity string                `json:"severity"`          // "warning", "info", "positive"
	Title    string                `json:"title"`             // short one-liner
	Detail   string                `json:"detail"`            // 1-2 sentence explanation citing concrete numbers
	Metrics  []HeadlineMetricDelta `json:"metrics,omitempty"` // contributing deltas
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
	Capacity      int    `json:"capacity"`       // 0-100, set at briefing time
	Current       int    `json:"current"`        // 0-100, capacity - drain
	DrainSoFar    int    `json:"drain_so_far"`   // 0-100, total accumulated drain
	Strain        int    `json:"strain"`         // 0-100, ACWR-flavoured activity load
	Stress        int    `json:"stress"`         // 0-100, autonomic z-score deviation
	ActionVerdict string `json:"action_verdict"` // enum: push_hard|moderate|active_recovery|rest
	// VerdictLabel is the localized short rendering of ActionVerdict.
	// Populated by EnrichLabels so iOS / other consumers don't need a
	// parallel i18n map for energy_verdict_<verdict>.
	VerdictLabel string `json:"verdict_label,omitempty"`
	// VerdictSeverity is the closed-vocabulary classification of the
	// verdict (one of: critical|warning|info|neutral|pending|good).
	// Same vocabulary as FlagDetail.Severity so iOS uses a single
	// severity → DS token map across both surfaces. Empty when the
	// verdict has no mapping (treated as neutral by clients).
	VerdictSeverity string                `json:"verdict_severity,omitempty"`
	VerdictReason   string                `json:"verdict_reason"` // localised one-sentence rationale
	Components      []EnergyBankComponent `json:"components,omitempty"`
	// HRVZRaw is today's HRV z-score against personal baseline (negative =
	// vagal depression = parasympathetic underactivity). Exposed because
	// downstream consumers — the storage-layer v2 override block, the AI
	// prompt path — re-evaluate the verdict on different bank inputs
	// (v1 current vs v2 bank) and need the same HRV gate the energy
	// kernel used. Persisting it on the struct keeps "what HRV state
	// drove this verdict" auditable instead of recomputing it from raw
	// HRV in every caller.
	HRVZRaw float64 `json:"hrv_z_raw"`
	// Flags is the v2.0 imputed-data set plus the §4.3 v2.2 stress
	// flags computed by the storage orchestrator. Currently populated:
	//   - imputed_sleep        (v2.0)
	//   - imputed_activity     (v2.0)
	//   - stale_stress         (§0 blocker 3 — HR coverage < 8h awake, day ended)
	//   - data_accruing        (§0 blocker 3 — HR coverage < 8h awake, day still in progress)
	//   - calibration_warmup   (§4.1 — PersonalBaseline still in 3-6 sample range)
	//   - acute_stress             (§4.3 — any hour z>+2 in awake window)
	//   - sustained_load           (§4.3 — ≥4h consecutive z>+1)
	//   - illness_signature        (§4.3 — temp ↑ AND resp ↑ AND HRV ↓, each >1 SD)
	//   - recovery_debt            (§4.3 — overnight HRV ↓ >1 SD AND overnight RHR ↑ >0.5 SD)
	//   - parasympathetic_rebound  (§4.3 — daily HR ↑ >1 SD AND HRV ABOVE baseline >1 SD)
	//
	// Hero UI renders this list as a single colour-coded indicator
	// with expand-on-tap detail; AI verdict layer reads it to override
	// push_hard recommendation on illness_signature.
	Flags []string `json:"flags,omitempty"`
	// FlagDetails is the localized rendering of Flags for consumers that
	// shouldn't maintain a parallel i18n table. One entry per flag; the
	// stable Key matches the corresponding entry in Flags. Label and
	// Description are empty strings for flags without a localization
	// (e.g. imputed_* or future server-only flags) so clients can
	// detect "no localization yet" without rendering a missing tile.
	FlagDetails []FlagDetail `json:"flag_details,omitempty"`
}

// FlagDetail carries the stable key alongside its localized rendering so
// downstream UIs can render a chip without needing a parallel i18n map.
// Built by EnrichLabels from the EnergyBank.Flags list; consumers that
// need raw enum values keep reading Flags.
type FlagDetail struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	// Severity is a closed-vocabulary classification (one of:
	// critical|warning|info|neutral|pending|good) so iOS maps
	// severity → DS colour token instead of key → token. A new
	// server-side flag automatically renders with the correct
	// visual emphasis without an iOS update. Empty when the flag
	// has no mapping yet (treated as default neutral by clients).
	Severity string `json:"severity,omitempty"`
}

// VerdictBands holds the per-user calibrated thresholds for translating
// the EnergyBank "current" (v1) or "bank" (v2) score into an action
// verdict. Personal bands are derived from the user's own
// energy_snapshots percentile distribution once enough data exists;
// before then the formula falls back to documented defaults derived
// from a single reference user's 459-day distribution (see
// DefaultV2VerdictBands).
//
// Source records the broad calibration family for backward-compatible
// clients. CalibrationMode carries the precise mode so mixed-formula
// warmup and default warmup are not hidden behind the legacy "personal"
// / "default" labels. NDataPoints is retained for compatibility and
// means distinct eligible local dates used for returned non-default
// bands, never raw snapshot rows. In default modes it stays zero; use
// LatestFormulaDays and CompatibleFormulaDays for warmup diagnostics.
type VerdictBands struct {
	Rest                  int    `json:"rest"`                    // ≤ this → rest
	Recovery              int    `json:"recovery"`                // ≤ this → active_recovery
	PushHard              int    `json:"push_hard"`               // ≥ this AND HRV gate → push_hard
	Source                string `json:"source"`                  // "personal" | "default" | "manual"
	NDataPoints           int    `json:"n_data_points"`           // distinct eligible dates used for non-default bands
	CalibrationMode       string `json:"calibration_mode"`        // personal_latest_formula | personal_mixed_formula_warmup | provisional_compatible_formula_warmup | default_warmup | default
	UsedDays              int    `json:"used_days"`               // distinct eligible dates used for returned non-default bands
	LatestFormulaDays     int    `json:"latest_formula_days"`     // distinct eligible dates for current formula
	CompatibleFormulaDays int    `json:"compatible_formula_days"` // distinct eligible dates after compatibility whitelist
}

// Level buckets Current into critical/low/medium/good so the dashboard can
// pick a colour and a plain-language explanation without duplicating the
// thresholds in every template. Aligned with the readiness palette
// (good ≥70, medium ≥40) and splits the low band at 20 so a near-empty tank
// renders red while a merely "low" reading stays amber.
func (e *EnergyBank) Level() string {
	if e == nil {
		return ""
	}
	switch {
	case e.Current >= 70:
		return "good"
	case e.Current >= 40:
		return "medium"
	case e.Current >= 20:
		return "low"
	default:
		return "critical"
	}
}

// EnergyBankComponent breaks down the capacity/strain/stress numbers so the
// dashboard and AI briefing can show *why* the verdict is what it is.
type EnergyBankComponent struct {
	Name  string `json:"name"` // morning_capacity | activity_load | autonomic_stress
	Value int    `json:"value"`
	Note  string `json:"note"` // free-form provenance ("steps 8200 vs 28d avg 7400")
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
	Date           string            `json:"date"`
	Greeting       string            `json:"greeting"`
	Overall        string            `json:"overall"` // "good", "fair", "low"
	Headline       *HeadlineSignal   `json:"headline,omitempty"`
	Sections       []BriefingSection `json:"sections"`
	Highlights     []BriefingDetail  `json:"highlights"`
	ReadinessScore int               `json:"readiness_score"` // 7-day sliding window
	// ReadinessBand is the stable enum key ("optimal" / "fair" / "low")
	// derived from ReadinessScore via the canonical ReadinessBand()
	// helper. Surfaced server-side so iOS / other consumers don't
	// reimplement the thresholds locally (which drift — see issue #83
	// item #4). ReadinessLabel is the language-specific rendering of
	// the same band; clients pick one or both.
	ReadinessBand  string `json:"readiness_band"`
	ReadinessLabel string `json:"readiness_label"`
	ReadinessTip   string `json:"readiness_tip"`
	RecoveryPct    int    `json:"recovery_pct"`
	ReadinessToday int    `json:"readiness_today"` // today only vs baseline
	// ReadinessTodayBand mirrors ReadinessBand for the today-only score.
	ReadinessTodayBand  string             `json:"readiness_today_band"`
	ReadinessTodayLabel string             `json:"readiness_today_label"`
	Correlation         []CorrelationPoint `json:"correlation"`
	Insights            []Insight          `json:"insights"`
	Alerts              []Alert            `json:"alerts,omitempty"`
	Sleep               *SleepAnalysis     `json:"sleep"`
	MetricCards         []MetricCard       `json:"metric_cards"`
	EnergyBank          *EnergyBank        `json:"energy_bank,omitempty"`
	IllnessSuspicion    *IllnessSuspicion  `json:"illness_suspicion,omitempty"`
	// SubjectiveCheckin is the morning self-report (Telegram one-tap).
	// Populated from subjective_checkins when a row exists for today
	// in the tenant's REPORT_TZ. nil when no row — dashboard renders
	// nothing in that case (silence-is-good convention matches the
	// stress-flags row).
	SubjectiveCheckin *SubjectiveCheckinSummary `json:"subjective_checkin,omitempty"`
	// AIInsight is the Gemini-generated narrative cached in `ai_briefings`.
	// Populated by the API handler (not by GetHealthBriefing), so it stays
	// optional and doesn't pollute internal-use callers of BriefingResponse.
	AIInsight string `json:"ai_insight,omitempty"`
}

// SubjectiveCheckinSummary is the read-shape rendered into the
// dashboard hero. nil when no check-in row exists for today.
type SubjectiveCheckinSummary struct {
	Status string `json:"status"`           // prompted | answered | expired | late_answered
	Answer string `json:"answer,omitempty"` // great | ok | meh | sick (empty when status=prompted/expired)
}
