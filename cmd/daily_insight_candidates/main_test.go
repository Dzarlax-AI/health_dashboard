package main

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"health-receiver/internal/ai"
	"health-receiver/internal/health"
)

func TestCandidateFailureReasonDoesNotExposeStorageError(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{errors.New("no retained metrics for historical insight date 2026-09-12"), "no_retained_metrics"},
		{errors.New("evaluate historical recent sleep claim for 2026-09-12: relation details"), "sleep_claim_unavailable"},
		{errors.New("database connection to private-host failed"), "storage_unavailable"},
		{&pgconn.PgError{Code: "42P01", Message: "hidden"}, "storage_sqlstate_42P01"},
	}
	for _, tt := range tests {
		if got := candidateFailureReason(tt.err); got != tt.want {
			t.Errorf("candidateFailureReason(%q) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

func TestCandidateReviewHintsAreStructuralRatherThanProvenanceClaims(t *testing.T) {
	item := ai.DailyInsightNarrativeCorpusCase{
		Locale: "en",
		Snapshot: health.DailyInsightSnapshot{Domains: []health.DailyInsightDomain{
			{Key: "sleep", DataState: "fresh", Confidence: "final", Insight: health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerFactual}},
			{Key: "recovery", DataState: "fresh", Confidence: "final", Insight: health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerFactual}},
			{Key: "energy", DataState: "fresh", Confidence: "final", NarrativeSubject: "rest", Insight: health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerFactual, ClaimID: "energy_current_verdict_context", EvidenceIDs: []string{"energy-evidence"}}},
		}},
		NarrativeSubjects: map[string]string{"energy": "rest"},
	}
	hints := ai.DailyInsightNarrativeCandidateReviewHints(item)
	set := map[string]bool{}
	for _, hint := range hints {
		set[hint] = true
	}
	for _, want := range []string{"sleep_fresh_final", "narrative_eligible", "recovery_energy_both_final"} {
		if !set[want] {
			t.Errorf("review hints = %v, missing %q", hints, want)
		}
	}
	for _, forbidden := range []string{"no_checkin", "late_source_update", "energy_recovery_conflict"} {
		if set[forbidden] {
			t.Errorf("review hints = %v, must not infer %q", hints, forbidden)
		}
	}
}

func TestSelectDiverseCandidatesKeepsStructuralAndTemporalCoverage(t *testing.T) {
	candidates := []ai.DailyInsightNarrativeCandidate{
		candidateForSelection("en", "rest", "sleep_fresh_final"),
		candidateForSelection("en", "rest", "sleep_fresh_final"),
		candidateForSelection("ru", "rest", "sleep_fresh_final"),
		candidateForSelection("sr", "push_hard", "sleep_fresh_final"),
		candidateForSelection("en", "", "sleep_incomplete"),
		candidateForSelection("ru", "", "all_domains_unavailable"),
	}
	got := selectDiverseCandidates(candidates, 4)
	if len(got) != 4 {
		t.Fatalf("selected %d candidates, want 4", len(got))
	}
	seen := map[string]bool{}
	for index, candidate := range got {
		if candidate.ID != "candidate-00"+string(rune('1'+index)) {
			t.Fatalf("candidate %d opaque ID = %q", index, candidate.ID)
		}
		seen[candidateStructuralSignature(candidate)] = true
	}
	if len(seen) != 4 {
		t.Fatalf("selected signatures = %v, want four distinct structural cases", seen)
	}
}

func candidateForSelection(locale, subject, hint string) ai.DailyInsightNarrativeCandidate {
	item := ai.DailyInsightNarrativeCorpusCase{
		Locale: locale,
		Snapshot: health.DailyInsightSnapshot{Domains: []health.DailyInsightDomain{{
			Key: "sleep", DataState: "fresh", Confidence: "final",
			Insight: health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerFactual},
		}}},
	}
	if subject != "" {
		item.Snapshot.Domains = append(item.Snapshot.Domains, health.DailyInsightDomain{Key: "energy", DataState: "fresh", Confidence: "final", NarrativeSubject: subject, Insight: health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerFactual, ClaimID: "energy_current_verdict_context", EvidenceIDs: []string{"energy"}}})
		item.NarrativeSubjects = map[string]string{"energy": subject}
	}
	return ai.DailyInsightNarrativeCandidate{DailyInsightNarrativeCorpusCase: item, ReviewHints: []string{hint}}
}
