package health

import (
	"strings"
	"testing"
)

func TestBuildDailyInsightSnapshotSeparatesFactsFromInterpretations(t *testing.T) {
	latestSleep := 7.1
	sleepScore := 77
	got := BuildDailyInsightSnapshot(&BriefingResponse{
		Date:                "2026-09-10",
		ReadinessToday:      75,
		ReadinessTodayLabel: "Умеренно",
		ReadinessTip:        "Небольшое отклонение от нормы. Умеренная активность — хороший выбор.",
		Sleep:               &SleepAnalysis{LatestDate: "2026-09-10", LatestTotal: &latestSleep, TotalAvg: 6.7},
		SleepQuality:        &SleepQualityBreakdown{ScorePct: &sleepScore, Confidence: SleepQualityConfidenceFinal},
		EnergyBank:          &EnergyBank{Current: 51, Capacity: 84, ActionVerdict: "moderate", VerdictReason: "Резерв приличный, маркеры стресса чистые — нормальный тренировочный день ок."},
	}, "ru")
	if got == nil || len(got.Domains) != 3 {
		t.Fatalf("snapshot = %#v, want three domains", got)
	}

	tests := []struct {
		key, wantFact, wantMeaning string
	}{
		{"sleep", "Продолжительность сна — 7.1 ч.", "На 0.4 ч дольше вашего среднего."},
		{"recovery", "Готовность — 75%: Умеренно.", "Небольшое отклонение от нормы."},
		{"energy", "Запас энергии — 51 из 84.", "Резерв приличный, маркеры стресса чистые"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			var domain *DailyInsightDomain
			for index := range got.Domains {
				if got.Domains[index].Key == tt.key {
					domain = &got.Domains[index]
					break
				}
			}
			if domain == nil {
				t.Fatal("domain missing")
			}
			if domain.Summary != tt.wantFact {
				t.Fatalf("summary = %q, want %q", domain.Summary, tt.wantFact)
			}
			if !strings.Contains(domain.Insight.Observation, tt.wantMeaning) {
				t.Fatalf("observation = %q, want %q", domain.Insight.Observation, tt.wantMeaning)
			}
			if domain.Summary == domain.Insight.Observation {
				t.Fatalf("summary and observation duplicate: %q", domain.Summary)
			}
		})
	}
}

func TestBuildDailyInsightSnapshotUsesFactualSleepContextWithoutBaseline(t *testing.T) {
	latestSleep := 7.1
	got := BuildDailyInsightSnapshot(&BriefingResponse{
		Date:  "2026-09-10",
		Sleep: &SleepAnalysis{LatestDate: "2026-09-10", LatestTotal: &latestSleep},
	}, "en")
	if got == nil {
		t.Fatal("snapshot is nil")
	}
	for _, domain := range got.Domains {
		if domain.Key != "sleep" {
			continue
		}
		if domain.Summary != "Sleep duration was 7.1 hours." {
			t.Fatalf("summary = %q", domain.Summary)
		}
		if domain.Insight.AnswerKind != DailyInsightAnswerProvisional {
			t.Fatalf("answer kind = %q", domain.Insight.AnswerKind)
		}
		if domain.Insight.Observation != "Last night is part of today’s context; a personal comparison will appear as more history accumulates." {
			t.Fatalf("observation = %q", domain.Insight.Observation)
		}
		return
	}
	t.Fatal("sleep domain missing")
}

func TestBuildDailyInsightSnapshotPreservesZeroValuedFacts(t *testing.T) {
	got := BuildDailyInsightSnapshot(&BriefingResponse{
		Date:                "2026-09-10",
		ReadinessToday:      0,
		ReadinessTodayLabel: "Low",
		ReadinessServing:    &ReadinessServingState{Status: ReadinessServingFresh, Confidence: ReadinessConfidenceFinal},
		EnergyBank:          &EnergyBank{Current: 0, Capacity: 0, ActionVerdict: "rest", VerdictReason: "The reserve is depleted."},
	}, "en")

	if summary := dailyInsightDomain(t, got, "recovery").Summary; summary != "Readiness is 0%: Low." {
		t.Fatalf("recovery summary = %q", summary)
	}
	if summary := dailyInsightDomain(t, got, "energy").Summary; summary != "Energy reserve is 0 of 0." {
		t.Fatalf("energy summary = %q", summary)
	}
}

func TestBuildDailyInsightSnapshotExplainsRecoveryDataLimits(t *testing.T) {
	for _, status := range []string{
		ReadinessServingMissing,
		ReadinessServingStale,
		ReadinessServingDataAccruing,
		ReadinessServingLowCoverage,
	} {
		t.Run(status, func(t *testing.T) {
			got := BuildDailyInsightSnapshot(&BriefingResponse{
				Date:                "2026-09-10",
				ReadinessToday:      65,
				ReadinessTodayLabel: "Moderate",
				ReadinessTip:        "Keep the effort controlled.",
				ReadinessServing:    &ReadinessServingState{Status: status, Confidence: ReadinessConfidenceLow},
			}, "en")
			domain := dailyInsightDomain(t, got, "recovery")
			if domain.Insight.State != "insufficient_data" {
				t.Fatalf("state = %q", domain.Insight.State)
			}
			if domain.Insight.AnswerKind != DailyInsightAnswerDataGuidance {
				t.Fatalf("answer kind = %q", domain.Insight.AnswerKind)
			}
			if domain.Insight.Observation == "" || domain.Insight.Meaning == "" {
				t.Fatalf("data guidance is incomplete: %#v", domain.Insight)
			}
		})
	}

	got := BuildDailyInsightSnapshot(&BriefingResponse{
		Date:                "2026-09-10",
		ReadinessToday:      65,
		ReadinessTodayLabel: "Moderate",
		ReadinessTip:        "Keep the effort controlled.",
		ReadinessServing:    &ReadinessServingState{Status: ReadinessServingCapped, Confidence: ReadinessConfidenceProvisional},
	}, "en")
	domain := dailyInsightDomain(t, got, "recovery")
	if domain.Insight.State != "insight" || domain.Insight.Observation != "Keep the effort controlled." {
		t.Fatalf("capped recovery = %#v", domain.Insight)
	}
}

func TestBuildDailyInsightSnapshotAlwaysProvidesAReadableAnswer(t *testing.T) {
	got := BuildDailyInsightSnapshot(&BriefingResponse{Date: "2026-09-10"}, "en")
	if got == nil {
		t.Fatal("snapshot is nil")
	}
	if got.Primary.Title == "" || got.Primary.Observation == "" || got.Primary.Meaning == "" {
		t.Fatalf("primary is incomplete: %#v", got.Primary)
	}
	for _, domain := range got.Domains {
		if domain.Insight.Title == "" || domain.Insight.Observation == "" || domain.Insight.Meaning == "" {
			t.Fatalf("%s answer is incomplete: %#v", domain.Key, domain.Insight)
		}
		switch domain.Insight.AnswerKind {
		case DailyInsightAnswerConfirmedPersonal, DailyInsightAnswerProvisional, DailyInsightAnswerFactual, DailyInsightAnswerDataGuidance:
		default:
			t.Fatalf("%s answer kind = %q", domain.Key, domain.Insight.AnswerKind)
		}
	}
}

func TestDailyInsightMaterialHashIncludesAnswerPolicy(t *testing.T) {
	base := &DailyInsightSnapshot{
		Date: "2026-09-10", Version: DailyInsightSnapshotVersion,
		Primary: DailyInsight{State: "insight", AnswerKind: DailyInsightAnswerFactual},
		Domains: []DailyInsightDomain{{Key: "sleep", Insight: DailyInsight{State: "insight", AnswerKind: DailyInsightAnswerFactual}}},
	}
	changed := *base
	changed.Primary = base.Primary
	changed.Primary.AnswerKind = DailyInsightAnswerDataGuidance
	if DailyInsightMaterialHash(base) == DailyInsightMaterialHash(&changed) {
		t.Fatal("answer kind must invalidate the material hash")
	}

	changed = *base
	changed.Domains = append([]DailyInsightDomain(nil), base.Domains...)
	changed.Domains[0].Insight = base.Domains[0].Insight
	changed.Domains[0].Insight.Remediation = "sync_sleep_data"
	if DailyInsightMaterialHash(base) == DailyInsightMaterialHash(&changed) {
		t.Fatal("remediation must invalidate the material hash")
	}
}

func TestApplyRecentSleepBelowReferenceAddsOnlySleepAction(t *testing.T) {
	base := BuildDailyInsightSnapshot(&BriefingResponse{Date: "2026-09-10"}, "en")
	got := ApplyRecentSleepBelowReference(base, RecentSleepBelowReference{State: RecentSleepClaimTrue, ReferenceHours: 7.8, CurrentShortDays: 3, ActionEvent: true}, "en")
	sleep := dailyInsightDomain(t, got, "sleep")
	if sleep.Insight.AnswerKind != DailyInsightAnswerConfirmedPersonal || sleep.Insight.ClaimID != "recent_sleep_below_reference" || sleep.Insight.NextStep == nil || sleep.Insight.NextStep.ID != "wind_down" {
		t.Fatalf("sleep B0 claim = %#v", sleep.Insight)
	}
	if got.Primary.NextStep != base.Primary.NextStep {
		t.Fatalf("sleep action changed primary decision: before=%#v after=%#v", base.Primary.NextStep, got.Primary.NextStep)
	}
	if got.Evidence[len(got.Evidence)-2].ID != "sleep_recent_reference" || got.Evidence[len(got.Evidence)-1].ID != "sleep_recent_short_nights" || len(sleep.Insight.EvidenceIDs) != 2 {
		t.Fatalf("sleep B0 evidence = %#v, ids=%#v", got.Evidence, sleep.Insight.EvidenceIDs)
	}
	if dailyInsightDomain(t, got, "recovery").Insight.NextStep != nil || dailyInsightDomain(t, got, "energy").Insight.NextStep != nil {
		t.Fatal("sleep action leaked into another domain")
	}
	if DailyInsightMaterialHash(base) == DailyInsightMaterialHash(got) {
		t.Fatal("canonical sleep claim must invalidate the material hash")
	}
}

func TestDailyInsightNarrativeKeepsFallbackAndRejectsOnlyUnsafeDomain(t *testing.T) {
	latestSleep := 7.2
	base := BuildDailyInsightSnapshot(&BriefingResponse{
		Date: "2026-09-12", Sleep: &SleepAnalysis{LatestDate: "2026-09-12", LatestTotal: &latestSleep, TotalAvg: 6.8},
		ReadinessToday: 70, ReadinessTodayBand: "low", ReadinessTodayLabel: "Moderate", ReadinessServing: &ReadinessServingState{Status: ReadinessServingFresh, Confidence: ReadinessConfidenceFinal},
		EnergyBank: &EnergyBank{Current: 56, Capacity: 80, ActionVerdict: "moderate", VerdictReason: "Current reserve is available."},
	}, "en")
	snapshot := ApplyRecentSleepBelowReference(base, RecentSleepBelowReference{State: RecentSleepClaimTrue}, "en")
	input := BuildDailyInsightNarrativeInput(snapshot, "en")
	if len(input.Domains) != 3 || input.Domains[0].Claims[0].ID != "recent_sleep_below_reference" || len(input.Domains[1].Claims) != 0 || len(input.Domains[2].Claims) != 0 {
		t.Fatalf("claim packet = %#v", input)
	}
	if got := input.Domains[0].Claims[0].Proposition; got != "Several recent nights were shorter than the personal historical sleep reference." {
		t.Fatalf("B0 narrative proposition = %q, want the server-selected multi-night claim", got)
	}
	if strings.Contains(input.Domains[0].Claims[0].Proposition, snapshot.Domains[0].Summary) {
		t.Fatalf("claim packet leaked display summary: %#v", input.Domains[0])
	}

	validSleep := &DailyInsightNarrativeSection{Sentences: []DailyInsightNarrativeSentence{{
		Text: "Recent nights were shorter than your usual sleep rhythm, so the pattern matters more than a single night.", ClaimIDs: []string{"recent_sleep_below_reference"}, QualifierIDs: []string{"personal_pattern", "current_context"}, MeaningIDs: []string{"sleep_pattern_not_single_night"},
	}}}
	unsafeRecovery := &DailyInsightNarrativeSection{Sentences: []DailyInsightNarrativeSentence{{
		Text: "Today is 100% safe.", ClaimIDs: []string{"recovery_current_context"}, QualifierIDs: []string{"current_context"}, MeaningIDs: []string{"recovery_pacing_not_verdict"},
	}}}
	narrative := DailyInsightNarrative{Version: DailyInsightNarrativeVersion, Locale: "en", Domains: []DailyInsightNarrativeDomain{
		{Key: "sleep", Section: validSleep}, {Key: "recovery", Section: unsafeRecovery}, {Key: "energy", Section: nil},
	}}
	validated, invalid, err := ValidateDailyInsightNarrative(snapshot, "en", narrative)
	if err != nil {
		t.Fatalf("ValidateDailyInsightNarrative: %v", err)
	}
	if invalid["recovery"] == "" || validated.Domains[0].Section == nil || validated.Domains[1].Section != nil {
		t.Fatalf("partial validation = %#v, invalid=%#v", validated, invalid)
	}
	rendered, err := ApplyDailyInsightNarrative(snapshot, narrative)
	if err != nil {
		t.Fatalf("ApplyDailyInsightNarrative: %v", err)
	}
	sleep := dailyInsightDomain(t, rendered, "sleep").Insight
	recovery := dailyInsightDomain(t, rendered, "recovery").Insight
	if sleep.Narrative == nil || sleep.Narrative.Text != validSleep.Sentences[0].Text {
		t.Fatalf("sleep overlay = %#v", sleep.Narrative)
	}
	if recovery.Narrative != nil || recovery.Observation != dailyInsightDomain(t, snapshot, "recovery").Insight.Observation {
		t.Fatalf("recovery fallback changed: %#v", recovery)
	}
}

func TestDailyInsightNarrativeSkipsGenericCurrentContext(t *testing.T) {
	duration := 7.2
	snapshot := BuildDailyInsightSnapshot(&BriefingResponse{
		Date: "2026-09-12", Sleep: &SleepAnalysis{LatestDate: "2026-09-12", LatestTotal: &duration, TotalAvg: 6.8},
		ReadinessToday: 70, ReadinessTodayLabel: "Moderate", ReadinessServing: &ReadinessServingState{Status: ReadinessServingFresh, Confidence: ReadinessConfidenceFinal},
		EnergyBank: &EnergyBank{Current: 56, Capacity: 80, ActionVerdict: "moderate", VerdictReason: "Current reserve is available."},
	}, "en")
	if HasEligibleDailyInsightNarrativeClaims(snapshot, "en") {
		t.Fatalf("generic snapshot unexpectedly eligible: %#v", BuildDailyInsightNarrativeInput(snapshot, "en"))
	}
	for _, domain := range BuildDailyInsightNarrativeInput(snapshot, "en").Domains {
		if len(domain.Claims) != 0 {
			t.Fatalf("generic %s domain has model claim: %#v", domain.Key, domain.Claims)
		}
	}
}

func TestDailyInsightNarrativeInputKeepsCanonicalDomainSlotsForPartialSnapshot(t *testing.T) {
	snapshot := &DailyInsightSnapshot{Domains: []DailyInsightDomain{{Key: "recovery"}}}
	input := BuildDailyInsightNarrativeInput(snapshot, "en")
	if len(input.Domains) != 3 {
		t.Fatalf("domain count = %d, want 3", len(input.Domains))
	}
	for index, want := range []string{"sleep", "recovery", "energy"} {
		if got := input.Domains[index].Key; got != want {
			t.Fatalf("domain %d key = %q, want %q", index, got, want)
		}
	}
	if len(input.Domains[0].Claims) != 0 || len(input.Domains[1].Claims) != 0 || len(input.Domains[2].Claims) != 0 {
		t.Fatalf("partial snapshot unexpectedly created claims: %#v", input.Domains)
	}
	validated, invalid, err := ValidateDailyInsightNarrative(snapshot, "en", DailyInsightNarrative{
		Version: DailyInsightNarrativeVersion,
		Locale:  "en",
		Domains: []DailyInsightNarrativeDomain{{Key: "sleep"}, {Key: "recovery"}, {Key: "energy"}},
	})
	if err != nil || len(invalid) != 0 || len(validated.Domains) != 3 {
		t.Fatalf("partial snapshot validation = %#v, invalid=%#v, err=%v", validated, invalid, err)
	}
}

func TestOverallNarrativePropositionCarriesDecisionModeAcrossLocales(t *testing.T) {
	for _, locale := range []string{"en", "ru", "sr"} {
		moderate := localizedOverallNarrativeProposition(locale, "moderate")
		for _, mode := range []string{"rest", "active_recovery", "push_hard"} {
			if got := localizedOverallNarrativeProposition(locale, mode); got == moderate {
				t.Fatalf("%s proposition does not distinguish %q from moderate: %q", locale, mode, got)
			}
		}
	}
}

func TestDailyInsightNarrativeBundleDoesNotRequireAnOverallSection(t *testing.T) {
	snapshot := &DailyInsightSnapshot{
		DecisionID: "daily-decision", Version: DailyInsightSnapshotVersion,
		Primary:  DailyInsight{State: "insight", AnswerKind: DailyInsightAnswerFactual, EvidenceIDs: []string{"overall-evidence"}, NextStep: &DailyInsightAction{ID: "moderate"}, NarrativeSubject: "moderate"},
		Evidence: []DailyInsightEvidence{{ID: "overall-evidence", Domain: "recovery", DataState: "fresh", Confidence: "final"}},
		Domains: []DailyInsightDomain{
			{Key: "sleep"},
			{Key: "recovery", DataState: "fresh", Confidence: "final", Insight: DailyInsight{State: "insight", AnswerKind: DailyInsightAnswerFactual, ClaimID: "recovery_current_context", EvidenceIDs: []string{"overall-evidence"}}},
			{Key: "energy"},
		},
	}
	_, invalid, err := ValidateDailyInsightNarrative(snapshot, "en", DailyInsightNarrative{
		Version: DailyInsightNarrativeVersion, Locale: "en",
		Domains: []DailyInsightNarrativeDomain{{Key: "sleep"}, {Key: "recovery"}, {Key: "energy"}},
	})
	if err != nil || invalid[DailyInsightNarrativeOverallSlot] != "" {
		t.Fatalf("bundle validation unexpectedly required overall: invalid=%#v err=%v", invalid, err)
	}
}

func TestDailyInsightNarrativeSlotsKeepSiblingMaterialIndependent(t *testing.T) {
	duration := 7.2
	base := BuildDailyInsightSnapshot(&BriefingResponse{
		Date: "2026-09-12", Sleep: &SleepAnalysis{LatestDate: "2026-09-12", LatestTotal: &duration, TotalAvg: 6.8},
		ReadinessToday: 42, ReadinessTodayBand: "low", ReadinessServing: &ReadinessServingState{Status: ReadinessServingFresh, Confidence: ReadinessConfidenceFinal},
		EnergyBank: &EnergyBank{Current: 45, Capacity: 80, ActionVerdict: "active_recovery", VerdictReason: "Current reserve is available."},
	}, "en")
	base.DecisionID = "decision-for-test"
	base.Primary = DailyInsight{
		State: "insight", AnswerKind: DailyInsightAnswerFactual, EvidenceIDs: []string{"decision-evidence"},
		NextStep: &DailyInsightAction{ID: "daily-decision-active_recovery", Text: "Active recovery"}, NarrativeSubject: "active_recovery",
	}
	base.Evidence = append(base.Evidence, DailyInsightEvidence{ID: "decision-evidence", Domain: "recovery", DataState: "fresh", Confidence: "final"})
	overall, known := BuildDailyInsightNarrativeSlotInput(base, "en", DailyInsightNarrativeOverallSlot)
	if !known || len(overall.Slot.Claims) != 1 || overall.Slot.Claims[0].ID != "overall_daily_decision_context" {
		t.Fatalf("overall slot packet = %#v, known=%v", overall, known)
	}
	if hash := DailyInsightNarrativeSlotMaterialHash(base, "en", "recovery"); hash == "" {
		t.Fatal("recovery slot has no material hash")
	} else if changed := DailyInsightNarrativeSlotMaterialHash(ApplyRecentSleepBelowReference(base, RecentSleepBelowReference{State: RecentSleepClaimTrue}, "en"), "en", "recovery"); changed != hash {
		t.Fatalf("sleep update changed recovery slot hash: before=%s after=%s", hash, changed)
	}
	section := &DailyInsightNarrativeSection{Sentences: []DailyInsightNarrativeSentence{{
		Text:     "Today is set to an active-recovery pace, keeping the selected pace scoped to today.",
		ClaimIDs: []string{"overall_daily_decision_context"}, QualifierIDs: []string{"current_context"}, MeaningIDs: []string{"overall_pacing_guardrail"},
	}}}
	validated, err := ValidateDailyInsightNarrativeSlotResponse(base, "en", DailyInsightNarrativeOverallSlot, DailyInsightNarrativeSlot{
		Version: DailyInsightNarrativeVersion, Locale: "en", Slot: DailyInsightNarrativeDomain{Key: DailyInsightNarrativeOverallSlot, Section: section},
	})
	if err != nil || validated == nil {
		t.Fatalf("validate overall slot: section=%#v err=%v", validated, err)
	}
	rendered, err := ApplyDailyInsightNarrativeSlot(base, "en", DailyInsightNarrativeOverallSlot, validated)
	if err != nil || rendered.Primary.Narrative == nil || rendered.Primary.Narrative.Text != section.Sentences[0].Text {
		t.Fatalf("apply overall slot: snapshot=%#v err=%v", rendered, err)
	}
}

func TestDailyInsightNarrativeSlotRejectsMeaningOutsideServerCatalogue(t *testing.T) {
	snapshot := ApplyRecentSleepBelowReference(BuildDailyInsightSnapshot(&BriefingResponse{Date: "2026-09-12"}, "en"), RecentSleepBelowReference{State: RecentSleepClaimTrue}, "en")
	_, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, "en", "sleep", DailyInsightNarrativeSlot{
		Version: DailyInsightNarrativeVersion,
		Locale:  "en",
		Slot: DailyInsightNarrativeDomain{Key: "sleep", Section: &DailyInsightNarrativeSection{Sentences: []DailyInsightNarrativeSentence{{
			Text:     "It keeps the focus on a pattern rather than a single night.",
			ClaimIDs: []string{"recent_sleep_below_reference"}, QualifierIDs: []string{"personal_pattern", "current_context"}, MeaningIDs: []string{"invented_meaning"},
		}}}},
	})
	if err == nil || !strings.Contains(err.Error(), "unapproved meaning ID") {
		t.Fatalf("unexpected meaning validation error: %v", err)
	}
}

func TestDailyInsightNarrativeSlotRequiresClaimTextAnchors(t *testing.T) {
	snapshot := ApplyRecentSleepBelowReference(BuildDailyInsightSnapshot(&BriefingResponse{Date: "2026-09-12"}, "en"), RecentSleepBelowReference{State: RecentSleepClaimTrue}, "en")
	_, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, "en", "sleep", DailyInsightNarrativeSlot{
		Version: DailyInsightNarrativeVersion,
		Locale:  "en",
		Slot: DailyInsightNarrativeDomain{Key: "sleep", Section: &DailyInsightNarrativeSection{Sentences: []DailyInsightNarrativeSentence{{
			Text:     "It gives the current day a little more context.",
			ClaimIDs: []string{"recent_sleep_below_reference"}, QualifierIDs: []string{"personal_pattern", "current_context"}, MeaningIDs: []string{"sleep_pattern_not_single_night"},
		}}}},
	})
	if err == nil || !strings.Contains(err.Error(), "missing required text fragment") {
		t.Fatalf("unexpected text-anchor validation error: %v", err)
	}
}

func TestDailyInsightNarrativeSlotRequiresSerbianLatinScript(t *testing.T) {
	snapshot := ApplyRecentSleepBelowReference(BuildDailyInsightSnapshot(&BriefingResponse{Date: "2026-09-12"}, "sr"), RecentSleepBelowReference{State: RecentSleepClaimTrue}, "sr")
	_, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, "sr", "sleep", DailyInsightNarrativeSlot{
		Version: DailyInsightNarrativeVersion,
		Locale:  "sr",
		Slot: DailyInsightNarrativeDomain{Key: "sleep", Section: &DailyInsightNarrativeSection{Sentences: []DailyInsightNarrativeSentence{{
			Text:     "Нekoliko poslednjih noći bilo je kraće od ličnog obrasca, pa čini skorašnji obrazac.",
			ClaimIDs: []string{"recent_sleep_below_reference"}, QualifierIDs: []string{"personal_pattern", "current_context"}, MeaningIDs: []string{"sleep_pattern_not_single_night"},
		}}}},
	})
	if err == nil || !strings.Contains(err.Error(), "Latin script") {
		t.Fatalf("unexpected Serbian script validation error: %v", err)
	}
}

func TestDailyInsightNarrativeIncludesDistinctRecoveryAndEnergyClaims(t *testing.T) {
	duration := 7.2
	snapshot := BuildDailyInsightSnapshot(&BriefingResponse{
		Date:               "2026-09-12",
		Sleep:              &SleepAnalysis{LatestDate: "2026-09-12", LatestTotal: &duration, TotalAvg: 7.1},
		ReadinessToday:     42,
		ReadinessTodayBand: "low",
		ReadinessTip:       "Recovery signals are more limited today.",
		ReadinessServing:   &ReadinessServingState{Status: ReadinessServingFresh, Confidence: ReadinessConfidenceFinal},
		EnergyBank:         &EnergyBank{Current: 45, Capacity: 80, ActionVerdict: "active_recovery", VerdictReason: "The current reserve supports a quieter day."},
	}, "en")
	input := BuildDailyInsightNarrativeInput(snapshot, "en")
	if got := narrativeInputClaimIDs(input, "recovery"); len(got) != 1 || got[0] != "recovery_readiness_context" {
		t.Fatalf("recovery claims = %#v", got)
	}
	if got := narrativeInputClaimIDs(input, "energy"); len(got) != 1 || got[0] != "energy_current_verdict_context" {
		t.Fatalf("energy claims = %#v", got)
	}
	if claim := narrativeInputClaim(input, "recovery"); strings.Contains(claim.Proposition, "Recovery signals are more limited today.") || claim.RequiredQualifierIDs[0] != "current_context" {
		t.Fatalf("recovery packet leaked display copy or wrong qualifiers: %#v", claim)
	}
	if claim := narrativeInputClaim(input, "energy"); strings.Contains(claim.Proposition, "The current reserve supports a quieter day.") || claim.RequiredQualifierIDs[0] != "current_context" {
		t.Fatalf("energy packet leaked display copy or wrong qualifiers: %#v", claim)
	}
	if got := dailyInsightDomain(t, snapshot, "energy").Band; got == "active_recovery" {
		t.Fatalf("energy display band was overwritten by narrative subject")
	}
	if got := dailyInsightDomain(t, snapshot, "energy").NarrativeSubject; got != "active_recovery" {
		t.Fatalf("energy narrative subject = %q", got)
	}
}

func TestEnergyNarrativePropositionsAreObservationsNotActions(t *testing.T) {
	for _, locale := range []string{"en", "ru", "sr"} {
		for _, verdict := range []string{"push_hard", "rest", "active_recovery"} {
			got := localizedEnergyNarrativeProposition(locale, verdict)
			for _, forbidden := range []string{"pace", "tempo", "темп"} {
				if strings.Contains(strings.ToLower(got), forbidden) {
					t.Fatalf("%s/%s proposition is action-like: %q", locale, verdict, got)
				}
			}
		}
	}
}

func narrativeInputClaimIDs(input DailyInsightNarrativeInput, key string) []string {
	for _, domain := range input.Domains {
		if domain.Key != key {
			continue
		}
		ids := make([]string, 0, len(domain.Claims))
		for _, claim := range domain.Claims {
			ids = append(ids, claim.ID)
		}
		return ids
	}
	return nil
}

func narrativeInputClaim(input DailyInsightNarrativeInput, key string) DailyInsightNarrativeClaim {
	for _, domain := range input.Domains {
		if domain.Key == key && len(domain.Claims) == 1 {
			return domain.Claims[0]
		}
	}
	return DailyInsightNarrativeClaim{}
}

func dailyInsightDomain(t *testing.T, snapshot *DailyInsightSnapshot, key string) DailyInsightDomain {
	t.Helper()
	if snapshot == nil {
		t.Fatal("snapshot is nil")
	}
	for _, domain := range snapshot.Domains {
		if domain.Key == key {
			return domain
		}
	}
	t.Fatalf("%s domain missing", key)
	return DailyInsightDomain{}
}
