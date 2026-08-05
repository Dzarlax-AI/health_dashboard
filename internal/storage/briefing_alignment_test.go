package storage

import (
	"testing"

	"health-receiver/internal/health"
)

func TestDailyHealthMetricsKeepsMissingValuesOnTheirCalendarDate(t *testing.T) {
	hrvYesterday := 38.0
	sleepToday := 7.0
	stepsToday := 1864.0
	row := dailyHealthMetrics(dailyScoreRow{
		date:  "2026-08-04",
		hrv:   nil,
		slp:   &sleepToday,
		steps: &stepsToday,
	})
	yesterday := dailyHealthMetrics(dailyScoreRow{
		date: "2026-08-03",
		hrv:  &hrvYesterday,
	})

	if row.Date != "2026-08-04" || row.HRV != nil {
		t.Fatalf("today = %#v, want date with missing HRV preserved", row)
	}
	if row.Sleep == nil || *row.Sleep != 7.0 {
		t.Fatalf("today sleep = %v, want 7.0", row.Sleep)
	}
	if yesterday.Date != "2026-08-03" || yesterday.HRV == nil || *yesterday.HRV != 38.0 {
		t.Fatalf("yesterday = %#v, want dated HRV 38.0", yesterday)
	}
}

func TestMergeDatedMetricsAlignsSparseMetricSeries(t *testing.T) {
	got := mergeDatedMetrics(map[string][]health.DatedValue{
		"hrv": {
			{Date: "2026-08-03", Val: 38},
			{Date: "2026-08-02", Val: 42},
		},
		"sleep": {
			{Date: "2026-08-04", Val: 7},
			{Date: "2026-08-02", Val: 6.5},
		},
		"steps": {
			{Date: "2026-08-04", Val: 1864},
		},
	})

	if len(got) != 3 {
		t.Fatalf("len = %d, want 3: %#v", len(got), got)
	}
	if got[0].Date != "2026-08-04" || got[0].HRV != nil || got[0].Sleep == nil || got[0].Steps == nil {
		t.Fatalf("latest row = %#v, want sleep+steps with missing HRV", got[0])
	}
	if got[1].Date != "2026-08-03" || got[1].HRV == nil || got[1].Sleep != nil {
		t.Fatalf("middle row = %#v, want HRV-only day", got[1])
	}
	if got[2].Date != "2026-08-02" || got[2].HRV == nil || got[2].Sleep == nil {
		t.Fatalf("oldest row = %#v, want aligned HRV+sleep", got[2])
	}
}

func TestFallbackDailyMetricsPreserveMeasuredZeroAwake(t *testing.T) {
	if got := dailyMetricQuantityPredicate("sleep_awake"); got != "qty >= 0" {
		t.Fatalf("sleep_awake predicate = %q, want zero-inclusive predicate", got)
	}
	if got := dailyMetricQuantityPredicate("sleep_total"); got != "qty > 0" {
		t.Fatalf("sleep_total predicate = %q, want positive-only predicate", got)
	}
	got := mergeDatedMetrics(map[string][]health.DatedValue{
		"awake": {{Date: "2026-08-04", Val: 0}},
	})
	if len(got) != 1 || got[0].Awake == nil || *got[0].Awake != 0 {
		t.Fatalf("daily awake = %#v, want measured zero", got)
	}
}
