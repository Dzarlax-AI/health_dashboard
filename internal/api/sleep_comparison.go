package api

import "health-receiver/internal/health"

type SleepDurationComparisonResponse struct {
	Comparison *health.SleepDurationComparison `json:"comparison" jsonschema:"nullable"`
}
