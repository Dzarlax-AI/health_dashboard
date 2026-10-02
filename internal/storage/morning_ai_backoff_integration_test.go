package storage

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"
)

type failingMorningEvidenceProvider struct {
	id    string
	calls atomic.Int32
}

func (p *failingMorningEvidenceProvider) Descriptor() ai.ProviderDescriptor {
	return ai.ProviderDescriptor{ID: p.id, DefaultModel: "synthetic"}
}
func (p *failingMorningEvidenceProvider) ListModels(context.Context, string) ([]ai.Model, error) {
	return nil, nil
}
func (p *failingMorningEvidenceProvider) Generate(context.Context, ai.ProviderConfig, ai.GenerationRequest) (ai.GenerationResult, error) {
	p.calls.Add(1)
	return ai.GenerationResult{}, errors.New("synthetic provider failure")
}

var failingMorningProviderSequence atomic.Int32

func TestMorningFailureBackoffAppliesOnlyToExactEvidence(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	if err := db.SaveSettings(map[string]string{"timezone": "UTC"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO hourly_metrics(metric_name,hour,source,avg_val,sample_count,min_val,max_val) VALUES('step_count',$1,'synthetic',10,1,10,10)`, today+" 08"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO daily_scores(date,sleep_total,hrv_avg,steps) VALUES($1,7.5,55,10)`, today); err != nil {
		t.Fatal(err)
	}
	night := health.CompletedNightSleep{WakeDate: today, DurationHours: 7.5, Source: "synthetic", SourceEpoch: "synthetic", InputHash: "night-A", AlgorithmVersion: "v1", CaptureCompleteness: health.NightCapturePartial, DurationAssessment: health.NightDurationPlausible, FinalizationState: health.NightFinalProvisional, ClaimEligibility: health.NightClaimIneligible, ObservedAt: now}
	if err := db.SaveCompletedNightSleep(ctx, night, nil); err != nil {
		t.Fatal(err)
	}
	p := &failingMorningEvidenceProvider{id: fmt.Sprintf("morning-failure-evidence-test-%d", failingMorningProviderSequence.Add(1))}
	ai.RegisterProvider(p)
	cfg := AIConfig{Provider: p.id, Providers: map[string]AIProviderSettings{p.id: {APIKey: "synthetic-only"}}}
	failedCurrent := func() bool {
		t.Helper()
		b, err := db.GetHealthBriefing("en")
		if err != nil {
			t.Fatal(err)
		}
		_, _, failed, err := db.MorningReportAIState(ctx, cfg, "en", today, b)
		if err != nil {
			t.Fatal(err)
		}
		return failed
	}
	db.EnsureTodayAIInsightContext(ctx, cfg, "en")
	if !failedCurrent() {
		t.Fatal("failed evidence A missing backoff")
	}
	db.EnsureTodayAIInsightContext(ctx, cfg, "en")
	if p.calls.Load() != 1 {
		t.Fatal("unchanged failed evidence retried")
	}
	night.InputHash = "night-B"
	night.DurationHours = 7.75
	if err := db.SaveCompletedNightSleep(ctx, night, nil); err != nil {
		t.Fatal(err)
	}
	if failedCurrent() {
		t.Fatal("failure for A caused immediate fallback for changed evidence B")
	}
	db.EnsureTodayAIInsightContext(ctx, cfg, "en")
	if p.calls.Load() != 2 || !failedCurrent() {
		t.Fatalf("changed evidence did not get its own attempt/backoff: calls=%d", p.calls.Load())
	}
}
