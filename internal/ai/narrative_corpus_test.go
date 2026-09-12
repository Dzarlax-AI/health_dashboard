package ai

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"health-receiver/internal/health"
)

func TestValidateDailyInsightNarrativeCorpusAcceptsCoveredFrozenSet(t *testing.T) {
	corpus := coveredNarrativeCorpus(t, 20)
	if err := ValidateDailyInsightNarrativeCorpus(corpus); err != nil {
		t.Fatalf("ValidateDailyInsightNarrativeCorpus: %v", err)
	}
}

func TestDailyInsightNarrativeCorpusHashIsStableAndTracksSemanticChanges(t *testing.T) {
	corpus := coveredNarrativeCorpus(t, 20)
	first, err := DailyInsightNarrativeCorpusHash(corpus)
	if err != nil {
		t.Fatalf("first corpus hash: %v", err)
	}
	second, err := DailyInsightNarrativeCorpusHash(corpus)
	if err != nil || first != second {
		t.Fatalf("stable corpus hash = %q, %q; err=%v", first, second, err)
	}
	corpus.Cases[0].Scenario.CheckIn = "absent"
	changed, err := DailyInsightNarrativeCorpusHash(corpus)
	if err != nil {
		t.Fatalf("changed corpus hash: %v", err)
	}
	if changed == first {
		t.Fatal("semantic corpus change retained the old hash")
	}
}

func TestValidateDailyInsightNarrativeCorpusRejectsIncompleteCoverage(t *testing.T) {
	corpus := coveredNarrativeCorpus(t, 20)
	corpus.Cases[0].Tags = nil
	if err := ValidateDailyInsightNarrativeCorpus(corpus); err == nil || !strings.Contains(err.Error(), "mixed_sleep_baseline") {
		t.Fatalf("validation error = %v, want missing state", err)
	}
}

func TestDailyInsightNarrativeFallbacksRemainServerOwned(t *testing.T) {
	snapshot := corpusSnapshot("2026-01-01")
	fallbacks := DailyInsightNarrativeFallbacks(snapshot, "en")
	if len(fallbacks) != 2 || fallbacks[0].Key != "overall" || fallbacks[0].Summary != "server_claim" || fallbacks[1].Summary != "server_claim" || !strings.Contains(fallbacks[1].Observation, "personal historical sleep reference") {
		t.Fatalf("fallbacks = %#v", fallbacks)
	}
}

func TestFrozenCorpusRestoresClosedEnergyNarrativeSubject(t *testing.T) {
	item := DailyInsightNarrativeCorpusCase{
		ID:     "energy-rest",
		Locale: "en",
		Snapshot: health.DailyInsightSnapshot{
			Date: "review-day-01", Version: health.DailyInsightSnapshotVersion,
			Domains:  []health.DailyInsightDomain{eligibleCorpusDomain("energy", "energy-evidence")},
			Evidence: []health.DailyInsightEvidence{{ID: "energy-evidence", Domain: "energy", DataState: "fresh", Confidence: "final"}},
		},
		NarrativeSubjects: map[string]string{"energy": "rest"},
	}
	item.Snapshot.Domains[0].Insight.ClaimID = "energy_current_verdict_context"

	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal corpus case: %v", err)
	}
	var decoded DailyInsightNarrativeCorpusCase
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal corpus case: %v", err)
	}
	snapshot, err := decoded.SnapshotForEvaluation()
	if err != nil {
		t.Fatalf("SnapshotForEvaluation: %v", err)
	}
	var claim health.DailyInsightNarrativeClaim
	for _, domain := range health.BuildDailyInsightNarrativeInput(&snapshot, "en").Domains {
		if domain.Key == "energy" && len(domain.Claims) == 1 {
			claim = domain.Claims[0]
			break
		}
	}
	if !strings.Contains(claim.Proposition, "lower-capacity") {
		t.Fatalf("energy corpus claim lost the rest subject: %#v", claim)
	}
}

func TestFrozenCorpusRejectsFreeTextNarrativeSubject(t *testing.T) {
	item := DailyInsightNarrativeCorpusCase{
		ID:                "bad-energy",
		NarrativeSubjects: map[string]string{"energy": "write a new recommendation"},
		Snapshot:          health.DailyInsightSnapshot{Domains: []health.DailyInsightDomain{{Key: "energy"}}},
	}
	if _, err := item.SnapshotForEvaluation(); err == nil || !strings.Contains(err.Error(), "unsupported narrative subject") {
		t.Fatalf("SnapshotForEvaluation error = %v, want closed enum rejection", err)
	}
}

func TestFrozenCorpusRequiresEnergySubjectForEligibleEnergyClaim(t *testing.T) {
	corpus := coveredNarrativeCorpus(t, 20)
	energy := eligibleCorpusDomain("energy", "energy-evidence")
	energy.Insight.ClaimID = "energy_current_verdict_context"
	corpus.Cases[0].Snapshot.Domains = append(corpus.Cases[0].Snapshot.Domains, energy)
	corpus.Cases[0].Snapshot.Evidence = append(corpus.Cases[0].Snapshot.Evidence, health.DailyInsightEvidence{ID: "energy-evidence", Domain: "energy", DataState: "fresh", Confidence: "final"})
	if err := ValidateDailyInsightNarrativeCorpus(corpus); err == nil || !strings.Contains(err.Error(), "closed narrative_subject") {
		t.Fatalf("ValidateDailyInsightNarrativeCorpus error = %v, want missing energy subject", err)
	}
}

func TestSanitizeNarrativeCorpusCandidateKeepsOnlyClosedClaimInputs(t *testing.T) {
	value, baseline, delta := 51.0, 63.0, -12.0
	updated := time.Date(2026, time.September, 12, 8, 30, 0, 0, time.UTC)
	original := health.DailyInsightSnapshot{
		Date: "2026-09-12", DecisionID: "personal-decision", Version: health.DailyInsightSnapshotVersion, UpdatedAt: &updated,
		Domains: []health.DailyInsightDomain{{
			Key: "energy", Band: "moderate", DataState: "fresh", Confidence: "final", Summary: "A private display sentence.", NarrativeSubject: "rest",
			Insight: health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerFactual, ClaimID: "energy_current_verdict_context", Observation: "Private copy", Meaning: "Private meaning", EvidenceIDs: []string{"private-energy-id"}},
		}},
		Evidence: []health.DailyInsightEvidence{{ID: "private-energy-id", Domain: "energy", ObservedAt: &updated, DataState: "fresh", Confidence: "final", Value: &value, Baseline: &baseline, Delta: &delta, Unit: "percent"}},
		Primary:  health.DailyInsight{EvidenceIDs: []string{"private-energy-id"}},
	}
	candidate := SanitizeDailyInsightNarrativeCorpusCandidate(original, "en", "candidate-01")
	encoded, err := json.Marshal(candidate)
	if err != nil {
		t.Fatalf("marshal sanitized candidate: %v", err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"2026-09-12", "personal-decision", "private-energy-id", "Private copy", "Private meaning", "51", "63", "-12", "percent"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("sanitized candidate leaked %q: %s", forbidden, text)
		}
	}
	restored, err := candidate.SnapshotForEvaluation()
	if err != nil {
		t.Fatalf("SnapshotForEvaluation: %v", err)
	}
	var claim health.DailyInsightNarrativeClaim
	for _, domain := range health.BuildDailyInsightNarrativeInput(&restored, "en").Domains {
		if domain.Key == "energy" && len(domain.Claims) == 1 {
			claim = domain.Claims[0]
			break
		}
	}
	if claim.ID != "energy_current_verdict_context" || !strings.Contains(claim.Proposition, "lower-capacity") || len(claim.EvidenceIDs) != 1 || claim.EvidenceIDs[0] != "evidence-01" {
		t.Fatalf("sanitized candidate changed the closed energy claim: %#v", claim)
	}
	if candidate.PrimaryMeaningID != "primary:energy:energy_current_verdict_context" {
		t.Fatalf("sanitized candidate primary meaning = %q", candidate.PrimaryMeaningID)
	}
}

func TestCheckDailyInsightNarrativeQualityGateRequiresAllRunsToBeUsefulAndSafe(t *testing.T) {
	corpus := coveredNarrativeCorpus(t, 20)
	output := reviewedEvaluation(t, corpus)

	gate, err := CheckDailyInsightNarrativeQualityGate(corpus, "frozen-hash", output)
	if err != nil {
		t.Fatalf("CheckDailyInsightNarrativeQualityGate: %v", err)
	}
	if !gate.Passed || gate.ImprovedCases != gate.EligibleCases || gate.EligibleImprovedPercent != 100 || gate.AllCasesImprovedPercent >= gate.EligibleImprovedPercent || gate.FallbackOnlyCases == 0 {
		t.Fatalf("unexpected passing gate: %#v", gate)
	}

	output.Cases[0].Runs[1].Review.Domains[0].Language = ""
	gate, err = CheckDailyInsightNarrativeQualityGate(corpus, "frozen-hash", output)
	if err != nil {
		t.Fatalf("check incomplete review: %v", err)
	}
	if gate.Passed || len(gate.UnreviewedRuns) != 1 || gate.ImprovedCases != gate.EligibleCases-1 || gate.EligibleImprovedPercent >= 100 {
		t.Fatalf("incomplete review passed: %#v", gate)
	}
}

func TestCheckDailyInsightNarrativeQualityGateUsesEligibleDenominatorButKeepsFallbackControls(t *testing.T) {
	corpus := coveredNarrativeCorpus(t, 20)
	output := reviewedEvaluation(t, corpus)
	gate, err := CheckDailyInsightNarrativeQualityGate(corpus, "frozen-hash", output)
	if err != nil {
		t.Fatalf("CheckDailyInsightNarrativeQualityGate: %v", err)
	}
	if !gate.Passed || gate.EligibleImprovedPercent != 100 || gate.AllCasesImprovedPercent >= 100 || gate.FallbackOnlyCases == 0 {
		t.Fatalf("eligible denominator or fallback controls were not retained: %#v", gate)
	}

	for index := range output.Cases {
		if output.Cases[index].Mode != "deterministic_fallback" {
			continue
		}
		output.Cases[index].Runs = []DailyInsightNarrativeEvaluationRun{{}}
		if _, err := CheckDailyInsightNarrativeQualityGate(corpus, "frozen-hash", output); err == nil || !strings.Contains(err.Error(), "fallback-only") {
			t.Fatalf("fallback provider output was accepted: %v", err)
		}
		return
	}
	t.Fatal("fixture has no fallback-only control")
}

func TestCheckDailyInsightNarrativeQualityGateRequiresAddedNonDuplicateMeaning(t *testing.T) {
	corpus := coveredNarrativeCorpus(t, 20)
	output := reviewedEvaluation(t, corpus)
	output.Cases[0].Runs[0].Review.Domains[0].ScreenDuplication = "hero"
	gate, err := CheckDailyInsightNarrativeQualityGate(corpus, "frozen-hash", output)
	if err != nil {
		t.Fatalf("CheckDailyInsightNarrativeQualityGate: %v", err)
	}
	if !gate.Passed || len(gate.UnreviewedRuns) != 0 || len(gate.SafetyViolations) != 0 || gate.ImprovedCases != gate.EligibleCases-1 {
		t.Fatalf("hero repetition was accepted as better than fallback: %#v", gate)
	}

	output = reviewedEvaluation(t, corpus)
	output.Cases[0].Runs[0].Review.Domains[0].ClaimFidelity = "fail"
	gate, err = CheckDailyInsightNarrativeQualityGate(corpus, "frozen-hash", output)
	if err != nil {
		t.Fatalf("CheckDailyInsightNarrativeQualityGate: %v", err)
	}
	if gate.Passed || len(gate.SafetyViolations) != 1 || !strings.Contains(gate.SafetyViolations[0], "claim-fidelity") {
		t.Fatalf("claim fidelity failure was not a safety violation: %#v", gate)
	}
}

func TestCheckDailyInsightNarrativeQualityGateReviewsPartialBundlesAndRequiresExplicitMeaningScore(t *testing.T) {
	corpus := coveredNarrativeCorpus(t, 20)
	output := reviewedEvaluation(t, corpus)
	found := false
	for caseIndex := range output.Cases {
		if len(output.Cases[caseIndex].Runs) == 0 {
			continue
		}
		run := &output.Cases[caseIndex].Runs[0]
		if len(run.Review.Domains) < 2 {
			continue
		}
		missing := run.Review.Domains[1].Key
		for domainIndex := range run.Narrative.Domains {
			if run.Narrative.Domains[domainIndex].Key == missing {
				run.Narrative.Domains[domainIndex].Section = nil
				break
			}
		}
		run.Review.Domains[1] = DailyInsightNarrativeDomainReview{Key: missing, OutputStatus: "null"}
		run.Review.Domains[0].Safety = "violation"
		found = true
		break
	}
	if !found {
		t.Fatal("fixture has no multi-domain eligible candidate")
	}
	gate, err := CheckDailyInsightNarrativeQualityGate(corpus, "frozen-hash", output)
	if err != nil {
		t.Fatalf("CheckDailyInsightNarrativeQualityGate: %v", err)
	}
	if gate.Passed || len(gate.SafetyViolations) != 1 || !strings.Contains(gate.SafetyViolations[0], "reviewer marked") {
		t.Fatalf("partial bundle hid a reviewed safety violation: %#v", gate)
	}

	output = reviewedEvaluation(t, corpus)
	output.Cases[0].Runs[0].Review.Domains[0].AddedMeaning = nil
	gate, err = CheckDailyInsightNarrativeQualityGate(corpus, "frozen-hash", output)
	if err != nil {
		t.Fatalf("CheckDailyInsightNarrativeQualityGate: %v", err)
	}
	if gate.Passed || len(gate.UnreviewedRuns) != 1 || !strings.Contains(gate.UnreviewedRuns[0], "incomplete domain review") {
		t.Fatalf("missing added-meaning score was accepted: %#v", gate)
	}
}

func TestCheckDailyInsightNarrativeQualityGateRejectsChangedFrozenFallback(t *testing.T) {
	corpus := coveredNarrativeCorpus(t, 20)
	output := reviewedEvaluation(t, corpus)
	output.Cases[0].Fallbacks = nil
	if _, err := CheckDailyInsightNarrativeQualityGate(corpus, "frozen-hash", output); err == nil || !strings.Contains(err.Error(), "fallback baseline") {
		t.Fatalf("quality gate error = %v, want frozen fallback mismatch", err)
	}
}

func TestCheckDailyInsightNarrativeQualityGateRejectsAChangedStaticPromptContract(t *testing.T) {
	corpus := coveredNarrativeCorpus(t, 20)
	output := reviewedEvaluation(t, corpus)
	output.ReviewFingerprint = strings.Repeat("a", 64)
	if _, err := CheckDailyInsightNarrativeQualityGate(corpus, "frozen-hash", output); err == nil || !strings.Contains(err.Error(), "prompt, schema") {
		t.Fatalf("quality gate error = %v, want static contract mismatch", err)
	}
}

func TestValidateDailyInsightNarrativeCorpusRejectsTagWithoutMatchingSnapshotState(t *testing.T) {
	corpus := coveredNarrativeCorpus(t, 20)
	corpus.Cases[2].Snapshot.Domains[0].DataState = "fresh"
	if err := ValidateDailyInsightNarrativeCorpus(corpus); err == nil || !strings.Contains(err.Error(), "incomplete_sleep") {
		t.Fatalf("validation error = %v, want incomplete_sleep mismatch", err)
	}
}

func TestValidateDailyInsightNarrativeCorpusRequiresEligibleCoverageForEveryLocale(t *testing.T) {
	corpus := coveredNarrativeCorpus(t, 20)
	for index := range corpus.Cases {
		corpus.Cases[index].Locale = "en"
	}
	if err := ValidateDailyInsightNarrativeCorpus(corpus); err == nil || !strings.Contains(err.Error(), "ru") || !strings.Contains(err.Error(), "sr") {
		t.Fatalf("validation error = %v, want missing eligible locale coverage", err)
	}
}

func TestValidateDailyInsightNarrativeCorpusRequiresEveryClaimInEveryLocale(t *testing.T) {
	corpus := coveredNarrativeCorpus(t, 20)
	for index := range corpus.Cases {
		if corpus.Cases[index].Locale != "ru" {
			continue
		}
		filtered := corpus.Cases[index].Snapshot.Domains[:0]
		for _, domain := range corpus.Cases[index].Snapshot.Domains {
			if domain.Insight.ClaimID == "recovery_readiness_context" {
				continue
			}
			filtered = append(filtered, domain)
		}
		corpus.Cases[index].Snapshot.Domains = filtered
	}
	if err := ValidateDailyInsightNarrativeCorpus(corpus); err == nil || !strings.Contains(err.Error(), "ru:recovery_readiness_context") {
		t.Fatalf("validation error = %v, want missing RU recovery claim coverage", err)
	}
}

func TestValidateDailyInsightNarrativeCorpusBoundsAndLabelsSyntheticFixtures(t *testing.T) {
	corpus := coveredNarrativeCorpus(t, 20)
	corpus.Cases[0].Origin = DailyInsightNarrativeOriginSynthetic
	if err := ValidateDailyInsightNarrativeCorpus(corpus); err == nil || !strings.Contains(err.Error(), "synthetic_controlled tag") {
		t.Fatalf("validation error = %v, want missing synthetic label", err)
	}

	corpus.Cases[0].Tags = append(corpus.Cases[0].Tags, DailyInsightNarrativeOriginSynthetic)
	if err := ValidateDailyInsightNarrativeCorpus(corpus); err != nil {
		t.Fatalf("labelled synthetic fixture rejected: %v", err)
	}

	for index := 1; index <= DailyInsightNarrativeCorpusMaxSyntheticCases; index++ {
		corpus.Cases[index].Origin = DailyInsightNarrativeOriginSynthetic
		corpus.Cases[index].Tags = append(corpus.Cases[index].Tags, DailyInsightNarrativeOriginSynthetic)
	}
	if err := ValidateDailyInsightNarrativeCorpus(corpus); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("validation error = %v, want synthetic cap", err)
	}
}

func TestValidateDailyInsightNarrativeCorpusRejectsSyntheticFixtureWithoutRequiredState(t *testing.T) {
	corpus := coveredNarrativeCorpus(t, 20)
	corpus.Cases[10].Origin = DailyInsightNarrativeOriginSynthetic
	corpus.Cases[10].Tags = []string{"ordinary", DailyInsightNarrativeOriginSynthetic}
	if err := ValidateDailyInsightNarrativeCorpus(corpus); err == nil || !strings.Contains(err.Error(), "required product state") {
		t.Fatalf("validation error = %v, want missing required synthetic state", err)
	}
}

func TestBuildDailyInsightNarrativeCorpusScaffoldUsesObservedCoverageAndExplicitSyntheticEdges(t *testing.T) {
	export := DailyInsightNarrativeCandidateExport{Version: "daily-insight-narrative-candidates-v1"}
	for index := 0; index < 17; index++ {
		item := DailyInsightNarrativeCorpusCase{
			ID: fmt.Sprintf("candidate-%03d", index+1), Locale: []string{"en", "ru", "sr"}[index%3], Snapshot: corpusSnapshot("review-day"),
		}
		if index == 1 {
			item.Snapshot.Domains[0].DataState, item.Snapshot.Domains[0].Confidence = "partial", "provisional"
			item.Snapshot.Domains[0].Insight = health.DailyInsight{State: "insufficient_data", AnswerKind: health.DailyInsightAnswerDataGuidance, GapReason: "sleep_partial", Fallback: true}
		}
		if index == 2 {
			item.Snapshot.Domains[0].DataState, item.Snapshot.Domains[0].Confidence = "missing", ""
			item.Snapshot.Domains[0].Insight = health.DailyInsight{State: "insufficient_data", AnswerKind: health.DailyInsightAnswerDataGuidance, GapReason: "sleep_missing", Fallback: true}
		}
		if index == 3 {
			item.Scenario.CheckIn = "absent"
		}
		item.PrimaryMeaningID = narrativeCorpusPrimaryMeaningForDomain(item.Snapshot.Domains[0])
		export.Candidates = append(export.Candidates, DailyInsightNarrativeCandidate{DailyInsightNarrativeCorpusCase: item})
	}

	corpus, err := BuildDailyInsightNarrativeCorpusScaffold(export)
	if err != nil {
		t.Fatalf("BuildDailyInsightNarrativeCorpusScaffold: %v", err)
	}
	if len(corpus.Cases) != 20 {
		t.Fatalf("case count = %d, want 20", len(corpus.Cases))
	}
	if err := ValidateDailyInsightNarrativeCorpus(corpus); err != nil {
		t.Fatalf("ValidateDailyInsightNarrativeCorpus: %v", err)
	}
	var observed, synthetic int
	for _, item := range corpus.Cases {
		switch item.Origin {
		case DailyInsightNarrativeOriginObserved:
			observed++
		case DailyInsightNarrativeOriginSynthetic:
			synthetic++
		}
	}
	if observed != 15 || synthetic != 5 {
		t.Fatalf("origins observed=%d synthetic=%d, want 15/5", observed, synthetic)
	}
	if baselineCases := countCasesMatching(corpus.Cases, hasConfirmedSleepBaselineClaim); baselineCases < 6 {
		t.Fatalf("confirmed sleep baseline cases = %d, want at least 6", baselineCases)
	}
}

func TestBuildDailyInsightNarrativeReviewPacketUsesClosedClaimsAndFallbackReferences(t *testing.T) {
	corpus := coveredNarrativeCorpus(t, 20)
	packet, err := BuildDailyInsightNarrativeReviewPacket(corpus)
	if err != nil {
		t.Fatalf("BuildDailyInsightNarrativeReviewPacket: %v", err)
	}
	if packet.Version != "daily-insight-narrative-review-packet-v4" || len(packet.Cases) != len(corpus.Cases) {
		t.Fatalf("packet = %#v", packet)
	}
	if len(packet.Cases[0].Claims) != 2 || packet.Cases[0].Claims[0].ID != "overall_daily_decision_context" || packet.Cases[0].Claims[1].ID != "recent_sleep_below_reference" {
		t.Fatalf("claims = %#v", packet.Cases[0].Claims)
	}
	if len(packet.Cases[0].Fallbacks) != 2 || packet.Cases[0].Fallbacks[0].Summary != "server_claim" {
		t.Fatalf("fallbacks = %#v", packet.Cases[0].Fallbacks)
	}
	if len(packet.Cases[0].QualifierDefinitions) == 0 || len(packet.Cases[0].ScreenBaseline.DisplayedMeanings) < 2 || !packet.Cases[0].ScreenBaseline.PrimaryServerOwned || !packet.Cases[0].ScreenBaseline.MetricValuesExcluded || !packet.Cases[0].ScreenBaseline.ActionContentExcluded {
		t.Fatalf("review packet lacks bounded screen context: %#v", packet.Cases[0])
	}
}

func coveredNarrativeCorpus(t *testing.T, count int) DailyInsightNarrativeCorpus {
	t.Helper()
	cases := make([]DailyInsightNarrativeCorpusCase, 0, count)
	for index := 0; index < count; index++ {
		tags := []string{"ordinary"}
		snapshot := corpusSnapshot(fmt.Sprintf("2026-01-%02d", index+1))
		scenario := DailyInsightNarrativeCorpusScenario{}
		if index < len(RequiredDailyInsightNarrativeCorpusTags) {
			tags = append(tags, RequiredDailyInsightNarrativeCorpusTags[index])
			switch RequiredDailyInsightNarrativeCorpusTags[index] {
			case "incomplete_sleep":
				snapshot.Domains[0].DataState, snapshot.Domains[0].Confidence = "partial", "provisional"
				snapshot.Domains[0].Insight = health.DailyInsight{State: "insufficient_data", AnswerKind: health.DailyInsightAnswerDataGuidance, GapReason: "sleep_partial", Observation: "Sleep capture is incomplete.", Meaning: "Use the available context carefully.", EvidenceIDs: []string{"sleep-evidence"}, Fallback: true}
				snapshot.Domains = append(snapshot.Domains, eligibleCorpusDomain("recovery", "recovery-evidence"))
				snapshot.Evidence = append(snapshot.Evidence, health.DailyInsightEvidence{ID: "recovery-evidence", Domain: "recovery", DataState: "fresh", Confidence: "final"})
			case "limited_history":
				snapshot.Domains[0].Insight.AnswerKind, snapshot.Domains[0].Insight.ClaimID = health.DailyInsightAnswerProvisional, ""
			case "no_checkin":
				scenario.CheckIn = "absent"
			case "energy_recovery_conflict":
				snapshot.Domains = append(snapshot.Domains, narrativeClaimCorpusDomain("recovery", "recovery_readiness_context", "recovery-evidence", ""), narrativeClaimCorpusDomain("energy", "energy_current_verdict_context", "energy-evidence", "active_recovery"))
				snapshot.Evidence = append(snapshot.Evidence,
					health.DailyInsightEvidence{ID: "recovery-evidence", Domain: "recovery", DataState: "fresh", Confidence: "final"},
					health.DailyInsightEvidence{ID: "energy-evidence", Domain: "energy", DataState: "fresh", Confidence: "final"},
				)
				scenario.ConflictEvidenceIDs = map[string]string{"recovery": "recovery-evidence", "energy": "energy-evidence"}
			case "late_source_update":
				updated := time.Date(2026, 1, index+1, 18, 30, 0, 0, time.UTC)
				snapshot.UpdatedAt, scenario.UpdateKind = &updated, "late_source_update"
			case "no_data":
				snapshot.Domains[0].DataState, snapshot.Domains[0].Confidence = "missing", ""
				snapshot.Domains[0].Insight = health.DailyInsight{State: "insufficient_data", AnswerKind: health.DailyInsightAnswerDataGuidance, GapReason: "sleep_missing", Remediation: "sync_sleep", Observation: "Sleep data is unavailable.", Meaning: "There is no sleep context to interpret yet.", EvidenceIDs: []string{"sleep-evidence"}, Fallback: true}
			}
		}
		item := DailyInsightNarrativeCorpusCase{
			ID: fmt.Sprintf("case-%02d", index), Locale: []string{"en", "ru", "sr"}[index%3], Tags: tags, Scenario: scenario,
			Snapshot: snapshot, PrimaryNarrativeSubject: "moderate",
		}
		item.PrimaryMeaningID = narrativeCorpusPrimaryMeaningForDomain(snapshot.Domains[0])
		if index == 5 {
			item.NarrativeSubjects = map[string]string{"energy": "active_recovery"}
		}
		cases = append(cases, item)
	}
	// The frozen review corpus must exercise every server-supported claim in
	// every locale. These are test-only controlled packets, not observed data.
	for _, index := range []int{8, 9, 10} {
		appendCorpusTestDomain(&cases[index], narrativeClaimCorpusDomain("recovery", "recovery_readiness_context", fmt.Sprintf("recovery-evidence-%02d", index), ""))
	}
	appendCorpusTestDomain(&cases[9], narrativeClaimCorpusDomain("energy", "energy_current_verdict_context", "energy-evidence-en", "rest"))
	cases[9].NarrativeSubjects = map[string]string{"energy": "rest"}
	appendCorpusTestDomain(&cases[10], narrativeClaimCorpusDomain("energy", "energy_current_verdict_context", "energy-evidence-ru", "push_hard"))
	cases[10].NarrativeSubjects = map[string]string{"energy": "push_hard"}
	return DailyInsightNarrativeCorpus{Version: "daily-insight-narrative-corpus-v1", Cases: cases}
}

func appendCorpusTestDomain(item *DailyInsightNarrativeCorpusCase, domain health.DailyInsightDomain) {
	item.Snapshot.Domains = append(item.Snapshot.Domains, domain)
	for _, id := range domain.Insight.EvidenceIDs {
		item.Snapshot.Evidence = append(item.Snapshot.Evidence, health.DailyInsightEvidence{ID: id, Domain: domain.Key, DataState: domain.DataState, Confidence: domain.Confidence})
	}
}

func narrativeClaimCorpusDomain(key, claimID, evidenceID, subject string) health.DailyInsightDomain {
	return health.DailyInsightDomain{
		Key: key, DataState: "fresh", Confidence: "final", NarrativeSubject: subject,
		Insight: health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerFactual, ClaimID: claimID, EvidenceIDs: []string{evidenceID}},
	}
}

func eligibleCorpusDomain(key, evidenceID string) health.DailyInsightDomain {
	return health.DailyInsightDomain{
		Key: key, DataState: "fresh", Confidence: "final", Summary: "Server summary",
		Insight: health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerFactual, ClaimID: key + "_current_context", Observation: "Server fallback", Meaning: "Server meaning", EvidenceIDs: []string{evidenceID}},
	}
}

func corpusSnapshot(date string) health.DailyInsightSnapshot {
	return health.DailyInsightSnapshot{
		Date: date, DecisionID: "review-decision", Version: health.DailyInsightSnapshotVersion,
		Primary:  health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerFactual, EvidenceIDs: []string{"sleep-evidence"}, NextStep: &health.DailyInsightAction{ID: "review-action"}, NarrativeSubject: "moderate"},
		Evidence: []health.DailyInsightEvidence{{ID: "sleep-evidence", Domain: "sleep", DataState: "fresh", Confidence: "final"}},
		Domains: []health.DailyInsightDomain{{
			Key: "sleep", DataState: "fresh", Confidence: "final", Summary: "Server summary",
			Insight: health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerConfirmedPersonal, ClaimID: "recent_sleep_below_reference", Observation: "Server fallback", Meaning: "Server meaning", EvidenceIDs: []string{"sleep-evidence"}},
		}},
	}
}

func reviewedEvaluation(t *testing.T, corpus DailyInsightNarrativeCorpus) DailyInsightNarrativeEvaluationOutput {
	t.Helper()
	identity := DailyInsightNarrativeCurrentReviewIdentity()
	output := DailyInsightNarrativeEvaluationOutput{
		Version: "daily-insight-narrative-evaluation-v2", CorpusHash: "frozen-hash", RunsPerCase: 3,
		PromptRevision: identity.PromptRevision, ClaimPacketVersion: identity.ClaimPacketVersion,
		NarrativeVersion: identity.NarrativeVersion, ReviewFingerprint: identity.Fingerprint,
		Cases: make([]DailyInsightNarrativeEvaluationCase, 0, len(corpus.Cases)),
	}
	for _, item := range corpus.Cases {
		snapshot, err := item.SnapshotForEvaluation()
		if err != nil {
			t.Fatal(err)
		}
		if !health.HasEligibleDailyInsightNarrativeClaims(&snapshot, item.Locale) {
			output.Cases = append(output.Cases, DailyInsightNarrativeEvaluationCase{ID: item.ID, Locale: item.Locale, Tags: append([]string(nil), item.Tags...), Mode: "deterministic_fallback", Fallbacks: DailyInsightNarrativeFallbacks(snapshot, item.Locale)})
			continue
		}
		candidate := corpusNarrative(snapshot, item.Locale)
		output.Cases = append(output.Cases, DailyInsightNarrativeEvaluationCase{
			ID: item.ID, Locale: item.Locale, Tags: append([]string(nil), item.Tags...), Mode: "narrative_candidate", Fallbacks: DailyInsightNarrativeFallbacks(snapshot, item.Locale),
			Runs: []DailyInsightNarrativeEvaluationRun{
				{Narrative: &candidate, Review: DailyInsightNarrativeRunReview{Domains: reviewedDomainReviews(snapshot, item.Locale)}},
				{Narrative: &candidate, Review: DailyInsightNarrativeRunReview{Domains: reviewedDomainReviews(snapshot, item.Locale)}},
				{Narrative: &candidate, Review: DailyInsightNarrativeRunReview{Domains: reviewedDomainReviews(snapshot, item.Locale)}},
			},
		})
	}
	return output
}

func reviewedDomainReviews(snapshot health.DailyInsightSnapshot, locale string) []DailyInsightNarrativeDomainReview {
	input := dailyInsightNarrativeReviewInputs(snapshot, locale)
	reviews := make([]DailyInsightNarrativeDomainReview, 0, len(input))
	for _, domain := range input {
		if len(domain.Claims) == 0 {
			continue
		}
		reviews = append(reviews, DailyInsightNarrativeDomainReview{
			Key: domain.Key, OutputStatus: "valid", ClaimFidelity: "pass", QualifierFidelity: "pass", Safety: "safe",
			AddedMeaning: intPointer(2), ScreenDuplication: "none", Language: "pass", ReviewReason: "Adds a permitted interpretation without repeating the screen.",
		})
	}
	return reviews
}

func intPointer(value int) *int { return &value }

func corpusNarrative(snapshot health.DailyInsightSnapshot, locale string) health.DailyInsightNarrative {
	text := "This personal recent pattern gives the current day a little more context."
	if locale == "ru" {
		text = "Этот недавний личный паттерн даёт сегодняшнему дню чуть больше контекста."
	}
	if locale == "sr" {
		text = "Ovaj lični nedavni obrazac daje današnjem danu malo više konteksta."
	}
	input := dailyInsightNarrativeReviewInputs(snapshot, locale)
	domains := make([]health.DailyInsightNarrativeDomain, 0, 3)
	var overall *health.DailyInsightNarrativeSection
	for _, domain := range input {
		candidate := health.DailyInsightNarrativeDomain{Key: domain.Key}
		if len(domain.Claims) > 0 {
			claimIDs, qualifierIDs := make([]string, 0, len(domain.Claims)), []string{}
			for _, claim := range domain.Claims {
				claimIDs = append(claimIDs, claim.ID)
				qualifierIDs = append(qualifierIDs, claim.RequiredQualifierIDs...)
			}
			candidate.Section = &health.DailyInsightNarrativeSection{Sentences: []health.DailyInsightNarrativeSentence{{Text: text, ClaimIDs: claimIDs, QualifierIDs: qualifierIDs}}}
		}
		if domain.Key == health.DailyInsightNarrativeOverallSlot {
			overall = candidate.Section
		} else {
			domains = append(domains, candidate)
		}
	}
	return health.DailyInsightNarrative{Version: health.DailyInsightNarrativeVersion, Locale: locale, Overall: overall, Domains: domains}
}
