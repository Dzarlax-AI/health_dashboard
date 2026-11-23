// Command runtimefields audits catalog DTO paths and queries synthetic production calculations.
// Only values returned by production calculations are emitted.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	dbtmetrics "health-receiver/experiments/dbt_metrics"
	"health-receiver/internal/health"
)

type row struct {
	MetricID string            `json:"metric_id"`
	Key      map[string]string `json:"key"`
	Profile  string            `json:"profile"`
	Value    any               `json:"value"`
	Status   string            `json:"status"`
	Version  *string           `json:"version"`
	Source   string            `json:"source,omitempty"`
}

func main() {
	id := flag.String("metric", "", "metric ID (default: all)")
	flag.Parse()
	// Execute real calculations against synthetic inputs. Input DTOs are not counted as read evidence.
	base, err := time.Parse(time.DateOnly, "2026-10-26")
	must(err)
	coverages := make([]health.SleepBalanceCoverage, 0, 14)
	for i := 0; i < 14; i++ {
		coverages = append(coverages, health.SleepBalanceCoverage{WakeDate: base.AddDate(0, 0, -i).Format(time.DateOnly), CaptureState: health.SleepBalanceCoverageComplete, CoverageGeneration: "synthetic"})
	}
	balance, err := health.CalculateSleepDurationBalance("2026-10-26", []health.SleepGoal{{EffectiveDate: "2026-01-01", Hours: 8, Version: "synthetic"}}, coverages, nil, time.UTC)
	must(err)
	observed := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	start, end := observed.Add(-8*time.Hour), observed
	night, err := health.CanonicalizeCompletedNight(health.CanonicalNightInput{
		WakeDate: "2026-10-26", DurationHours: 7.5, Source: "synthetic watch", SourceEpoch: "synthetic", InputHash: "synthetic", AlgorithmVersion: "synthetic-v1", CaptureCompleteness: health.NightCaptureComplete, CoverageGeneration: "synthetic", CoveredIntervalStart: &start, CoveredIntervalEnd: &end, ObservedAt: observed,
	}, observed.Add(12*time.Hour), time.UTC)
	must(err)
	morning := health.SelectMorningReportSleep("2026-10-26", []health.CompletedNightSleep{night})
	total, deep, rem, awake := 7.5, 1.5, 1.5, 0.5
	quality := health.ComputeSleepQualityBreakdown(&total, &deep, &rem, &awake, health.ReadinessComponentEvidence{Present: true, Freshness: health.ReadinessFreshnessOK, Confidence: health.ReadinessConfidenceFinal})
	illness := health.ComputeIllnessSuspicion(health.IllnessEvidenceInput{Date: "2026-10-26", StressFlags: []string{"illness_signature"}})
	if len(illness.Signals) == 0 {
		panic("expected illness signal")
	}
	if len(balance.Periods) == 0 {
		panic("expected balance periods")
	}
	bands := health.DefaultV2VerdictBands()
	raw := health.RawMetrics{
		LastDate: "2026-10-26", Sleep: []float64{7.5, 7, 7.2, 7.4, 7.3, 7.1, 7.8, 7.5}, Deep: []float64{1.5, 1.4}, REM: []float64{1.5, 1.4}, Awake: []float64{0.5}, HRV: []float64{50, 48, 49, 51, 50, 48, 50, 49}, RHR: []float64{60, 61, 60, 59, 60, 61, 60, 60}, Steps: []float64{7000, 6500, 8000, 7500, 7000, 7500, 8000, 6000},
	}
	for i := 0; i < 20; i++ {
		raw.Sleep = append(raw.Sleep, 7.5)
		raw.HRV = append(raw.HRV, 50)
		raw.RHR = append(raw.RHR, 60)
	}
	brief := health.ComputeBriefing(raw, "en")
	if brief.Sleep == nil || brief.EnergyBank == nil || len(brief.MetricCards) == 0 {
		panic(fmt.Sprintf("briefing sleep=%v energy=%v cards=%d score=%d", brief.Sleep != nil, brief.EnergyBank != nil, len(brief.MetricCards), brief.ReadinessScore))
	}
	snapshot := health.BuildDailyInsightSnapshot(brief, "en")

	decision := health.BuildDailyDecision(brief)
	type capture struct {
		value   any
		source  string
		version *string
		key     map[string]string
	}
	dayKey := func(day string) map[string]string { return map[string]string{"date": day} }
	mainVersion := night.AlgorithmVersion
	captures := map[string]capture{
		"sleep_summary":     {brief.Sleep, "health.ComputeBriefing", nil, dayKey(raw.LastDate)},
		"energy_bank":       {brief.EnergyBank, "health.ComputeBriefing", nil, dayKey(raw.LastDate)},
		"trend_card":        {brief.MetricCards[0], "health.ComputeBriefing", nil, map[string]string{"date": raw.LastDate, "metric": brief.MetricCards[0].Metric}},
		"illness_evidence":  {illness.Signals[0], "health.ComputeIllnessSuspicion", nil, map[string]string{"date": illness.Date, "metric": illness.Signals[0].Metric, "kind": illness.Signals[0].Kind}},
		"sleep_balance_day": {balance.Periods[0], "health.CalculateSleepDurationBalance", nil, map[string]string{"wake_date": balance.WakeDate, "period_wake_date": balance.Periods[0].WakeDate}},
		"verdict_bands":     {bands, "health.DefaultV2VerdictBands", nil, map[string]string{"kind": "default_v2"}},
		"main_night":        {night, "health.CanonicalizeCompletedNight", &mainVersion, map[string]string{"wake_date": night.WakeDate}},
		"morning_sleep":     {morning, "health.SelectMorningReportSleep", nil, map[string]string{"report_date": morning.ReportDate, "wake_date": morning.Date}},
		"sleep_quality":     {quality, "health.ComputeSleepQualityBreakdown", nil, map[string]string{"wake_date": night.WakeDate}},
		"sleep_balance":     {balance, "health.CalculateSleepDurationBalance", nil, map[string]string{"wake_date": balance.WakeDate}},
		"today_decision":    {decision, "health.BuildDailyDecision", nil, map[string]string{"date": raw.LastDate, "decision_id": decision.ID}},
		"readiness":         {brief, "health.ComputeBriefing", nil, dayKey(raw.LastDate)},
	}
	if snapshot != nil && len(snapshot.Evidence) > 0 {
		version := snapshot.Version
		captures["today_evidence"] = capture{snapshot.Evidence[0], "health.BuildDailyInsightSnapshot", &version, map[string]string{"date": snapshot.Date, "evidence_id": snapshot.Evidence[0].ID}}
	}
	rows := []row{}
	for family, capture := range captures {
		values, err := dbtmetrics.ProjectFamilyValues(family, capture.value)
		must(err)
		for metric, value := range values {
			if *id != "" && *id != metric {
				continue
			}
			rows = append(rows, row{MetricID: metric, Key: capture.key, Profile: "synthetic", Value: value, Status: "captured", Version: capture.version, Source: capture.source})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].MetricID < rows[j].MetricID })
	found := len(rows) > 0
	if !found {
		fmt.Fprintln(os.Stderr, "unknown metric:", *id)
		os.Exit(2)
	}
	body, err := json.MarshalIndent(rows, "", "  ")
	must(err)
	body = append(body, '\n')
	if dir := os.Getenv("HEALTH_METRIC_AUDIT_DIR"); dir != "" {
		must(os.MkdirAll(dir, 0700))
		must(os.WriteFile(filepath.Join(dir, "runtimefields.json"), body, 0600))
	} else {
		_, err = os.Stdout.Write(body)
		must(err)
	}
}
func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
