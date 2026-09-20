package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"health-receiver/internal/ai"
	"health-receiver/internal/health"
)

func TestOpenCandidateSourceRequiresDirectDSNWhenIsolationDisabled(t *testing.T) {
	t.Setenv("TENANT_DB_ISOLATION_ENABLED", "false")
	for _, key := range []string{"PGHOST", "PGPORT", "PGDATABASE", "PGUSER"} {
		t.Setenv(key, "")
	}

	db, closeSource, err := openCandidateSource(context.Background(), "", "health")
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("openCandidateSource error = %v, want missing direct DSN error", err)
	}
	if db != nil || closeSource != nil {
		t.Fatal("openCandidateSource returned a source on configuration error")
	}
}

func TestOpenCandidateSourceRejectsInvalidIsolationBeforeUsingDatabaseURL(t *testing.T) {
	t.Setenv("TENANT_DB_ISOLATION_ENABLED", "true")
	t.Setenv("ADMIN_DATABASE_URL", "")
	t.Setenv("REGISTRY_DATABASE_URL", "")
	t.Setenv("TENANT_DATABASE_URL_BASE", "")
	t.Setenv("TENANT_DB_MASTER_SECRET", "")
	t.Setenv("TENANT_DB_MASTER_SECRET_VERSION", "")

	db, closeSource, err := openCandidateSource(context.Background(), "postgres://registry.example/health", "health")
	if err == nil || !strings.Contains(err.Error(), "parse tenant candidate source") {
		t.Fatalf("openCandidateSource error = %v, want isolation configuration error", err)
	}
	if db != nil || closeSource != nil {
		t.Fatal("openCandidateSource returned a source for invalid isolation configuration")
	}
}

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
			{Key: "sleep", DataState: "fresh", Confidence: "final", Insight: health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerConfirmedPersonal, ClaimID: "recent_sleep_below_reference", EvidenceIDs: []string{"sleep-evidence"}}},
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

func TestSelectDiverseCandidatesReservesTemporalCoverageWhenAllSignaturesDiffer(t *testing.T) {
	candidates := []ai.DailyInsightNarrativeCandidate{
		candidateForSelection("en", "rest", "a"),
		candidateForSelection("en", "rest", "b"),
		candidateForSelection("en", "rest", "c"),
		candidateForSelection("en", "rest", "d"),
		candidateForSelection("en", "rest", "e"),
		candidateForSelection("en", "rest", "f"),
	}
	got := selectDiverseCandidates(candidates, 4)
	if len(got) != 4 {
		t.Fatalf("selected %d candidates, want 4", len(got))
	}
	if got[0].ReviewHints[0] != "a" || got[len(got)-1].ReviewHints[0] != "f" {
		t.Fatalf("selection did not retain temporal endpoints: %#v", got)
	}
	if got[2].ReviewHints[0] != "c" {
		t.Fatalf("selection did not retain a middle candidate: %#v", got)
	}
}

func TestCandidateJobsKeepHistoricalLocaleOrderBeforeConcurrentReads(t *testing.T) {
	start := time.Date(2026, time.September, 11, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, time.September, 12, 0, 0, 0, 0, time.UTC)
	jobs := candidateJobs(start, end, []string{"en", "ru", "sr"})
	if len(jobs) != 6 {
		t.Fatalf("job count = %d, want 6", len(jobs))
	}
	want := []struct{ date, locale, id string }{
		{"2026-09-12", "en", "candidate-001"},
		{"2026-09-12", "ru", "candidate-002"},
		{"2026-09-12", "sr", "candidate-003"},
		{"2026-09-11", "en", "candidate-004"},
		{"2026-09-11", "ru", "candidate-005"},
		{"2026-09-11", "sr", "candidate-006"},
	}
	for index, expected := range want {
		job := jobs[index]
		if job.date.Format("2006-01-02") != expected.date || job.locale != expected.locale || job.candidateID != expected.id {
			t.Fatalf("job %d = %#v, want date=%s locale=%s id=%s", index, job, expected.date, expected.locale, expected.id)
		}
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
