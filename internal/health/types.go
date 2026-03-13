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
	SpO2     []float64
	VO2      []float64
	Resp     []float64
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

type SleepAnalysis struct {
	DeepAvg    float64 `json:"deep_avg"`
	REMAvg     float64 `json:"rem_avg"`
	AwakeAvg   float64 `json:"awake_avg"`
	Efficiency float64 `json:"efficiency"`
}

type MetricCard struct {
	Name       string  `json:"name"`
	Value      string  `json:"value"`
	Unit       string  `json:"unit"`
	TrendPct   float64 `json:"trend_pct"`
	TrendLabel string  `json:"trend_label"`
}

type BriefingResponse struct {
	Date           string             `json:"date"`
	Greeting       string             `json:"greeting"`
	Overall        string             `json:"overall"` // "good", "fair", "low"
	Sections       []BriefingSection  `json:"sections"`
	Highlights     []BriefingDetail   `json:"highlights"`
	ReadinessScore int                `json:"readiness_score"`
	ReadinessLabel string             `json:"readiness_label"`
	ReadinessTip   string             `json:"readiness_tip"`
	RecoveryPct    int                `json:"recovery_pct"`
	Correlation    []CorrelationPoint `json:"correlation"`
	Insights       []Insight          `json:"insights"`
	Sleep          *SleepAnalysis     `json:"sleep"`
	MetricCards    []MetricCard       `json:"metric_cards"`
}
