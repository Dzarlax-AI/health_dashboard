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
	TrendPct   float64 `json:"trend_pct"`
	TrendLabel string  `json:"trend_label"`
	TrendStatus string `json:"trend_status"`
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

type BriefingResponse struct {
	Date           string             `json:"date"`
	Greeting       string             `json:"greeting"`
	Overall        string             `json:"overall"` // "good", "fair", "low"
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
}
