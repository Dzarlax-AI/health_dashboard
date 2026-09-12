package ai

import (
	"fmt"
	"time"

	"health-receiver/internal/health"
)

// BuildDailyInsightNarrativeCorpusScaffold converts a reviewed candidate
// export into a deterministic 20-case draft corpus. It only handles already
// sanitized packets; no database state, dates, measurements, display copy or
// provider output enters the result.
//
// Three explicitly labelled synthetic fixtures cover bounded edge states that
// may be absent from a user's retained history. They exercise safety handling,
// never assert a fact about the user, and remain visible in the later human
// quality review alongside observed aggregate packets.
func BuildDailyInsightNarrativeCorpusScaffold(export DailyInsightNarrativeCandidateExport) (DailyInsightNarrativeCorpus, error) {
	if export.Version != "daily-insight-narrative-candidates-v1" {
		return DailyInsightNarrativeCorpus{}, fmt.Errorf("unsupported candidate export version %q", export.Version)
	}
	selected := make([]DailyInsightNarrativeCorpusCase, 0, DailyInsightNarrativeCorpusMinCases)
	used := make(map[int]struct{})
	add := func(predicate func(DailyInsightNarrativeCorpusCase) bool, tags []string, retainCheckin bool) error {
		for index, candidate := range export.Candidates {
			if _, exists := used[index]; exists || !predicate(candidate.DailyInsightNarrativeCorpusCase) {
				continue
			}
			item := observedNarrativeCorpusCase(candidate.DailyInsightNarrativeCorpusCase, len(selected)+1, tags, retainCheckin)
			selected = append(selected, item)
			used[index] = struct{}{}
			return nil
		}
		return fmt.Errorf("candidate export does not cover required observed state %q", tags)
	}

	if err := add(hasConfirmedSleepBaselineClaim, []string{"observed_state", "mixed_sleep_baseline", "complete_sleep"}, false); err != nil {
		return DailyInsightNarrativeCorpus{}, err
	}
	if err := add(hasIncompleteSleep, []string{"observed_state", "incomplete_sleep"}, false); err != nil {
		return DailyInsightNarrativeCorpus{}, err
	}
	if err := add(hasAbsentCheckin, []string{"observed_state", "no_checkin"}, true); err != nil {
		return DailyInsightNarrativeCorpus{}, err
	}
	if err := add(hasOnlyUnavailableDomains, []string{"observed_state", "no_data"}, false); err != nil {
		return DailyInsightNarrativeCorpus{}, err
	}
	for _, locale := range []string{"en", "ru", "sr"} {
		if hasNarrativeEligibleLocale(selected, locale) {
			continue
		}
		if err := add(func(item DailyInsightNarrativeCorpusCase) bool {
			snapshot, err := item.SnapshotForEvaluation()
			return err == nil && item.Locale == locale && health.HasEligibleDailyInsightNarrativeClaims(&snapshot, locale)
		}, []string{"observed_state"}, false); err != nil {
			return DailyInsightNarrativeCorpus{}, fmt.Errorf("seed %s narrative coverage: %w", locale, err)
		}
	}
	// The current retained history contains a small but useful sample of the
	// B0 sleep pattern. Keep six observed examples (rather than letting the
	// common energy verdict dominate the bounded corpus), while still leaving
	// room for incomplete and fallback-only cases.
	for countCasesMatching(selected, hasConfirmedSleepBaselineClaim) < 6 && len(selected) < 17 {
		if err := add(hasConfirmedSleepBaselineClaim, []string{"observed_state", "complete_sleep"}, false); err != nil {
			break
		}
	}
	for len(selected) < 17 {
		if err := add(func(DailyInsightNarrativeCorpusCase) bool { return true }, []string{"observed_state"}, false); err != nil {
			return DailyInsightNarrativeCorpus{}, err
		}
	}

	selected = append(selected,
		syntheticLimitedHistoryCase(len(selected)+1),
		syntheticRecoveryEnergyConflictCase(len(selected)+2),
		syntheticLateSourceUpdateCase(len(selected)+3),
	)
	corpus := DailyInsightNarrativeCorpus{Version: "daily-insight-narrative-corpus-v1", Cases: selected}
	if err := ValidateDailyInsightNarrativeCorpus(corpus); err != nil {
		return DailyInsightNarrativeCorpus{}, fmt.Errorf("validate corpus scaffold: %w", err)
	}
	return corpus, nil
}

func hasNarrativeEligibleLocale(items []DailyInsightNarrativeCorpusCase, locale string) bool {
	for _, item := range items {
		if item.Locale != locale {
			continue
		}
		snapshot, err := item.SnapshotForEvaluation()
		if err == nil && health.HasEligibleDailyInsightNarrativeClaims(&snapshot, locale) {
			return true
		}
	}
	return false
}

func countCasesMatching(items []DailyInsightNarrativeCorpusCase, predicate func(DailyInsightNarrativeCorpusCase) bool) int {
	count := 0
	for _, item := range items {
		if predicate(item) {
			count++
		}
	}
	return count
}

func observedNarrativeCorpusCase(item DailyInsightNarrativeCorpusCase, index int, tags []string, retainCheckin bool) DailyInsightNarrativeCorpusCase {
	item.ID = fmt.Sprintf("observed-%03d", index)
	item.Origin = DailyInsightNarrativeOriginObserved
	item.Tags = append([]string(nil), tags...)
	if !retainCheckin {
		item.Scenario.CheckIn = ""
	}
	return item
}

func hasConfirmedSleepBaselineClaim(item DailyInsightNarrativeCorpusCase) bool {
	snapshot, err := item.SnapshotForEvaluation()
	if err != nil {
		return false
	}
	sleep, found := narrativeCorpusDomain(snapshot, "sleep")
	return found && sleep.DataState == "fresh" && sleep.Confidence == "final" &&
		sleep.Insight.ClaimID == "recent_sleep_below_reference" &&
		sleep.Insight.AnswerKind == health.DailyInsightAnswerConfirmedPersonal
}

func hasIncompleteSleep(item DailyInsightNarrativeCorpusCase) bool {
	snapshot, err := item.SnapshotForEvaluation()
	if err != nil {
		return false
	}
	sleep, found := narrativeCorpusDomain(snapshot, "sleep")
	return found && (sleep.DataState == "partial" || sleep.DataState == "missing" || sleep.DataState == "stale")
}

func hasAbsentCheckin(item DailyInsightNarrativeCorpusCase) bool {
	return item.Scenario.CheckIn == "absent"
}

func hasOnlyUnavailableDomains(item DailyInsightNarrativeCorpusCase) bool {
	snapshot, err := item.SnapshotForEvaluation()
	return err == nil && !health.HasEligibleDailyInsightNarrativeClaims(&snapshot, item.Locale) && narrativeCorpusHasOnlyUnavailableDomains(snapshot)
}

func syntheticLimitedHistoryCase(index int) DailyInsightNarrativeCorpusCase {
	return DailyInsightNarrativeCorpusCase{
		ID: fmt.Sprintf("synthetic-%03d", index), Locale: "ru", Origin: DailyInsightNarrativeOriginSynthetic,
		Tags: []string{DailyInsightNarrativeOriginSynthetic, "limited_history"},
		Snapshot: health.DailyInsightSnapshot{Date: "review-day", Version: health.DailyInsightSnapshotVersion, Domains: []health.DailyInsightDomain{{
			Key: "sleep", DataState: "fresh", Confidence: "low",
			Insight: health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerProvisional, GapReason: "sleep_history_short", Fallback: true},
		}}},
	}
}

func syntheticRecoveryEnergyConflictCase(index int) DailyInsightNarrativeCorpusCase {
	return DailyInsightNarrativeCorpusCase{
		ID: fmt.Sprintf("synthetic-%03d", index), Locale: "en", Origin: DailyInsightNarrativeOriginSynthetic,
		Tags:              []string{DailyInsightNarrativeOriginSynthetic, "energy_recovery_conflict"},
		Scenario:          DailyInsightNarrativeCorpusScenario{ConflictEvidenceIDs: map[string]string{"recovery": "evidence-recovery", "energy": "evidence-energy"}},
		NarrativeSubjects: map[string]string{"energy": "active_recovery"},
		Snapshot: health.DailyInsightSnapshot{Date: "review-day", Version: health.DailyInsightSnapshotVersion,
			Domains: []health.DailyInsightDomain{
				{Key: "recovery", DataState: "fresh", Confidence: "final", Insight: health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerFactual, ClaimID: "recovery_readiness_context", EvidenceIDs: []string{"evidence-recovery"}}},
				{Key: "energy", DataState: "fresh", Confidence: "final", Insight: health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerFactual, ClaimID: "energy_current_verdict_context", EvidenceIDs: []string{"evidence-energy"}}},
			},
			Evidence: []health.DailyInsightEvidence{{ID: "evidence-recovery", Domain: "recovery", DataState: "fresh", Confidence: "final"}, {ID: "evidence-energy", Domain: "energy", DataState: "fresh", Confidence: "final"}},
		},
	}
}

func syntheticLateSourceUpdateCase(index int) DailyInsightNarrativeCorpusCase {
	updatedAt := time.Date(2000, time.January, 2, 18, 1, 0, 0, time.UTC)
	return DailyInsightNarrativeCorpusCase{
		ID: fmt.Sprintf("synthetic-%03d", index), Locale: "sr", Origin: DailyInsightNarrativeOriginSynthetic,
		Tags:     []string{DailyInsightNarrativeOriginSynthetic, "late_source_update"},
		Scenario: DailyInsightNarrativeCorpusScenario{UpdateKind: "late_source_update"},
		Snapshot: health.DailyInsightSnapshot{Date: "review-day", Version: health.DailyInsightSnapshotVersion, UpdatedAt: &updatedAt, Domains: []health.DailyInsightDomain{{
			Key: "sleep", DataState: "partial", Confidence: "provisional",
			Insight: health.DailyInsight{State: "insufficient_data", AnswerKind: health.DailyInsightAnswerDataGuidance, GapReason: "sleep_partial", Fallback: true},
		}}},
	}
}
