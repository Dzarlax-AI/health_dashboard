package ai

import (
	"fmt"
	"sort"

	"health-receiver/internal/health"
)

// BuildDailyInsightNarrativeCorpusScaffold converts a reviewed candidate
// export into a deterministic 20-case draft corpus. It only handles already
// sanitized packets; no database state, dates, measurements, display copy or
// provider output enters the result.
//
// Five explicitly labelled synthetic fixtures cover bounded edge states that
// may be absent from a user's retained history. They exercise safety handling,
// never assert a fact about the user, and remain visible in the later human
// quality review alongside observed aggregate packets.
func BuildDailyInsightNarrativeCorpusScaffold(export DailyInsightNarrativeCandidateExport) (DailyInsightNarrativeCorpus, error) {
	if export.Version != "daily-insight-narrative-candidates-v1" {
		return DailyInsightNarrativeCorpus{}, fmt.Errorf("unsupported candidate export version %q", export.Version)
	}
	observedLimit := DailyInsightNarrativeCorpusMinCases - DailyInsightNarrativeCorpusMaxSyntheticCases
	selected := make([]DailyInsightNarrativeCorpusCase, 0, observedLimit)
	used := make(map[int]struct{})
	add := func(predicate func(DailyInsightNarrativeCorpusCase) bool, tags []string, retainCheckin bool) error {
		for index, candidate := range export.Candidates {
			if _, exists := used[index]; exists || candidate.PrimaryMeaningID == "" || !predicate(candidate.DailyInsightNarrativeCorpusCase) {
				continue
			}
			item := observedNarrativeCorpusCase(candidate.DailyInsightNarrativeCorpusCase, len(selected)+1, tags, retainCheckin)
			selected = append(selected, item)
			used[index] = struct{}{}
			return nil
		}
		return fmt.Errorf("candidate export does not cover required observed state %q", tags)
	}
	normalObserved := add(hasNormalContext, []string{"observed_state", "normal_context"}, false) == nil
	positiveObserved := add(hasPositiveRecoveryContext, []string{"observed_state", "positive_context"}, false) == nil
	_ = add(hasProvisionalContext, []string{"observed_state", "provisional_context"}, false)

	for index, locale := range []string{"en", "ru", "sr"} {
		tags := []string{"observed_state", "complete_sleep"}
		if index == 0 {
			tags = append(tags, "mixed_sleep_baseline")
		}
		if err := add(func(item DailyInsightNarrativeCorpusCase) bool {
			return item.Locale == locale && hasConfirmedSleepBaselineClaim(item)
		}, tags, false); err != nil {
			// A pre-B0-migration export can legitimately retain no canonical
			// sleep claim at all. The three explicitly marked controlled
			// conflict fixtures then carry that structural contract coverage;
			// they never become evidence about the user.
			continue
		}
	}
	if err := add(hasIncompleteSleep, []string{"observed_state", "incomplete_sleep"}, false); err != nil {
		// The controlled late-update fixture covers this absent state.
	}
	if err := add(hasAbsentCheckin, []string{"observed_state", "no_checkin"}, true); err != nil {
		// Optional check-in provenance is intentionally not fabricated from a
		// missing table; the limited-history fixture carries explicit absence.
	}
	if err := add(hasOnlyUnavailableDomains, []string{"observed_state", "no_data"}, false); err != nil {
		// The controlled late-update fixture covers a truthful no-data state.
	}
	// The current retained history contains a small but useful sample of the
	// B0 sleep pattern. Keep six observed examples (rather than letting the
	// common energy verdict dominate the bounded corpus), while still leaving
	// room for incomplete and fallback-only cases.
	for countCasesMatching(selected, hasConfirmedSleepBaselineClaim) < 6 && len(selected) < observedLimit {
		if err := add(hasConfirmedSleepBaselineClaim, []string{"observed_state", "complete_sleep"}, false); err != nil {
			break
		}
	}
	for len(selected) < observedLimit {
		if err := add(func(DailyInsightNarrativeCorpusCase) bool { return true }, []string{"observed_state"}, false); err != nil {
			return DailyInsightNarrativeCorpus{}, err
		}
	}

	selected = append(selected,
		syntheticLimitedHistoryCase(len(selected)+1),
		syntheticRecoveryEnergyConflictCase(len(selected)+2, "en", !normalObserved, !positiveObserved),
		syntheticRecoveryEnergyConflictCase(len(selected)+3, "ru", false, false),
		syntheticRecoveryEnergyConflictCase(len(selected)+4, "sr", false, false),
		syntheticLateSourceUpdateCase(len(selected)+5),
	)
	corpus := DailyInsightNarrativeCorpus{
		Version:       DailyInsightNarrativeCorpusVersionV2,
		PacketVersion: health.DailyInsightNarrativeInputVersion,
		PacketShape:   DailyInsightNarrativeCorpusCurrentPacketShape(),
		Cases:         selected,
	}
	if err := ValidateDailyInsightNarrativeCorpus(corpus); err != nil {
		return DailyInsightNarrativeCorpus{}, fmt.Errorf("validate corpus scaffold: %w", err)
	}
	return corpus, nil
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
	// Candidate exports may still contain review placeholders from the
	// reconstruction step. Re-run the sanitizer at the freeze boundary so the
	// v2 artifact never inherits dates, decision IDs, or source identifiers.
	sanitized := SanitizeDailyInsightNarrativeCorpusCandidate(item.Snapshot, item.Locale, fmt.Sprintf("observed-%03d", index))
	sanitized.Scenario = item.Scenario
	// Candidate exports already carry the sanitized B1 material separately
	// because health.DailyInsightSnapshot intentionally hides NarrativeFacts
	// from JSON. Preserve that material through the freeze boundary, but copy
	// it deeply so later validation sees exactly what will be frozen and no
	// caller-owned slice can mutate the draft after selection.
	if item.NarrativeFacts != nil {
		sanitized.NarrativeFacts = cloneCorpusNarrativeFacts(item.NarrativeFacts)
	}
	if item.VisibleB0Baseline != nil {
		sanitized.VisibleB0Baseline = cloneCorpusBaseline(item.VisibleB0Baseline)
	}
	if item.ActionOptions != nil {
		sanitized.ActionOptions = cloneCorpusActionOptions(item.ActionOptions)
	}
	// v2 does not retain the closed decision identity; the overall packet is
	// eligible from its derived fact set alone.
	sanitized.Snapshot.DecisionID = ""
	sanitized.PrimaryNarrativeSubject = ""
	item = sanitized
	item.ID = fmt.Sprintf("observed-%03d", index)
	item.Origin = DailyInsightNarrativeOriginObserved
	item.Tags = append([]string(nil), tags...)
	item.Tags = append(item.Tags, v2TagsForCorpusCase(item)...)
	if !retainCheckin {
		item.Scenario.CheckIn = ""
	}
	return item
}

func cloneCorpusNarrativeFacts(facts []health.DailyInsightNarrativeFact) []health.DailyInsightNarrativeFact {
	if facts == nil {
		return nil
	}
	out := make([]health.DailyInsightNarrativeFact, len(facts))
	for index, fact := range facts {
		out[index] = fact
		out[index].DisplayValues = append([]string(nil), fact.DisplayValues...)
		out[index].EvidenceIDs = append([]string(nil), fact.EvidenceIDs...)
	}
	return out
}

func cloneCorpusBaseline(baseline *health.DailyInsightNarrativeBaseline) *health.DailyInsightNarrativeBaseline {
	if baseline == nil {
		return nil
	}
	out := *baseline
	out.Domains = append([]health.DailyInsightBaselineDomain(nil), baseline.Domains...)
	return &out
}

func cloneCorpusActionOptions(actions []health.DailyInsightNarrativeAction) []health.DailyInsightNarrativeAction {
	if actions == nil {
		return nil
	}
	out := make([]health.DailyInsightNarrativeAction, len(actions))
	copy(out, actions)
	for index := range out {
		out[index].FactIDs = append([]string(nil), actions[index].FactIDs...)
	}
	return out
}

func v2TagsForCorpusCase(item DailyInsightNarrativeCorpusCase) []string {
	derived, err := v2CorpusCoverageTags(item)
	if err != nil {
		panic(fmt.Sprintf("derive v2 corpus coverage tags: %v", err))
	}
	tags := make([]string, 0, len(derived))
	for tag := range derived {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
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

func hasNormalContext(item DailyInsightNarrativeCorpusCase) bool {
	snapshot, err := item.SnapshotForEvaluation()
	if err != nil || snapshot.Primary.State != "insight" || snapshot.Primary.AnswerKind != health.DailyInsightAnswerFactual || snapshot.Primary.NarrativeSubject != "moderate" {
		return false
	}
	sleep, found := narrativeCorpusDomain(snapshot, "sleep")
	return found && sleep.DataState == "fresh" && sleep.Confidence == "final"
}

func hasPositiveRecoveryContext(item DailyInsightNarrativeCorpusCase) bool {
	snapshot, err := item.SnapshotForEvaluation()
	if err != nil {
		return false
	}
	recovery, found := narrativeCorpusDomain(snapshot, "recovery")
	return found && recovery.DataState == "fresh" && recovery.Confidence == "final" && recovery.Band == "optimal" && recovery.Insight.ClaimID == "recovery_readiness_context"
}

func hasProvisionalContext(item DailyInsightNarrativeCorpusCase) bool {
	snapshot, err := item.SnapshotForEvaluation()
	if err != nil {
		return false
	}
	for _, domain := range snapshot.Domains {
		if domain.Insight.AnswerKind == health.DailyInsightAnswerProvisional && domain.Insight.ClaimID == "" {
			return true
		}
	}
	return false
}

func hasAbsentCheckin(item DailyInsightNarrativeCorpusCase) bool {
	return item.Scenario.CheckIn == "absent"
}

func hasOnlyUnavailableDomains(item DailyInsightNarrativeCorpusCase) bool {
	snapshot, err := item.SnapshotForEvaluation()
	return err == nil && !health.HasEligibleDailyInsightNarrativeClaims(&snapshot, item.Locale) && narrativeCorpusHasOnlyUnavailableDomains(snapshot)
}

func syntheticLimitedHistoryCase(index int) DailyInsightNarrativeCorpusCase {
	item := DailyInsightNarrativeCorpusCase{
		ID: fmt.Sprintf("synthetic-%03d", index), Locale: "ru", Origin: DailyInsightNarrativeOriginSynthetic,
		Tags:     []string{DailyInsightNarrativeOriginSynthetic, "limited_history", "provisional_context", "no_checkin", "safety_control", "missing_data", "no_action", "locale_ru"},
		Scenario: DailyInsightNarrativeCorpusScenario{CheckIn: "absent"},
		Snapshot: health.DailyInsightSnapshot{Version: health.DailyInsightSnapshotVersion, Domains: []health.DailyInsightDomain{{
			Key: "sleep", DataState: "fresh", Confidence: "low",
			Insight: health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerProvisional, GapReason: "sleep_history_short", Fallback: true},
		}}},
	}
	item.Snapshot.DecisionID = ""
	item.Snapshot.Primary = health.DailyInsight{}
	item.VisibleB0Baseline = &health.DailyInsightNarrativeBaseline{Primary: "No current overall context.", Domains: []health.DailyInsightBaselineDomain{{Domain: "sleep", Observation: "History is still limited."}}}
	return item
}

func syntheticRecoveryEnergyConflictCase(index int, locale string, coverNormal, coverPositive bool) DailyInsightNarrativeCorpusCase {
	recoveryBand := ""
	if coverPositive {
		recoveryBand = "optimal"
	}
	sleepHours, usualSleep, shortNights := 5.8, 7.2, 3.0
	item := DailyInsightNarrativeCorpusCase{
		ID: fmt.Sprintf("synthetic-%03d", index), Locale: locale, Origin: DailyInsightNarrativeOriginSynthetic,
		Tags:     append(syntheticConflictTags(locale, coverNormal, coverPositive), "fresh_data", "action_options", "facts_sleep_recovery", "facts_sleep_energy", "facts_recovery_energy", "locale_"+locale),
		Scenario: DailyInsightNarrativeCorpusScenario{ConflictEvidenceIDs: map[string]string{"recovery": "evidence-recovery", "energy": "evidence-energy"}},
		// Keep the conflict packet narrative-eligible for energy. The direct
		// active-recovery action is deliberately server-only, so it cannot cover
		// the independently reviewed energy explanation slot.
		NarrativeSubjects: map[string]string{"energy": "rest"},
		Snapshot: health.DailyInsightSnapshot{Version: health.DailyInsightSnapshotVersion,
			Domains: []health.DailyInsightDomain{
				{Key: "sleep", DataState: "fresh", Confidence: "final", Insight: health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerConfirmedPersonal, ClaimID: "recent_sleep_below_reference", EvidenceIDs: []string{"evidence-sleep", "evidence-sleep-reference", "evidence-sleep-short-nights"}, NextStep: &health.DailyInsightAction{ID: "wind_down"}}},
				{Key: "recovery", Band: recoveryBand, DataState: "fresh", Confidence: "final", Insight: health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerFactual, ClaimID: "recovery_readiness_context", EvidenceIDs: []string{"evidence-recovery"}}},
				{Key: "energy", DataState: "fresh", Confidence: "final", Insight: health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerFactual, ClaimID: "energy_current_verdict_context", EvidenceIDs: []string{"evidence-energy"}}},
			},
			DecisionID: "review-decision",
			Primary:    health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerFactual, EvidenceIDs: []string{"evidence-recovery"}, NextStep: &health.DailyInsightAction{ID: "review-action"}},
			Evidence: []health.DailyInsightEvidence{
				{ID: "evidence-sleep", Domain: "sleep", DataState: "fresh", Confidence: "final", Value: &sleepHours, Unit: "h"},
				{ID: "evidence-sleep-reference", Domain: "sleep", DataState: "fresh", Confidence: "final", Value: &usualSleep, Unit: "h"},
				{ID: "evidence-sleep-short-nights", Domain: "sleep", DataState: "fresh", Confidence: "final", Value: &shortNights, Unit: "nights"},
				{ID: "evidence-recovery", Domain: "recovery", DataState: "fresh", Confidence: "final"},
				{ID: "evidence-energy", Domain: "energy", DataState: "fresh", Confidence: "final"},
			},
		},
	}
	item.Snapshot.DecisionID = ""
	item.Snapshot.Primary = health.DailyInsight{}
	item.VisibleB0Baseline = &health.DailyInsightNarrativeBaseline{Primary: "Today is set to a moderate pace.", Domains: []health.DailyInsightBaselineDomain{{Domain: "sleep", Observation: "Recent sleep is below your reference."}, {Domain: "recovery", Observation: "Recovery is available."}, {Domain: "energy", Observation: "Energy is available."}}}
	item.ActionOptions = []health.DailyInsightNarrativeAction{{ID: "wind_down", Text: "Try a calmer wind-down tonight."}}
	item.NarrativeFacts = []health.DailyInsightNarrativeFact{
		{ID: "sleep_recent_four_day_pattern", Domain: "sleep", Authority: "server_derived", Fresh: true, Statement: "Recent sleep has been shorter than your usual pattern.", DisplayValues: []string{"5.8", "7.2"}, EvidenceIDs: []string{"evidence-sleep"}},
		{ID: "readiness_current", Domain: "recovery", Authority: "server_derived", Fresh: true, Statement: "Recovery is holding up today.", EvidenceIDs: []string{"evidence-recovery"}},
		{ID: "energy_authoritative_state", Domain: "energy", Authority: "server_derived", Fresh: true, Statement: "Energy is available today, with some drain already visible.", EvidenceIDs: []string{"evidence-energy"}},
	}
	return item
}

func syntheticConflictTags(locale string, coverNormal, coverPositive bool) []string {
	tags := []string{DailyInsightNarrativeOriginSynthetic, "energy_recovery_conflict", "complete_sleep"}
	if locale == "en" {
		tags = append(tags, "mixed_sleep_baseline")
	}
	if coverNormal {
		tags = append(tags, "normal_context")
	}
	if coverPositive {
		tags = append(tags, "positive_context")
	}
	return tags
}

func syntheticLateSourceUpdateCase(index int) DailyInsightNarrativeCorpusCase {
	return DailyInsightNarrativeCorpusCase{
		ID: fmt.Sprintf("synthetic-%03d", index), Locale: "sr", Origin: DailyInsightNarrativeOriginSynthetic,
		Tags:     []string{DailyInsightNarrativeOriginSynthetic, "late_source_update", "incomplete_sleep", "no_data", "safety_control", "missing_data", "no_action", "locale_sr"},
		Scenario: DailyInsightNarrativeCorpusScenario{UpdateKind: "late_source_update"},
		Snapshot: health.DailyInsightSnapshot{Version: health.DailyInsightSnapshotVersion, Domains: []health.DailyInsightDomain{{
			Key: "sleep", DataState: "partial", Confidence: "provisional",
			Insight: health.DailyInsight{State: "insufficient_data", AnswerKind: health.DailyInsightAnswerDataGuidance, GapReason: "sleep_partial", Fallback: true},
		}}},
		VisibleB0Baseline: &health.DailyInsightNarrativeBaseline{Primary: "No current overall context.", Domains: []health.DailyInsightBaselineDomain{{Domain: "sleep", Observation: "Sleep data is incomplete."}}},
	}
}
