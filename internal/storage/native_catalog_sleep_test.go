package storage

import (
	"testing"
	"time"

	"health-receiver/internal/health"
)

func checkNativeSleepReference(t *testing.T, db *DB) {
	t.Helper()
	ctx := t.Context()
	anchor := time.Date(2024, 9, 16, 12, 0, 0, 0, time.UTC)
	save := func(offset int, hours float64) {
		t.Helper()
		day := anchor.AddDate(0, 0, offset)
		night := health.CompletedNightSleep{
			WakeDate: day.Format(time.DateOnly), DurationHours: hours, Source: "Fixture Watch",
			SourceEpoch: "native-probe", AlgorithmVersion: "v1", InputHash: day.String() + time.Duration(hours*float64(time.Hour)).String(),
			CaptureCompleteness: health.NightCaptureComplete, DurationAssessment: health.NightDurationPlausible,
			FinalizationState: health.NightFinalFinal, ClaimEligibility: health.NightClaimEligible, ObservedAt: day,
		}
		if err := db.SaveCompletedNightSleep(ctx, night, nil); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want string) {
		t.Helper()
		day := anchor.Format(time.DateOnly)
		page, err := db.ReadCatalogMetricPage(ctx, CatalogMetricRequest{MetricID: "sleep_comparison.delta_minutes", FromDate: day, ToDate: day, DateField: "wake_date", Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if want == "" {
			if len(page.Samples) != 0 {
				t.Fatalf("disabled comparison: %v", page)
			}
			return
		}
		if len(page.Samples) != 1 || string(page.Samples[0].Value) != want {
			t.Fatalf("delta: %v, want %s", page, want)
		}
	}
	for day := -10; day < 0; day++ {
		save(day, 7)
	}
	save(0, 6.5)
	check("")
	if err := db.SaveSettings(map[string]string{sleepComparisonSetting: "true"}); err != nil {
		t.Fatal(err)
	}
	check("-30")
	save(0, 7.5)
	check("30")
}
