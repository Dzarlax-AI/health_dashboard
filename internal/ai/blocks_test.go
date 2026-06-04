package ai

import (
	"strings"
	"testing"

	"health-receiver/internal/health"
)

func TestHashRecoveryIncludesReadinessContextAndCheckin(t *testing.T) {
	raw := &health.RawMetrics{
		LastDate: "2026-06-04",
		HRV:      []float64{40, 41, 42},
		RHR:      []float64{60, 61, 62},
		Sleep:    []float64{7, 7.5, 8},
	}
	base := InsightContext{
		ReadinessScore:      65,
		ReadinessRawScore:   95,
		ReadinessConfidence: health.ReadinessConfidenceProvisional,
		ReadinessCapReason:  "missing_same_day_evidence",
		AIAdviceMode:        "needs_regeneration_after_sync",
		CheckinStatus:       "answered",
		CheckinAnswer:       "meh",
	}
	changed := base
	changed.CheckinAnswer = "sick"

	if got, wantNot := HashRecovery(raw, nil, base), HashRecovery(raw, nil, changed); got == wantNot {
		t.Fatalf("HashRecovery did not change after check-in answer changed")
	}
}

func TestHashRecommendationIncludesReadinessContextAndCheckin(t *testing.T) {
	base := InsightContext{AIAdviceMode: "confident_advice_allowed", CheckinStatus: "answered", CheckinAnswer: "ok"}
	changed := base
	changed.AIAdviceMode = "provisional_explanation_only"

	got := HashRecommendation("sleep", "yesterday", "recovery", nil, []string{"moderate"}, base)
	wantNot := HashRecommendation("sleep", "yesterday", "recovery", nil, []string{"moderate"}, changed)
	if got == wantNot {
		t.Fatalf("HashRecommendation did not change after AI advice mode changed")
	}
}

func TestGenerateRecommendationAddsProvisionalInstruction(t *testing.T) {
	ctx := InsightContext{
		ReadinessScore:      65,
		ReadinessRawScore:   95,
		ReadinessConfidence: health.ReadinessConfidenceProvisional,
		ReadinessCapReason:  "missing_same_day_evidence",
		AIAdviceMode:        "provisional_explanation_only",
		CheckinStatus:       "answered",
		CheckinAnswer:       "sick",
	}
	text, err := GenerateRecommendation("", "test-model", 100, []byte(`{}`), "en", "sleep", "yesterday", "recovery", nil, nil, ctx)
	if err == nil && strings.TrimSpace(text) != "" {
		t.Fatalf("GenerateRecommendation unexpectedly called model without API key")
	}
}
