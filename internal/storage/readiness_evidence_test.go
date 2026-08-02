package storage

import (
	"testing"

	"health-receiver/internal/health"
)

func TestBuildReadinessEvidence_DoesNotUsePriorDayAsSameDayRHR(t *testing.T) {
	rhr := 55.0
	latest := dailyScoreRow{
		date: "2026-06-03",
		rhr:  &rhr,
	}

	e := buildReadinessEvidence("2026-06-04", latest, nil)
	if e.RHR.Present {
		t.Fatalf("RHR present = true; prior-day RHR must not be treated as same-day evidence")
	}
	if e.RHR.SourceDate != "" {
		t.Fatalf("RHR source date = %q, want empty for missing same-day evidence", e.RHR.SourceDate)
	}
}

func TestBuildReadinessEvidence_UsesFreshSameDayCounts(t *testing.T) {
	hrv := 80.0
	rhr := 72.0
	sleep := 7.5
	fresh := &dayRow{
		hrv:  &hrv,
		rhr:  &rhr,
		slp:  &sleep,
		hrvN: 4,
		rhrN: 1,
	}

	e := buildReadinessEvidence("2026-06-04", dailyScoreRow{date: "2026-06-04"}, fresh)
	if !e.HRV.Present || e.HRV.SampleCount != 4 {
		t.Fatalf("HRV evidence = present %v samples %d, want present with 4 samples", e.HRV.Present, e.HRV.SampleCount)
	}
	if !e.RHR.Present || e.RHR.SourceDate != "2026-06-04" {
		t.Fatalf("RHR evidence = present %v source %q, want same-day present", e.RHR.Present, e.RHR.SourceDate)
	}
}

func TestBuildReadinessEvidence_HRVSampleThresholdsAreMonotonic(t *testing.T) {
	hrv := 80.0

	provisional := buildReadinessEvidence("2026-06-04", dailyScoreRow{date: "2026-06-04"}, &dayRow{hrv: &hrv, hrvN: health.MinUnalignedHRVSamplesForProvisionalUse})
	if provisional.HRV.Confidence != health.ReadinessConfidenceProvisional {
		t.Fatalf("HRV confidence at provisional threshold = %q", provisional.HRV.Confidence)
	}

	final := buildReadinessEvidence("2026-06-04", dailyScoreRow{date: "2026-06-04"}, &dayRow{hrv: &hrv, hrvN: health.MinSleepWindowHRVSamplesForFullConfidence})
	if final.HRV.Confidence != health.ReadinessConfidenceFinal {
		t.Fatalf("HRV confidence at full threshold = %q", final.HRV.Confidence)
	}
}

func TestBuildReadinessEvidence_SleepQualityRequiresDeepAndAwake(t *testing.T) {
	sleep := 7.5
	deep := 1.0
	latest := dailyScoreRow{
		date: "2026-06-04",
		slp:  &sleep,
		deep: &deep,
	}

	e := buildReadinessEvidence("2026-06-04", latest, nil)
	if e.SleepQuality.Present {
		t.Fatalf("sleep quality present = true; missing awake stage must remain missing evidence")
	}
	if e.SleepQuality.MissingReason != "missing_sleep_stage_details" {
		t.Fatalf("sleep quality missing reason = %q", e.SleepQuality.MissingReason)
	}
}

func TestBuildReadinessEvidence_InfersFilteredZeroAwakeForCompleteStagedNight(t *testing.T) {
	sleep := 7.5
	deep := 1.0
	rem := 1.5
	core := 5.0
	latest := dailyScoreRow{
		date: "2026-06-04",
		slp:  &sleep,
		deep: &deep,
		rem:  &rem,
		core: &core,
	}

	e := buildReadinessEvidence("2026-06-04", latest, nil)
	if !e.SleepAwake.Present || e.SleepAwake.Value == nil {
		t.Fatalf("awake evidence = %+v, want inferred measured zero", e.SleepAwake)
	}
	if *e.SleepAwake.Value != 0 {
		t.Fatalf("awake value = %v, want 0", *e.SleepAwake.Value)
	}
	if e.SleepAwake.SourceDate != "2026-06-04" {
		t.Fatalf("awake source date = %q, want same-night date", e.SleepAwake.SourceDate)
	}
	if !e.SleepQuality.Present {
		t.Fatalf("sleep quality = %+v, want complete staged evidence", e.SleepQuality)
	}
}

func TestBuildReadinessEvidence_SleepStagesStayDateAligned(t *testing.T) {
	sleep := 7.5
	deep := 1.0
	rem := 1.5
	core := 4.6
	awake := 0.4
	latest := dailyScoreRow{
		date:  "2026-06-03",
		slp:   &sleep,
		deep:  &deep,
		rem:   &rem,
		awake: &awake,
	}

	stale := buildReadinessEvidence("2026-06-04", latest, nil)
	for name, component := range map[string]health.ReadinessComponentEvidence{
		"duration": stale.SleepDuration,
		"deep":     stale.SleepDeep,
		"rem":      stale.SleepREM,
		"core":     stale.SleepCore,
		"awake":    stale.SleepAwake,
	} {
		if component.Present || component.SourceDate != "" {
			t.Fatalf("%s evidence treated prior-day stage as current: %+v", name, component)
		}
	}

	fresh := buildReadinessEvidence("2026-06-04", dailyScoreRow{date: "2026-06-04"}, &dayRow{
		slp: &sleep, deep: &deep, rem: &rem, core: &core, awake: &awake,
	})
	for name, component := range map[string]health.ReadinessComponentEvidence{
		"duration": fresh.SleepDuration,
		"deep":     fresh.SleepDeep,
		"rem":      fresh.SleepREM,
		"core":     fresh.SleepCore,
		"awake":    fresh.SleepAwake,
	} {
		if !component.Present || component.SourceDate != "2026-06-04" {
			t.Fatalf("%s same-day evidence = %+v", name, component)
		}
	}
	if fresh.SleepCore.Value == nil || *fresh.SleepCore.Value != core {
		t.Fatalf("core value = %v, want %v", fresh.SleepCore.Value, core)
	}
}
