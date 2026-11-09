package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"
)

func TestMorningEvidenceUsesTodayRecoveryAndDatedYesterdayActivity(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	date := db.Today()
	day, _ := time.Parse("2006-01-02", date)
	yesterday := day.AddDate(0, 0, -1).Format("2006-01-02")
	steps, hrv := 6400.0, 43.6
	raw := &health.RawMetrics{LastDate: date, Daily: []health.DailyHealthMetrics{{Date: yesterday, Steps: &steps}, {Date: date, Steps: &steps}}, ReadinessEvidence: &health.ReadinessEvidenceInput{Date: date, HRV: health.ReadinessComponentEvidence{Metric: "heart_rate_variability", Present: true, Value: &hrv, EvaluatedDate: date, SourceDate: date, Freshness: health.ReadinessFreshnessOK, Confidence: health.ReadinessConfidenceFinal, SampleCount: health.MinSleepWindowHRVSamplesForFullConfidence}}}
	b := &health.BriefingResponse{Date: date, ReadinessToday: 84, ReadinessTodayBand: "optimal", ReadinessTodayLabel: "Optimal", ReadinessConfidence: health.ReadinessConfidenceFinal, ReadinessServing: &health.ReadinessServingState{Status: health.ReadinessServingFresh, Confidence: health.ReadinessConfidenceFinal}, RawMetrics: raw, Sections: []health.BriefingSection{{Key: "recovery", Summary: "Moderate weekly recovery.", Details: []health.BriefingDetail{{Label: "HRV", Value: "38 ms"}}}}}
	evidence, err := db.morningInsightEvidence(context.Background(), b, raw, date, "en")
	if err != nil {
		t.Fatal(err)
	}
	if evidence.NightSleep.YesterdayActivity == nil || evidence.NightSleep.YesterdayActivity.Date != yesterday || evidence.NightSleep.YesterdayActivity.Steps == nil || evidence.Daily[1].Steps != nil {
		t.Fatalf("yesterday's activity not isolated from today: %+v", evidence)
	}
	if len(evidence.Sections) != 1 || strings.Contains(evidence.Sections[0].Summary, "Moderate") || !strings.Contains(evidence.Sections[0].Summary, "84") || len(evidence.Sections[0].Details) != 1 || !strings.Contains(evidence.Sections[0].Details[0].Note, "43.6") {
		t.Fatalf("legacy weekly recovery reached morning provider: %+v", evidence.Sections)
	}
}

func TestMorningGenerationHashRejectsChangedNightAndConfig(t *testing.T) {
	cfg := AIConfig{Provider: "openai", Providers: map[string]AIProviderSettings{"openai": {APIKey: "synthetic-test-key"}}}
	_, resolved, fingerprint, err := morningGenerationConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Model == "" || resolved.MaxOutputTokens != ai.SynthesisMaxTokens || fingerprint.PromptRevision != ai.PromptRevision {
		t.Fatalf("defaults not applied: %#v", fingerprint)
	}
	briefing := &health.BriefingResponse{Date: "2026-09-30"}
	hours := 7.5
	evidence := health.MorningInsightEvidence{Date: "2026-09-30", NightSleep: &health.MorningReportSleep{ReportDate: "2026-09-30", Date: "2026-09-30", Hours: &hours, InputHash: "night-1"}}
	hash := morningBundleHash(evidence, briefing, fingerprint)
	full := map[string]*AIBlock{}
	for _, key := range ai.GeneratedBlockOrder {
		full[key] = &AIBlock{Text: "synthetic text", InputsHash: hash}
	}
	if !aiBundleCacheComplete(full, hash) {
		t.Fatal("fresh bundle rejected")
	}
	evidence.NightSleep.InputHash = "night-2"
	if aiBundleCacheComplete(full, morningBundleHash(evidence, briefing, fingerprint)) {
		t.Fatal("updated night accepted old cache")
	}
	evidence.NightSleep.InputHash = "night-1"
	for _, mutate := range []func(*ai.GenerationFingerprint){func(f *ai.GenerationFingerprint) { f.Model += "-changed" }, func(f *ai.GenerationFingerprint) { f.ReasoningEffort = "high" }, func(f *ai.GenerationFingerprint) { f.MaxOutputTokens-- }, func(f *ai.GenerationFingerprint) { f.PromptRevision += "-changed" }, func(f *ai.GenerationFingerprint) { f.Provider = "gemini" }} {
		changed := fingerprint
		mutate(&changed)
		if aiBundleCacheComplete(full, morningBundleHash(evidence, briefing, changed)) {
			t.Fatal("changed generation accepted old cache")
		}
	}
	legacyFingerprint := fingerprint
	legacyFingerprint.PromptRevision = "health-briefing-v4-sleep-night"
	legacyHash := morningBundleHash(evidence, briefing, legacyFingerprint)
	legacy := map[string]*AIBlock{}
	for _, key := range ai.GeneratedBlockOrder {
		legacy[key] = &AIBlock{Text: "legacy sleep prose", InputsHash: legacyHash}
	}
	if aiBundleCacheComplete(legacy, morningBundleHash(evidence, briefing, fingerprint)) {
		t.Fatal("legacy free-text sleep bundle accepted under the constrained preliminary-sleep fingerprint")
	}
}

func TestMorningReportEvidenceExactCacheAndLanguage(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	today := time.Now().UTC().Format("2006-01-02")
	if err := db.SaveSettings(map[string]string{"timezone": "UTC"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO hourly_metrics(metric_name,hour,source,avg_val,sample_count,min_val,max_val) VALUES('step_count',$1,'synthetic',10,1,10,10)`, today+" 08"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO daily_scores(date,sleep_total,hrv_avg,steps) VALUES($1,7.5,55,10)`, today); err != nil {
		t.Fatal(err)
	}
	night := health.CompletedNightSleep{WakeDate: today, DurationHours: 7.5, Source: "synthetic", SourceEpoch: "epoch", InputHash: "night-1", AlgorithmVersion: "v1", CaptureCompleteness: health.NightCaptureComplete, DurationAssessment: health.NightDurationPlausible, FinalizationState: health.NightFinalProvisional, ClaimEligibility: health.NightClaimIneligible, ObservedAt: time.Now().UTC()}
	if err := db.SaveCompletedNightSleep(ctx, night, nil); err != nil {
		t.Fatal(err)
	}
	briefing, err := db.GetHealthBriefing("ru")
	if err != nil {
		t.Fatal(err)
	}
	cfg := AIConfig{Provider: "openai", Providers: map[string]AIProviderSettings{"openai": {APIKey: "synthetic-test-key"}}}
	_, _, fingerprint, err := morningGenerationConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := db.morningInsightEvidence(ctx, briefing, db.GetRawMetrics(), today, "ru")
	if err != nil {
		t.Fatal(err)
	}
	blocks := map[string]string{}
	for _, key := range ai.GeneratedBlockOrder {
		blocks[key] = "synthetic " + key
	}
	if err := db.SaveAIBundle(today, "ru", blocks, morningBundleHash(evidence, briefing, fingerprint)); err != nil {
		t.Fatal(err)
	}
	// Re-read the real briefing as the sender does after generation.
	briefing, err = db.GetHealthBriefing("ru")
	if err != nil {
		t.Fatal(err)
	}
	sleep, got, err := db.MorningReportEvidence(ctx, cfg, "ru", today, briefing)
	if err != nil || sleep.Date != today || got[ai.BlockSleep] != "synthetic SLEEP" {
		t.Fatalf("fresh cache: sleep=%#v blocks=%v err=%v", sleep, got, err)
	}
	_, got, err = db.MorningReportEvidence(ctx, cfg, "en", today, briefing)
	if err != nil || len(got) != 0 {
		t.Fatalf("wrong-language cache: %v %v", got, err)
	}
	_, got, err = db.MorningReportEvidence(ctx, AIConfig{}, "ru", today, briefing)
	if err != nil || len(got) != 0 {
		t.Fatalf("disabled AI cache: %v %v", got, err)
	}
	night.InputHash = "night-2"
	night.DurationHours = 8
	if err := db.SaveCompletedNightSleep(ctx, night, nil); err != nil {
		t.Fatal(err)
	}
	sleep, got, err = db.MorningReportEvidence(ctx, cfg, "ru", today, briefing)
	if err != nil || len(got) != 0 || sleep.Hours == nil || *sleep.Hours != 8 {
		t.Fatalf("stale cache after update: %#v %v %v", sleep, got, err)
	}
}

func TestMorningReportEvidenceKeepsCanonicalSleepWithoutRawAggregates(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	today := "2026-09-30"
	night := health.CompletedNightSleep{WakeDate: today, DurationHours: 7.5, Source: "synthetic", SourceEpoch: "epoch", InputHash: "night-1", AlgorithmVersion: "v1", CaptureCompleteness: health.NightCaptureComplete, DurationAssessment: health.NightDurationPlausible, FinalizationState: health.NightFinalProvisional, ClaimEligibility: health.NightClaimIneligible, ObservedAt: time.Now().UTC()}
	if err := db.SaveCompletedNightSleep(context.Background(), night, nil); err != nil {
		t.Fatal(err)
	}
	cfg := AIConfig{Provider: "openai", Providers: map[string]AIProviderSettings{"openai": {APIKey: "synthetic-test-key"}}}
	sleep, blocks, err := db.MorningReportEvidence(context.Background(), cfg, "ru", today, &health.BriefingResponse{Date: today})
	if err == nil || len(blocks) != 0 || sleep.Date != today || sleep.Hours == nil || *sleep.Hours != 7.5 {
		t.Fatalf("canonical fact lost: %#v %v %v", sleep, blocks, err)
	}
}

func TestMorningEvidenceNewerUnverifiedNightDoesNotReuseOldCanonicalMetadata(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	night := health.CompletedNightSleep{WakeDate: "2026-09-29", DurationHours: 7.5, Source: "synthetic", SourceEpoch: "epoch", InputHash: "night-1", AlgorithmVersion: "v1", CaptureCompleteness: health.NightCaptureComplete, DurationAssessment: health.NightDurationPlausible, FinalizationState: health.NightFinalFinal, ClaimEligibility: health.NightClaimEligible, ObservedAt: time.Now().UTC()}
	if err := db.SaveCompletedNightSleep(ctx, night, nil); err != nil {
		t.Fatal(err)
	}
	hours := 6.5
	briefing := &health.BriefingResponse{Date: "2026-09-30", Sleep: &health.SleepAnalysis{LatestDate: "2026-09-30", LatestTotal: &hours}}
	evidence, err := db.morningInsightEvidence(ctx, briefing, nil, "2026-09-30", "en")
	if err != nil {
		t.Fatal(err)
	}
	got := evidence.NightSleep
	if got.Date != "2026-09-30" || got.Capture != health.NightCaptureUnknown || got.Assessment != health.NightDurationUnknown || got.InputHash != "" || got.Hours == nil || *got.Hours != 6.5 || got.BaselineHours != nil {
		t.Fatalf("old canonical metadata leaked into new observation: %#v", got)
	}
	night.CaptureCompleteness = health.NightCapturePartial
	night.InputHash = "night-2"
	if err := db.SaveCompletedNightSleep(ctx, night, nil); err != nil {
		t.Fatal(err)
	}
	briefing.Sleep.LatestDate = "2026-09-29"
	evidence, err = db.morningInsightEvidence(ctx, briefing, nil, "2026-09-30", "en")
	if err != nil || (evidence.NightSleep.InputHash != "night-2" || evidence.NightSleep.Capture != health.NightCapturePartial) {
		t.Fatalf("same-day canonical replaced by generic: %#v %v", evidence.NightSleep, err)
	}
}

func TestMorningEvidenceUsesFinalNightInsteadOfStableObservedEpisode(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	if err := db.SaveSettings(map[string]string{"timezone": "UTC"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	date := now.Format("2006-01-02")
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	id := insertTestRawRecord(t, db, "final-versus-observed")
	pointDate := day.Add(4 * time.Hour).Format("2006-01-02 15:04:05 -0700")
	if _, err := db.pool.Exec(ctx, `INSERT INTO metric_points(health_record_id,metric_name,units,date,qty,source,quality) VALUES($1,'sleep_core','hr',$2,3,'Apple Watch','ok')`, id, pointDate); err != nil {
		t.Fatal(err)
	}
	candidate, ok, err := db.loadWakeCandidate(date, time.UTC)
	if err != nil || !ok {
		t.Fatalf("candidate %v %v", ok, err)
	}
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wake := MorningWakeStatus{Ready: true, Confidence: WakeConfidenceHigh, CandidateWake: candidate.Wake, InputSource: candidate.Source, InputsHash: candidate.InputsHash, Signal: candidate.Signal}
	if err := saveWakeStatusTx(ctx, tx, date, wake, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	final := health.CompletedNightSleep{WakeDate: date, DurationHours: 7.5, Source: "Apple Watch", SourceEpoch: "test", InputHash: "final", AlgorithmVersion: "v1", CaptureCompleteness: health.NightCaptureComplete, DurationAssessment: health.NightDurationPlausible, FinalizationState: health.NightFinalFinal, ClaimEligibility: health.NightClaimEligible, ObservedAt: now}
	if err := db.SaveCompletedNightSleep(ctx, final, nil); err != nil {
		t.Fatal(err)
	}
	evidence, err := db.morningInsightEvidence(ctx, &health.BriefingResponse{Date: date}, nil, date, "en")
	if err != nil || evidence.NightSleep == nil || evidence.NightSleep.Observed != nil || !evidence.NightSleep.HasUsableFinalNight(date) {
		t.Fatalf("final evidence: %#v, %v", evidence.NightSleep, err)
	}
}
