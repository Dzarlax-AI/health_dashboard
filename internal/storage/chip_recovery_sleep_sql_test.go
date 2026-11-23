package storage

import (
	"context"
	"encoding/json"
	"maps"
	"testing"
	"time"

	"health-receiver/internal/health"
)

func TestChipDBTRecoverySleepFeatures(t *testing.T) {
	db := chipDB(t)
	ctx := context.Background()
	anchor := time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
	date := anchor.Format(isoDate)
	for _, scenario := range []string{"all_missing", "mixed", "epoch", "surplus"} {
		t.Run(scenario, func(t *testing.T) {
			rows := map[string]health.SleepRow{}
			efficiencies := map[string]health.SleepEfficiencyResult{}
			captures := map[string]health.SleepCaptureConfidenceResult{}
			for offset := -8; offset <= 1; offset++ {
				if scenario == "all_missing" || (scenario == "mixed" && offset == -3) {
					continue
				}
				day := anchor.AddDate(0, 0, offset).Format(isoDate)
				total := 8.25
				if scenario != "surplus" && offset == -2 {
					total = 2.5
				}
				row := health.SleepRow{Date: day, Total: &total, Core: ptrFloat(5), Awake: ptrFloat(.25)}
				rows[day] = row
				efficiencies[day] = health.ComputeSleepEfficiency(row)
				captures[day] = health.ComputeSleepCaptureConfidence(row)
			}
			epochStart := ""
			if scenario == "epoch" {
				epochStart = anchor.AddDate(0, 0, -2).Format(isoDate)
			}
			cache := &sourceEpochRunCache{byDate: map[string]sourceEpochResolution{date: {epochID: "captured-epoch", startDate: epochStart}}}
			if err := db.writeRecoveryStabilityRow(ctx, anchor, date, cache, rows, efficiencies, captures, SleepArchitectureFeatureFields{}, date); err != nil {
				t.Fatal(err)
			}
			var actual recoveryFeatures
			var savedEpoch string
			if err := db.pool.QueryRow(ctx, "select features,source_epoch from feature_snapshots where date=$1 and sub_score=$2", date, SubScoreRecoveryStability).Scan(&actual, &savedEpoch); err != nil {
				t.Fatal(err)
			}
			if savedEpoch != "captured-epoch" {
				t.Fatalf("source epoch %q", savedEpoch)
			}
			expected := recoveryFeatureContext(anchor, epochStart, rows, efficiencies, captures, SleepArchitectureFeatureFields{})
			wantDebt := map[string]float64{"all_missing": 52.5, "mixed": 11.25, "epoch": 36, "surplus": -5.25}[scenario]
			if actual.SleepDebt7dHours == nil || *actual.SleepDebt7dHours != wantDebt {
				t.Fatalf("weekly debt: got %v, want %v hours", actual.SleepDebt7dHours, wantDebt)
			}
			payload, err := json.Marshal(recoverySleepDays(anchor, rows, efficiencies, captures))
			if err != nil {
				t.Fatal(err)
			}
			chipExec(t, db, "insert into source_data.chip_recovery_sleep_input values ($1,$2::text::date,nullif($3,'')::date,$4)", scenario, date, epochStart, json.RawMessage(payload))
			var model recoveryFeatures
			if err := db.pool.QueryRow(ctx, "select debt_hours,mean_confidence,low_days,class_counts from dbt_output.chip_recovery_sleep_v1 where case_id=$1", scenario).Scan(&model.SleepDebt7dHours, &model.SleepCaptureMeanConfidence7d, &model.SleepCaptureLowDays7d, &model.SleepCaptureClassCounts7d); err != nil {
				t.Fatal(err)
			}
			for _, got := range []recoveryFeatures{actual, model} {
				if !chipValueEqual(got.SleepDebt7dHours, expected.SleepDebt7dHours) || !chipValueEqual(got.SleepCaptureMeanConfidence7d, expected.SleepCaptureMeanConfidence7d) || got.SleepCaptureLowDays7d != expected.SleepCaptureLowDays7d || !maps.Equal(got.SleepCaptureClassCounts7d, expected.SleepCaptureClassCounts7d) {
					t.Fatalf("weekly feature mismatch: got %+v, want %+v", got, expected)
				}
			}
		})
	}
	chipExec(t, db, `insert into source_data.chip_recovery_sleep_input values ('unknown',current_date,null,null),('incomplete',current_date,null,'[]')`)
	var n int
	if err := db.pool.QueryRow(ctx, `select count(*) from dbt_output.chip_recovery_sleep_v1 where case_id in ('unknown','incomplete')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("unavailable capture produced %d results: %v", n, err)
	}
}
