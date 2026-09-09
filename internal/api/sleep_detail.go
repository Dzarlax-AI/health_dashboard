package api

import "health-receiver/internal/storage"

type MetricDataResponse struct {
	Metric         string                     `json:"metric"`
	Bucket         string                     `json:"bucket"`
	Agg            string                     `json:"agg"`
	BySource       bool                       `json:"by_source,omitempty"`
	Points         []storage.DataPoint        `json:"points,omitempty"`
	PointsBySource []storage.SourceDataPoints `json:"points_by_source,omitempty"`
}

type MetricRangeResponse struct {
	Min string `json:"min"`
	Max string `json:"max"`
}

type SectionDetail struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Trend string `json:"trend"`
	Note  string `json:"note,omitempty"`
}

type SectionChart struct {
	Metric    string `json:"metric,omitempty"`
	Agg       string `json:"agg,omitempty"`
	Label     string `json:"label"`
	Unit      string `json:"unit,omitempty"`
	Color     string `json:"color,omitempty"`
	ColorDark string `json:"color_dark,omitempty"`
	Type      string `json:"type,omitempty"`
	Stacked   bool   `json:"stacked,omitempty"`
	Virtual   bool   `json:"virtual,omitempty"`
}

type SectionExplain struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type SectionResponse struct {
	Key      string           `json:"key"`
	Title    string           `json:"title"`
	Summary  string           `json:"summary"`
	Details  []SectionDetail  `json:"details"`
	Charts   []SectionChart   `json:"charts"`
	Explains []SectionExplain `json:"explains"`
}
