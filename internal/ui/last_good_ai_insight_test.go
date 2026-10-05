package ui

import (
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
	"testing"
	"time"
)

func TestLastGoodAIIsDisplayOnlyAndDoesNotChangeCurrentEvidence(t *testing.T) {
	snapshot := &health.DailyInsightSnapshot{Date: "2026-10-05", Domains: []health.DailyInsightDomain{{Key: "sleep", DataState: "partial"}}}
	saved := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	entry := storage.LastGoodAIInsight{Date: "2026-10-04", GeneratedAt: saved, Insight: &health.AIInsightSection{Text: "Previous opinion", Stance: "qualify", FactIDs: []string{"old-fact"}}}
	got := applyLastGoodAIInsights(snapshot, map[string]storage.LastGoodAIInsight{"sleep": entry})
	ai := got.Domains[0].AIInsight
	if ai == nil || !ai.Stale || ai.SourceDate != entry.Date || !ai.GeneratedAt.Equal(saved) {
		t.Fatalf("missing historical metadata: %#v", ai)
	}
	if len(got.NarrativeFacts) != 0 || len(got.Evidence) != 0 || got.Domains[0].DataState != "partial" {
		t.Fatal("previous opinion became current evidence")
	}
	if snapshot.Domains[0].AIInsight != nil {
		t.Fatal("fallback mutated generation snapshot")
	}
	snapshot.Domains[0].AIInsight = &health.DailyInsightAIInsight{Text: "Current opinion", Stance: "agree"}
	got = applyLastGoodAIInsights(snapshot, map[string]storage.LastGoodAIInsight{"sleep": entry})
	if got.Domains[0].AIInsight.Text != "Current opinion" || got.Domains[0].AIInsight.Stale {
		t.Fatal("previous opinion replaced current opinion")
	}
}
