package storage

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"health-receiver/internal/health"
)

func TestSleepDurationComparisonSnapshots(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	t.Run("catalog window and minimum", func(t *testing.T) { checkSleepReferenceContract(t, db) })
	ctx := t.Context()
	date, _ := time.Parse(time.DateOnly, "2026-10-26")
	wake := date.Format(time.DateOnly)
	if comparison := db.GetSleepDurationComparison(ctx, wake); comparison != nil {
		t.Fatalf("default-off returned %+v", comparison)
	}
	if err := db.SaveSettings(map[string]string{sleepComparisonSetting: "true"}); err != nil {
		t.Fatal(err)
	}
	missing := db.GetSleepDurationComparison(ctx, wake)
	if missing.Status != health.SleepComparisonNoNight || missing.DurationMinutes != nil || missing.DeltaMinutes != nil {
		t.Fatalf("missing date reused another night: %+v", missing)
	}
	for i := range 10 {
		day := date.AddDate(0, 0, -i)
		if i == 2 || i == 8 {
			continue
		}
		night := health.CompletedNightSleep{
			WakeDate: day.Format(time.DateOnly), DurationHours: 7.5, Source: "Synthetic Watch",
			SourceEpoch: "sample", AlgorithmVersion: "v1", InputHash: day.Format(time.DateOnly),
			CaptureCompleteness: health.NightCaptureComplete, DurationAssessment: health.NightDurationPlausible,
			FinalizationState: health.NightFinalFinal, ClaimEligibility: health.NightClaimEligible, ObservedAt: day,
		}
		if i == 0 {
			night.DurationHours = 6.8
		}
		if i == 0 || i == 9 {
			night.FinalizationState, night.ClaimEligibility = health.NightFinalProvisional, health.NightClaimIneligible
		}
		if err := db.SaveCompletedNightSleep(ctx, night, nil); err != nil {
			t.Fatal(err)
		}
	}
	type chartRow struct {
		Scenario string `json:"scenario"`
		health.SleepDurationComparison
	}
	var export []chartRow
	collect := func(scenario string) {
		for i := 9; i >= 0; i-- {
			f := db.GetSleepDurationComparison(ctx, date.AddDate(0, 0, -i).Format(time.DateOnly))
			export = append(export, chartRow{Scenario: scenario, SleepDurationComparison: *f})
		}
	}
	six := db.GetSleepDurationComparison(ctx, wake)
	if six.Status != health.SleepComparisonInsufficient || *six.EligibleNights != 6 || six.DeltaMinutes != nil {
		t.Fatalf("threshold: %+v", six)
	}
	collect("Six comparable nights")
	oldest := date.AddDate(0, 0, -9).Format(time.DateOnly)
	if _, err := db.pool.Exec(ctx, `UPDATE completed_night_sleep SET finalization_state='final',claim_eligibility='eligible' WHERE wake_date=$1`, oldest); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	first := db.GetSleepDurationComparison(ctx, wake)
	t.Logf("enabled comparison read including tenant setting: %s", time.Since(started))
	if first.Status != health.SleepComparisonAvailable || *first.EligibleNights != 7 || *first.DurationMinutes != 408 ||
		*first.ReferenceMinutes != 450 || *first.DeltaMinutes != -42 || first.WindowStart != "2026-09-26" || first.WindowEndExclusive != wake ||
		first.Finalization != health.NightFinalProvisional || first.FactVersion == six.FactVersion {
		t.Fatalf("common fact: %+v", first)
	}
	collect("Seven comparable nights")
	if _, err := db.pool.Exec(ctx, "UPDATE completed_night_sleep SET duration_hours=8.5 WHERE wake_date=$1", oldest); err != nil {
		t.Fatal(err)
	}
	revised := db.GetSleepDurationComparison(ctx, wake)
	if *revised.DeltaMinutes != -51 || revised.FactVersion == first.FactVersion {
		t.Fatalf("historical correction did not change the fact: %+v", revised)
	}
	collect("Late correction")
	if _, err := db.pool.Exec(ctx, "UPDATE completed_night_sleep SET capture_completeness='partial' WHERE wake_date=$1", wake); err != nil {
		t.Fatal(err)
	}
	partial := db.GetSleepDurationComparison(ctx, wake)
	if partial.Status != health.SleepComparisonIneligible || partial.DurationMinutes == nil || partial.DeltaMinutes != nil || partial.ReferenceMinutes != nil {
		t.Fatalf("partial night: %+v", partial)
	}
	if output := os.Getenv("SLEEP_REFERENCE_CHART_EXPORT"); output != "" {
		body, err := json.MarshalIndent(export, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(output, append(body, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}

	// A temporary model failure degrades only the comparison. Verify the
	// migrator can restore it and the runtime rejects a drifted definition.
	if _, err := db.pool.Exec(ctx, "ALTER VIEW sleep_duration_reference_v1 RENAME TO broken_sleep_duration_reference"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.pool.Exec(ctx, "DROP VIEW broken_sleep_duration_reference"); err != nil {
			t.Error(err)
		}
		if err := db.MigrateSchemaContractContext(ctx); err != nil {
			t.Error(err)
		}
	}()
	if unavailable := db.GetSleepDurationComparison(ctx, wake); unavailable.Status != health.SleepComparisonUnavailable || unavailable.DeltaMinutes != nil {
		t.Fatalf("missing view: %+v", unavailable)
	}
	if err := db.VerifySchemaContractContext(ctx); err == nil {
		t.Fatal("schema contract accepted a missing view")
	}
}

// The catalog uses JSON-compatible YAML, like the embedded HealthKit ingress contract.
func checkSleepReferenceContract(t *testing.T, db *DB) {
	t.Helper()
	catalog, err := os.ReadFile("../../experiments/dbt_metrics/models/health_metric_catalog.yml")
	if err != nil {
		t.Fatal(err)
	}
	type rule struct {
		WindowDays    int `json:"window_days"`
		MinimumNights int `json:"minimum_nights"`
	}
	var document struct {
		Models []struct {
			Config struct {
				Meta struct {
					Catalog struct {
						Bindings map[string]rule `json:"dbt_bindings"`
					} `json:"health_catalog"`
				}
			}
		}
	}
	if err := json.Unmarshal(catalog, &document); err != nil {
		t.Fatal(err)
	}
	var spec rule
	for _, model := range document.Models {
		if binding, ok := model.Config.Meta.Catalog.Bindings["sleep_duration_reference_v1"]; ok {
			spec = binding
			break
		}
	}
	if spec.MinimumNights < 2 || spec.WindowDays < spec.MinimumNights {
		t.Fatalf("invalid sleep reference binding: %+v", spec)
	}

	// The target and the night just before the window must never contribute.
	ctx := t.Context()
	target := time.Date(2015, 7, 1, 0, 0, 0, 0, time.UTC)
	wake := target.Format(time.DateOnly)
	save := func(daysAgo int, hours float64) {
		t.Helper()
		day := target.AddDate(0, 0, -daysAgo)
		night := health.CompletedNightSleep{
			WakeDate: day.Format(time.DateOnly), DurationHours: hours, Source: "synthetic",
			SourceEpoch: "contract", AlgorithmVersion: "v1", InputHash: day.Format(time.DateOnly),
			CaptureCompleteness: health.NightCaptureComplete, DurationAssessment: health.NightDurationPlausible,
			FinalizationState: health.NightFinalFinal, ClaimEligibility: health.NightClaimEligible, ObservedAt: day,
		}
		if err := db.SaveCompletedNightSleep(ctx, night, nil); err != nil {
			t.Fatal(err)
		}
	}
	save(0, 6)
	save(spec.WindowDays+1, 10)
	for i := 1; i < spec.MinimumNights; i++ {
		save(i, 8)
	}
	check := func(expected int, available bool) {
		t.Helper()
		var start, end string
		var count int
		var mean *float64
		err := db.pool.QueryRow(ctx, `SELECT window_start,window_end_exclusive,eligible_nights,reference_hours
			FROM sleep_duration_reference_v1 WHERE wake_date=$1`, wake).Scan(&start, &end, &count, &mean)
		if err != nil {
			t.Fatal(err)
		}
		if start != target.AddDate(0, 0, -spec.WindowDays).Format(time.DateOnly) || end != wake ||
			count != expected || (mean != nil) != available || (mean != nil && *mean != 8) {
			t.Fatalf("catalog %+v: got [%s,%s), count=%d, mean=%v; want count=%d, available=%t", spec, start, end, count, mean, expected, available)
		}
	}
	check(spec.MinimumNights-1, false)
	save(spec.WindowDays, 8)
	check(spec.MinimumNights, true)
}
