package api

import "time"

type DerivedMetricValue struct {
	MetricName     string     `json:"metric_name"`
	MetricDate     string     `json:"metric_date"`
	ValueType      string     `json:"value_type"`
	ValueNumeric   *float64   `json:"value_numeric,omitempty"`
	ValueText      *string    `json:"value_text,omitempty"`
	ValueTimestamp *time.Time `json:"value_timestamp,omitempty"`
	ValueJSON      any        `json:"value_json,omitempty"`
	Unit           string     `json:"unit"`
	State          string     `json:"state"`
	FormulaVersion string     `json:"formula_version"`
	CalculatedAt   time.Time  `json:"calculated_at"`
	FinalizedAt    *time.Time `json:"finalized_at,omitempty"`
}

type DerivedMetricsResponse struct {
	Metric string               `json:"metric"`
	From   string               `json:"from"`
	To     string               `json:"to"`
	Values []DerivedMetricValue `json:"values"`
}
