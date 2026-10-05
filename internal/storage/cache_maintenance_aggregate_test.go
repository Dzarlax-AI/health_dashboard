package storage

import (
	"strings"
	"testing"
)

func TestHistoricalAggregateQueriesAreDateBounded(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		want        string
		mustNotHave string
	}{
		{
			name: "hourly average",
			query: aggregateSQLWith("legacy_hourly_avg", map[string]string{
				"SLEEP_DEDUP": "",
				"FROM_CLAUSE": "AND SUBSTRING(date,1,10) = $2",
			}),
			want:        "AND SUBSTRING(date,1,10) = $2",
			mustNotHave: "SUBSTRING(date,1,10) >=",
		},
		{
			name: "hourly sum",
			query: aggregateSQLWith("legacy_hourly_sum", map[string]string{
				"SLEEP_DEDUP": "",
				"FROM_CLAUSE": "AND SUBSTRING(date,1,10) = $2",
			}),
			want:        "AND SUBSTRING(date,1,10) = $2",
			mustNotHave: "SUBSTRING(date,1,10) >=",
		},
		{
			name: "daily avg",
			query: aggregateSQLWith("legacy_daily_avg", map[string]string{
				"FROM_CLAUSE": "AND SUBSTRING(hour,1,10) = $2",
			}),
			want:        "AND SUBSTRING(hour,1,10) = $2",
			mustNotHave: "SUBSTRING(hour,1,10) >=",
		},
		{
			name: "daily sleep",
			query: aggregateSQLWith("legacy_daily_sleep", map[string]string{
				"FROM_CLAUSE":               "AND SUBSTRING(hour,1,10) = $1",
				"SLEEP_METRICS":             sqlStringList(sleepMetricNames),
				"SLEEP_TRADITIONAL_METRICS": sqlStringList(sleepTraditionalMetrics),
				"SLEEP_COARSE_METRICS":      sqlStringList(sleepCoarseMetrics),
				"SLEEP_PRIORITY_CASE":       sleepSourcePriorityCaseExpr("source"),
				"SLEEP_MIN_HOURS":           sqlPolicyNumber(sleepCrossValidationMinHours),
				"SLEEP_DIVERGENCE":          sqlPolicyNumber(sleepCrossValidationDivergence),
			}),
			want:        "AND SUBSTRING(hour,1,10) = $1",
			mustNotHave: "SUBSTRING(hour,1,10) >=",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(tt.query, tt.want) {
				t.Fatalf("bounded query missing %q", tt.want)
			}
			if strings.Contains(tt.query, tt.mustNotHave) {
				t.Fatalf("bounded query contains range-wide predicate %q", tt.mustNotHave)
			}
		})
	}
}

func TestValidateCacheMaintenanceDate(t *testing.T) {
	for _, date := range []string{"2026-10-05", "2000-02-29"} {
		if err := validateCacheMaintenanceDate(date); err != nil {
			t.Errorf("valid date %q rejected: %v", date, err)
		}
	}
	for _, date := range []string{"", "2026-2-05", "2026-02-30", "2026-10-05 trailing"} {
		if err := validateCacheMaintenanceDate(date); err == nil {
			t.Errorf("invalid date %q accepted", date)
		}
	}
}
