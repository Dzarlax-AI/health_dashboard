package api

import "encoding/json"

// Catalog samples preserve the JSON type of each value, including measured false and zero.
type CatalogSeriesResponse struct {
	Metric     string                `json:"metric"`
	Samples    []CatalogSeriesSample `json:"samples"`
	NextCursor string                `json:"next_cursor,omitempty"`
}

type CatalogSeriesSample struct {
	Status       string            `json:"value_status"`
	Disposition  string            `json:"disposition"`
	Reason       *string           `json:"reason,omitempty"`
	Value        json.RawMessage   `json:"value,omitempty"`
	NumericValue *string           `json:"numeric_value,omitempty"` // Exact decimal for clients without a decimal JSON type.
	BooleanValue *bool             `json:"boolean_value,omitempty"`
	TextValue    *string           `json:"text_value,omitempty"`
	Source       string            `json:"source,omitempty"`
	Dimensions   map[string]string `json:"dimensions,omitempty"`
	ObservedAt   *string           `json:"observed_at,omitempty"`
	AvailableAt  *string           `json:"available_at,omitempty"`
}
