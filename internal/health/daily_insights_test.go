package health

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode"
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
		if domain.Insight.Observation != "Last night is already part of today’s picture." {
			t.Fatalf("observation = %q", domain.Insight.Observation)
		}
		return
	}
	t.Fatal("sleep domain missing")
}

func TestLocalizedInsightDataGuidanceKeepsTechnicalCaveatsOffTheCard(t *testing.T) {
	for _, locale := range []string{"en", "ru", "sr"} {
		for _, domain := range []string{"sleep", "recovery", "energy"} {
			for _, state := range []string{"missing", "stale", "partial", ReadinessServingDataAccruing} {
				observation, meaning, _ := localizedInsightDataGuidance(locale, domain, state)
				text := strings.ToLower(observation + " " + meaning)
				for _, forbidden := range []string{
					"limited", "insufficient", "incomplete", "data quality", "uncertain", "calibrat",
					"огранич", "недостат", "непол", "качество данных", "неопредел", "калибров",
					"ogranič", "nedovoljno", "nepotpun", "kvalitet podataka", "neizves", "kalibr",
				} {
					if strings.Contains(text, forbidden) {
						t.Fatalf("%s/%s/%s exposes %q in %q", locale, domain, state, forbidden, text)
					}
				}
			}
		}
	}
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
	got := ApplyRecentSleepBelowReference(base, RecentSleepBelowReference{State: RecentSleepClaimTrue, ReferenceHours: 7.8, CurrentShortNightCount: 3, EveningActionAvailable: true, ActionEvent: true}, "en")
	sleep := dailyInsightDomain(t, got, "sleep")
	if sleep.Insight.AnswerKind != DailyInsightAnswerConfirmedPersonal || sleep.Insight.ClaimID != "recent_sleep_below_reference" || sleep.Insight.NextStep == nil || sleep.Insight.NextStep.ID != "wind_down" {
		t.Fatalf("sleep B0 claim = %#v", sleep.Insight)
	}
	if want := "3 of your last 4 nights were shorter than your usual sleep."; sleep.Insight.Observation != want {
		t.Fatalf("sleep B0 observation = %q, want %q", sleep.Insight.Observation, want)
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

func TestApplyRecentSleepBelowReferenceKeepsGentleActionAfterEventCadenceSuppresses(t *testing.T) {
	base := BuildDailyInsightSnapshot(&BriefingResponse{Date: "2026-09-10"}, "en")
	got := ApplyRecentSleepBelowReference(base, RecentSleepBelowReference{
		State: RecentSleepClaimTrue, ReferenceHours: 7.8, CurrentShortNightCount: 3,
		EveningActionAvailable: true,
	}, "en")
	if sleep := dailyInsightDomain(t, got, "sleep"); sleep.Insight.NextStep == nil || sleep.Insight.NextStep.ID != "wind_down" {
		t.Fatalf("cadence-suppressed current evening lost server action: %#v", sleep.Insight)
	}

	withoutEveningEligibility := ApplyRecentSleepBelowReference(base, RecentSleepBelowReference{State: RecentSleepClaimTrue}, "en")
	if sleep := dailyInsightDomain(t, withoutEveningEligibility, "sleep"); sleep.Insight.NextStep != nil {
		t.Fatalf("non-evening claim unexpectedly received action: %#v", sleep.Insight.NextStep)
	}
}

func TestApplyRecentSleepBelowReferenceLocalizesWindDownAction(t *testing.T) {
	want := map[string]string{
		"en": "Wind down this evening",
		"ru": "Сделать вечер тише",
		"sr": "Utišati veče",
	}
	for locale, actionText := range want {
		base := BuildDailyInsightSnapshot(&BriefingResponse{Date: "2026-09-10"}, locale)
		got := ApplyRecentSleepBelowReference(base, RecentSleepBelowReference{State: RecentSleepClaimTrue, EveningActionAvailable: true, ActionEvent: true}, locale)
		sleep := dailyInsightDomain(t, got, "sleep")
		if sleep.Insight.NextStep == nil || sleep.Insight.NextStep.ID != "wind_down" || sleep.Insight.NextStep.Text != actionText {
			t.Fatalf("%s wind-down action = %#v, want %q", locale, sleep.Insight.NextStep, actionText)
		}
	}
}

func TestSleepNarrativePacketUsesDetailedServerOwnedWindDownCopy(t *testing.T) {
	snapshot := ApplyRecentSleepBelowReference(
		BuildDailyInsightSnapshot(&BriefingResponse{Date: "2026-09-12"}, "ru"),
		RecentSleepBelowReference{State: RecentSleepClaimTrue, ReferenceHours: 7.8, CurrentShortNightCount: 3, EveningActionAvailable: true},
		"ru",
	)
	input, known := BuildDailyInsightNarrativeSlotInput(snapshot, "ru", "sleep")
	if !known || input.Slot.Action == nil {
		t.Fatalf("sleep action packet = %#v, known=%v", input, known)
	}
	if want := "Сегодня вечером оставь себе спокойный час без задач и начни сворачиваться раньше."; input.Slot.Action.Text != want {
		t.Fatalf("narrative action = %q, want %q", input.Slot.Action.Text, want)
	}
	if got := dailyInsightDomain(t, snapshot, "sleep").Insight.NextStep.Text; got != "Сделать вечер тише" {
		t.Fatalf("compact UI action changed to %q", got)
	}
}

func TestSleepNarrativePacketRestoresKnownActionCopyFromPrivacyMinimizedMarker(t *testing.T) {
	action := buildDailyInsightNarrativeAction(&DailyInsightAction{ID: "wind_down"}, nil, "ru")
	if action == nil {
		t.Fatal("privacy-minimized wind_down marker lost narrative action")
	}
	if want := "Сегодня вечером оставь себе спокойный час без задач и начни сворачиваться раньше."; action.Text != want {
		t.Fatalf("narrative action = %q, want %q", action.Text, want)
	}
}

func TestSleepNarrativeEvidenceNamesUsualSleepAndRepeatedShortNights(t *testing.T) {
	reference, shortNights := 7.2, 3.0
	if got, want := localizedNarrativeEvidenceStatement("ru", DailyInsightEvidence{ID: "sleep_recent_reference", Value: &reference, Unit: "h"}), "Твой обычный сон: 7 ч 12 мин."; got != want {
		t.Fatalf("reference fact = %q, want %q", got, want)
	}
	if got, want := localizedNarrativeEvidenceStatement("ru", DailyInsightEvidence{ID: "sleep_recent_short_nights", Value: &shortNights, Unit: "nights"}), "Коротких ночей за последние четыре: 3."; got != want {
		t.Fatalf("short-nights fact = %q, want %q", got, want)
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
		Text: "The comparison stays with your own usual sleep, rather than a universal target.", ClaimIDs: []string{"recent_sleep_below_reference"}, QualifierIDs: []string{"personal_pattern", "current_context"}, MeaningIDs: []string{"sleep_personal_reference"},
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

func TestDailyInsightNarrativeUsesFreshDerivedContextAcrossDomains(t *testing.T) {
	duration := 7.2
	snapshot := BuildDailyInsightSnapshot(&BriefingResponse{
		Date: "2026-09-12", Sleep: &SleepAnalysis{LatestDate: "2026-09-12", LatestTotal: &duration, TotalAvg: 6.8},
		ReadinessToday: 70, ReadinessTodayLabel: "Moderate", ReadinessServing: &ReadinessServingState{Status: ReadinessServingFresh, Confidence: ReadinessConfidenceFinal},
		EnergyBank: &EnergyBank{Current: 56, Capacity: 80, ActionVerdict: "moderate", VerdictReason: "Current reserve is available."},
	}, "en")
	if !HasEligibleDailyInsightNarrativeClaims(snapshot, "en") {
		t.Fatalf("fresh multi-domain snapshot unexpectedly ineligible")
	}
	input, known := BuildDailyInsightNarrativeSlotInput(snapshot, "en", DailyInsightNarrativeOverallSlot)
	if !known || len(input.Slot.Facts) < 2 || input.Slot.Baseline == nil {
		t.Fatalf("overall synthesis input = %#v", input)
	}
}

func TestDailyInsightNarrativeSkipsUnsupportedClaimID(t *testing.T) {
	snapshot := &DailyInsightSnapshot{
		Domains: []DailyInsightDomain{{
			Key: "recovery", DataState: "fresh", Confidence: "final",
			Insight: DailyInsight{State: "insight", AnswerKind: DailyInsightAnswerFactual, ClaimID: "recovery_current_context", EvidenceIDs: []string{"recovery-evidence"}},
		}},
		Evidence: []DailyInsightEvidence{{ID: "recovery-evidence", Domain: "recovery", DataState: "fresh", Confidence: "final"}},
	}
	if HasEligibleDailyInsightNarrativeClaims(snapshot, "en") {
		t.Fatalf("unsupported generic claim unexpectedly eligible: %#v", BuildDailyInsightNarrativeInput(snapshot, "en"))
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

func TestNarrativeMeaningLinksRequireDistinctCombinedContext(t *testing.T) {
	for _, locale := range []string{"en", "ru", "sr"} {
		if got := overallNarrativeMeaningLinks(locale, []string{"recovery"}); len(got) != 0 {
			t.Fatalf("%s single-domain overall meaning = %#v", locale, got)
		}
		combined := overallNarrativeMeaningLinks(locale, []string{"recovery", "energy"})
		if len(combined) != 1 || combined[0].ID != "overall_combined_context" || combined[0].Statement == "" {
			t.Fatalf("%s combined overall meaning = %#v", locale, combined)
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
		EnergyBank: &EnergyBank{Current: 45, Capacity: 80, ActionVerdict: "rest", VerdictReason: "Current reserve is available."},
	}, "en")
	base.DecisionID = "decision-for-test"
	base.Primary = DailyInsight{
		State: "insight", AnswerKind: DailyInsightAnswerFactual, EvidenceIDs: []string{"decision-evidence"},
		NextStep: &DailyInsightAction{ID: "daily-decision-rest", Text: "Rest"}, NarrativeSubject: "rest",
	}
	base.DecisionEvidenceDomains = []string{"recovery", "energy"}
	base.Evidence = append(base.Evidence, DailyInsightEvidence{ID: "decision-evidence", Domain: "recovery", DataState: "fresh", Confidence: "final"})
	base.Domains[1].Insight.AnswerKind = DailyInsightAnswerFactual
	base.Domains[1].Insight.ClaimID = "recovery_readiness_context"
	overall, known := BuildDailyInsightNarrativeSlotInput(base, "en", DailyInsightNarrativeOverallSlot)
	if !known || len(overall.Slot.Claims) != 1 || overall.Slot.Claims[0].ID != "overall_daily_decision_context" {
		t.Fatalf("overall slot packet = %#v, known=%v", overall, known)
	}
	if hash := DailyInsightNarrativeSlotMaterialHash(base, "en", "energy"); hash == "" {
		t.Fatal("energy slot has no material hash")
	} else if changed := DailyInsightNarrativeSlotMaterialHash(ApplyRecentSleepBelowReference(base, RecentSleepBelowReference{State: RecentSleepClaimTrue}, "en"), "en", "energy"); changed != hash {
		t.Fatalf("sleep update changed energy slot hash: before=%s after=%s", hash, changed)
	}
	energy, known := BuildDailyInsightNarrativeSlotInput(base, "en", "energy")
	if !known || len(energy.Slot.Claims) != 0 || energy.Slot.Position != nil {
		t.Fatalf("energy should retain its deterministic card without standalone model prose: %#v, known=%v", energy, known)
	}
	sleep, known := BuildDailyInsightNarrativeSlotInput(base, "en", "sleep")
	if !known || sleep.Slot.Position != nil {
		t.Fatalf("unrelated sleep slot received server position: %#v", sleep.Slot.Position)
	}
	section := &DailyInsightNarrativeSection{Text: "Sleep and recovery point in the same careful direction today.", FactIDs: []string{"sleep_canonical_comparison", "readiness_current"}}
	validated, err := ValidateDailyInsightNarrativeSlotResponse(base, "en", DailyInsightNarrativeOverallSlot, DailyInsightNarrativeSlot{
		Version: DailyInsightNarrativeVersion, Locale: "en", Slot: DailyInsightNarrativeDomain{Key: DailyInsightNarrativeOverallSlot, Section: section},
	})
	if err != nil || validated == nil {
		t.Fatalf("validate overall slot: section=%#v err=%v", validated, err)
	}
	rendered, err := ApplyDailyInsightNarrativeSlot(base, "en", DailyInsightNarrativeOverallSlot, validated)
	if err != nil || rendered.Primary.Narrative == nil || rendered.Primary.Narrative.Text != section.Text {
		t.Fatalf("apply overall slot: snapshot=%#v err=%v", rendered, err)
	}
}

func TestHumanSynthesisValidatorAcceptsConversationalRussianAndSerbian(t *testing.T) {
	snapshot := narrativeTestSnapshot(t)
	for _, test := range []struct{ locale, text string }{
		{"ru", "Сон и готовность сегодня складываются в более спокойный фон."},
		{"sr", "San i spremnost danas zajedno ukazuju na mirniji okvir."},
	} {
		t.Run(test.locale, func(t *testing.T) {
			_, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, test.locale, DailyInsightNarrativeOverallSlot, DailyInsightNarrativeSlot{Version: DailyInsightNarrativeVersion, Locale: test.locale, Slot: DailyInsightNarrativeDomain{Key: DailyInsightNarrativeOverallSlot, Section: &DailyInsightNarrativeSection{Text: test.text, FactIDs: []string{"sleep_canonical_comparison", "readiness_current"}}}})
			if err != nil {
				t.Fatalf("validate %s: %v", test.locale, err)
			}
		})
	}
}

func TestHumanSynthesisValidatorAcceptsLocalizedDecimalSeparator(t *testing.T) {
	snapshot := narrativeTestSnapshot(t)
	_, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, "ru", DailyInsightNarrativeOverallSlot, DailyInsightNarrativeSlot{
		Version: DailyInsightNarrativeVersion,
		Locale:  "ru",
		Slot: DailyInsightNarrativeDomain{Key: DailyInsightNarrativeOverallSlot, Section: &DailyInsightNarrativeSection{
			Text:    "После 7,2 часа сна готовность сегодня выглядит устойчивой.",
			FactIDs: []string{"sleep_canonical_comparison", "readiness_current"},
		}},
	})
	if err != nil {
		t.Fatalf("localized decimal separator rejected: %v", err)
	}
}

func TestHumanSynthesisValidatorCountsDecimalPunctuationAsNumbers(t *testing.T) {
	snapshot := numericNarrativeTestSnapshot()
	for _, test := range []struct{ locale, text string }{
		{"en", "Sleep is 47.2 while recovery is 34.5 today."},
		{"ru", "Сон: 47,2, а восстановление: 34,5 сегодня."},
		{"sr", "San je 47,2, a oporavak 34,5 danas."},
	} {
		t.Run(test.locale, func(t *testing.T) {
			candidate := DailyInsightNarrativeSlot{Version: DailyInsightNarrativeVersion, Locale: test.locale, Slot: DailyInsightNarrativeDomain{Key: DailyInsightNarrativeOverallSlot, Section: &DailyInsightNarrativeSection{Text: test.text, FactIDs: []string{"sleep_decimal", "recovery_decimal"}}}}
			if _, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, test.locale, DailyInsightNarrativeOverallSlot, candidate); err != nil {
				t.Fatalf("decimal prose rejected: %v", err)
			}
		})
	}
	tooMany := DailyInsightNarrativeSlot{Version: DailyInsightNarrativeVersion, Locale: "en", Slot: DailyInsightNarrativeDomain{Key: DailyInsightNarrativeOverallSlot, Section: &DailyInsightNarrativeSection{Text: "Sleep is 47.2. Recovery is 34.5. Activity is present. Energy is present.", FactIDs: []string{"sleep_decimal", "recovery_decimal"}}}}
	if _, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, "en", DailyInsightNarrativeOverallSlot, tooMany); err == nil {
		t.Fatal("four actual sentences were accepted")
	}
}

func TestHumanSynthesisValidatorAcceptsOnlyFormattingEquivalentGroupedNumbers(t *testing.T) {
	snapshot := numericNarrativeTestSnapshot()
	for _, text := range []string{
		"Sleep and activity align around 5,789 today.",
		"Sleep and activity align around 5.789 today.",
		"Sleep and activity align around 5\u00a0789 today.",
		"Sleep and activity align around 5\u202f789 today.",
	} {
		candidate := DailyInsightNarrativeSlot{Version: DailyInsightNarrativeVersion, Locale: "en", Slot: DailyInsightNarrativeDomain{Key: DailyInsightNarrativeOverallSlot, Section: &DailyInsightNarrativeSection{Text: text, FactIDs: []string{"sleep_decimal", "activity_integer"}}}}
		if _, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, "en", DailyInsightNarrativeOverallSlot, candidate); err != nil {
			t.Fatalf("grouped number %q rejected: %v", text, err)
		}
	}
	ru := DailyInsightNarrativeSlot{Version: DailyInsightNarrativeVersion, Locale: "ru", Slot: DailyInsightNarrativeDomain{Key: DailyInsightNarrativeOverallSlot, Section: &DailyInsightNarrativeSection{Text: "Сон и энергия сходятся около 11 505 сегодня.", FactIDs: []string{"sleep_decimal", "energy_integer"}}}}
	if _, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, "ru", DailyInsightNarrativeOverallSlot, ru); err != nil {
		t.Fatalf("Russian grouped number rejected: %v", err)
	}
	changed := DailyInsightNarrativeSlot{Version: DailyInsightNarrativeVersion, Locale: "en", Slot: DailyInsightNarrativeDomain{Key: DailyInsightNarrativeOverallSlot, Section: &DailyInsightNarrativeSection{Text: "Sleep and activity align around 5,790 today.", FactIDs: []string{"sleep_decimal", "activity_integer"}}}}
	if _, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, "en", DailyInsightNarrativeOverallSlot, changed); err == nil {
		t.Fatal("changed grouped value was accepted")
	}
}

func numericNarrativeTestSnapshot() *DailyInsightSnapshot {
	return &DailyInsightSnapshot{NarrativeFacts: []DailyInsightNarrativeFact{
		{ID: "sleep_decimal", Domain: "sleep", Fresh: true, Statement: "Sleep.", DisplayValues: []string{"47.2"}},
		{ID: "recovery_decimal", Domain: "recovery", Fresh: true, Statement: "Recovery.", DisplayValues: []string{"34.5"}},
		{ID: "activity_integer", Domain: "activity", Fresh: true, Statement: "Activity.", DisplayValues: []string{"5789"}},
		{ID: "energy_integer", Domain: "energy", Fresh: true, Statement: "Energy.", DisplayValues: []string{"11505"}},
	}}
}

func TestHumanSynthesisValidatorRejectsUnsafeOrUnsupportedOutput(t *testing.T) {
	snapshot := narrativeTestSnapshot(t)
	valid := func(text string) DailyInsightNarrativeSlot {
		return DailyInsightNarrativeSlot{Version: DailyInsightNarrativeVersion, Locale: "en", Slot: DailyInsightNarrativeDomain{Key: DailyInsightNarrativeOverallSlot, Section: &DailyInsightNarrativeSection{Text: text, FactIDs: []string{"sleep_canonical_comparison", "readiness_current"}}}}
	}
	for _, candidate := range []DailyInsightNarrativeSlot{
		valid("Sleep and readiness point to 99 today."),
		valid("Sleep causes your readiness to fall."),
		valid("This is a medical diagnosis."),
		{Version: DailyInsightNarrativeVersion, Locale: "en", Slot: DailyInsightNarrativeDomain{Key: DailyInsightNarrativeOverallSlot, Section: &DailyInsightNarrativeSection{Text: "Sleep and readiness point in one direction.", FactIDs: []string{"unknown", "readiness_current"}}}},
		{Version: DailyInsightNarrativeVersion, Locale: "en", Slot: DailyInsightNarrativeDomain{Key: DailyInsightNarrativeOverallSlot, Section: &DailyInsightNarrativeSection{Text: "Sleep and readiness point in one direction.", FactIDs: []string{"sleep_canonical_comparison", "readiness_current"}, ActionID: "invented"}}},
	} {
		if _, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, "en", DailyInsightNarrativeOverallSlot, candidate); err == nil {
			t.Fatalf("candidate unexpectedly passed: %#v", candidate)
		}
	}
}

func TestHumanSynthesisRiskBasedContractAllowsLowRiskSuggestionsAndRejectsDanger(t *testing.T) {
	snapshot := narrativeTestSnapshot(t)
	snapshot.Primary.NextStep = nil
	valid := func(locale, text string, actionID ...string) DailyInsightNarrativeSlot {
		selected := ""
		if len(actionID) > 0 {
			selected = actionID[0]
		}
		return DailyInsightNarrativeSlot{Version: DailyInsightNarrativeVersion, Locale: locale, Slot: DailyInsightNarrativeDomain{Key: DailyInsightNarrativeOverallSlot, Section: &DailyInsightNarrativeSection{Text: text, FactIDs: []string{"sleep_canonical_comparison", "readiness_current"}, ActionID: selected}}}
	}
	for _, candidate := range []DailyInsightNarrativeSlot{
		valid("en", "Sleep and readiness point to a quieter frame, so you could take a short walk if it fits.", ""),
		valid("ru", "Сон и готовность складываются в более спокойный фон; можно сделать короткую прогулку, если это удобно.", ""),
		valid("sr", "San i spremnost daju mirniji okvir; možeš prošetati kratko ako ti odgovara.", ""),
		valid("en", "Sleep and readiness form one picture because of the contrast in their recent values.", ""),
		valid("ru", "Сон и готовность складываются в один контекст из-за разницы в недавних значениях.", ""),
		valid("sr", "San i spremnost daju jedan kontekst zbog razlike u nedavnim vrednostima.", ""),
		valid("en", "A short walk will still be an option later if it fits.", ""),
		valid("ru", "Сон и готовность складываются в спокойный контекст; позже ты будешь выбирать короткую прогулку, если это удобно.", ""),
		valid("sr", "San i spremnost daju mirniji okvir; kasnije ćeš imati mogućnost za kratku šetnju ako ti odgovara.", ""),
	} {
		if _, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, candidate.Locale, DailyInsightNarrativeOverallSlot, candidate); err != nil {
			t.Fatalf("low-risk suggestion was rejected: %#v err=%v", candidate, err)
		}
	}
	restSnapshot := narrativeTestSnapshot(t)
	restSnapshot.Primary.NextStep = &DailyInsightAction{ID: "daily-decision-rest", Text: "Rest"}
	for _, candidate := range []DailyInsightNarrativeSlot{
		valid("en", "This is a medical diagnosis."),
		valid("en", "There is no need to see a doctor."),
		valid("en", "Take a supplement dose tonight."),
		valid("en", "Sleep directly causes readiness to fall."),
		valid("en", "Sleep and readiness mean you will recover tomorrow."),
		valid("en", "A short walk could help support your recovery."),
		valid("en", "Train all-out today."),
		valid("en", "Push through your day."),
		valid("ru", "Это медицинский диагноз."),
		valid("ru", "Не нужно обращаться к врачу."),
		valid("ru", "Прими добавку сегодня вечером."),
		valid("ru", "Сон напрямую вызывает снижение готовности."),
		valid("ru", "Сон и готовность означают, что завтра ты восстановишься."),
		valid("ru", "Короткая прогулка поможет поддержать восстановление."),
		valid("ru", "Тренируйся на максимум сегодня."),
		valid("ru", "Работай на пределе сегодня."),
		valid("sr", "Ovo je medicinska dijagnoza."),
		valid("sr", "Ne moraš kod lekara."),
		valid("sr", "Uzmi suplement večeras."),
		valid("sr", "San direktno uzrokuje nižu spremnost."),
		valid("sr", "San i spremnost znače da ćeš se oporaviti sutra."),
		valid("sr", "Kratka šetnja može pomoći oporavku."),
		valid("sr", "Treniraj maksimalno danas."),
		valid("sr", "Idi do kraja danas."),
	} {
		if _, err := ValidateDailyInsightNarrativeSlotResponse(restSnapshot, candidate.Locale, DailyInsightNarrativeOverallSlot, candidate); err == nil {
			t.Fatalf("unsafe candidate unexpectedly passed: %#v", candidate)
		}
	}
	if _, err := ValidateDailyInsightNarrativeSlotResponse(restSnapshot, "en", DailyInsightNarrativeOverallSlot, valid("en", "Sleep and readiness point together. Consider a short walk.", "daily-decision-rest")); err != nil {
		t.Fatalf("optional authoritative action_id was rejected: %v", err)
	}
}

func TestHumanSynthesisMaterialHashCoversRichPacketAndVisibleBaseline(t *testing.T) {
	base := narrativeTestSnapshot(t)
	first := DailyInsightNarrativeSlotMaterialHash(base, "en", DailyInsightNarrativeOverallSlot)
	changed := cloneDailyInsightSnapshot(base)
	changed.NarrativeFacts[0].Statement = "changed derived aggregate"
	if got := DailyInsightNarrativeSlotMaterialHash(changed, "en", DailyInsightNarrativeOverallSlot); got == first {
		t.Fatal("derived fact did not change material hash")
	}
	changed = cloneDailyInsightSnapshot(base)
	changed.Primary.Observation = "changed B0 primary"
	if got := DailyInsightNarrativeSlotMaterialHash(changed, "en", DailyInsightNarrativeOverallSlot); got == first {
		t.Fatal("visible B0 baseline did not change material hash")
	}
	changed = cloneDailyInsightSnapshot(base)
	changed.Primary.NextStep = &DailyInsightAction{ID: "daily-decision-rest", Text: "Rest"}
	if got := DailyInsightNarrativeSlotMaterialHash(changed, "en", DailyInsightNarrativeOverallSlot); got == first {
		t.Fatal("action options did not change material hash")
	}
}

func TestHumanSynthesisLegacyWrapperUsesFreshFactsWithoutLegacyDecision(t *testing.T) {
	snapshot := &DailyInsightSnapshot{NarrativeFacts: []DailyInsightNarrativeFact{
		{ID: "sleep", Domain: "sleep", Fresh: true, Statement: "Sleep context.", EvidenceIDs: []string{"sleep"}},
		{ID: "activity", Domain: "activity", Fresh: true, Statement: "Activity context.", EvidenceIDs: []string{"activity"}},
	}}
	if legacyOverallNarrativeEligible(snapshot) {
		t.Fatal("legacy decision unexpectedly eligible")
	}
	candidate := DailyInsightNarrative{Version: DailyInsightNarrativeVersion, Locale: "en", Overall: &DailyInsightNarrativeSection{Text: "Sleep and activity point in one direction.", FactIDs: []string{"sleep", "activity"}}}
	validated, err := ValidateDailyInsightNarrativeSlot(snapshot, "en", DailyInsightNarrativeOverallSlot, candidate)
	if err != nil || validated.Overall == nil {
		t.Fatalf("fresh-fact overall wrapper rejected: %#v err=%v", validated, err)
	}
}

func TestServingEligibilityRejectsLegacyOnlyOverallPacket(t *testing.T) {
	snapshot := &DailyInsightSnapshot{
		DecisionID: "historical-decision",
		Primary:    DailyInsight{State: "insight", EvidenceIDs: []string{"recovery-evidence"}},
		Evidence:   []DailyInsightEvidence{{ID: "recovery-evidence", Domain: "recovery", DataState: "fresh"}},
		Domains:    []DailyInsightDomain{{Key: "recovery", DataState: "fresh"}},
	}
	if !legacyOverallNarrativeEligible(snapshot) {
		t.Fatal("fixture must exercise legacy historical eligibility")
	}
	if HasEligibleDailyInsightNarrativeSlot(snapshot, "en", DailyInsightNarrativeOverallSlot) || HasEligibleDailyInsightNarrativeClaims(snapshot, "en") {
		t.Fatal("legacy-only packet unexpectedly eligible for B1 serving")
	}
}

func TestDailyInsightNarrativeCombinedWrapperUsesFreshFactsWithoutLegacyClaims(t *testing.T) {
	snapshot := &DailyInsightSnapshot{NarrativeFacts: []DailyInsightNarrativeFact{
		{ID: "sleep", Domain: "sleep", Fresh: true, Statement: "Sleep context.", EvidenceIDs: []string{"sleep-evidence"}},
		{ID: "activity", Domain: "activity", Fresh: true, Statement: "Activity context.", EvidenceIDs: []string{"activity-evidence"}},
	}, Domains: []DailyInsightDomain{{Key: "sleep"}, {Key: "recovery"}, {Key: "energy"}}}
	if legacyOverallNarrativeEligible(snapshot) {
		t.Fatal("legacy decision unexpectedly eligible")
	}
	candidate := DailyInsightNarrative{
		Version: DailyInsightNarrativeVersion,
		Locale:  "en",
		Overall: &DailyInsightNarrativeSection{Text: "Sleep and activity point in one direction.", FactIDs: []string{"sleep", "activity"}},
		Domains: []DailyInsightNarrativeDomain{{Key: "sleep"}, {Key: "recovery"}, {Key: "energy"}},
	}
	validated, invalid, err := ValidateDailyInsightNarrative(snapshot, "en", candidate)
	if err != nil || len(invalid) != 0 || validated.Overall == nil {
		t.Fatalf("fresh-fact combined wrapper rejected: %#v invalid=%#v err=%v", validated, invalid, err)
	}
	rendered, err := ApplyDailyInsightNarrative(snapshot, candidate)
	if err != nil || rendered.Primary.Narrative == nil || rendered.Primary.Narrative.Text != candidate.Overall.Text || !containsDailyInsightID(rendered.Primary.Narrative.EvidenceIDs, "sleep-evidence") || !containsDailyInsightID(rendered.Primary.Narrative.EvidenceIDs, "activity-evidence") {
		t.Fatalf("fresh-fact combined apply = %#v err=%v", rendered, err)
	}
	candidate.Overall = &DailyInsightNarrativeSection{Text: "Sleep and activity point in one direction.", FactIDs: []string{"sleep", "unsupported"}}
	_, invalid, err = ValidateDailyInsightNarrative(snapshot, "en", candidate)
	if err != nil || invalid[DailyInsightNarrativeOverallSlot] == "" {
		t.Fatalf("unsupported fact passed combined validation: invalid=%#v err=%v", invalid, err)
	}
}

func TestNarrativeFactsUseOnlyBoundedDailyAggregates(t *testing.T) {
	daily := make([]DailyHealthMetrics, 11)
	for index := range daily {
		sleep := 6.0 + float64(index)/10
		steps := 6000.0 + float64(index*100)
		daily[index] = DailyHealthMetrics{Date: fmt.Sprintf("2026-09-%02d", 20-index), Sleep: &sleep, Steps: &steps}
	}
	resp := &BriefingResponse{Date: "2026-09-20", RawMetrics: &RawMetrics{LastDate: "2026-09-20", Daily: daily}}
	facts := buildDailyInsightNarrativeFacts(resp, nil, "en")
	byID := map[string]DailyInsightNarrativeFact{}
	for _, fact := range facts {
		byID[fact.ID] = fact
	}
	for _, id := range []string{"sleep_recent_four_day_pattern", "activity_recent_steps_trend"} {
		fact, ok := byID[id]
		if !ok || !strings.Contains(fact.Window, "calendar") || fact.Domain == "" || fact.Meaning == "" {
			t.Fatalf("bounded fact %q = %#v", id, fact)
		}
		if strings.IndexFunc(fact.Window, unicode.IsDigit) >= 0 {
			t.Fatalf("model-visible fact window must spell out its bounded count: %#v", fact)
		}
	}
	packet := DailyInsightNarrativeSlotInput{Version: DailyInsightNarrativeInputVersion, Locale: "en", Slot: DailyInsightNarrativeDomainInput{Key: DailyInsightNarrativeOverallSlot, Facts: facts}}
	encoded, err := json.Marshal(packet)
	if err != nil {
		t.Fatalf("marshal packet: %v", err)
	}
	for _, forbidden := range []string{"2026-09-20", "source", "device", "raw_metrics"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("packet leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestNarrativeFactsUseAlignedDomainsAndDoNotInventHeadlineBaselines(t *testing.T) {
	sleep, rhr := 5.5, 62.0
	resp := &BriefingResponse{
		Date:             "2026-09-20",
		Sleep:            &SleepAnalysis{LatestDate: "2026-09-19", LatestTotal: &sleep, TotalAvg: 7.1},
		ReadinessToday:   70,
		ReadinessTip:     "Current readiness evidence is available.",
		ReadinessServing: &ReadinessServingState{Status: ReadinessServingFresh, Confidence: ReadinessConfidenceFinal},
		RawMetrics:       &RawMetrics{LastDate: "2026-09-20", Daily: []DailyHealthMetrics{{Date: "2026-09-20", RHR: &rhr}}},
		Headline: &HeadlineSignal{Metrics: []HeadlineMetricDelta{
			{Metric: "unknown_metric", Value: 7, Baseline: 5},
			{Metric: "sleep_total", Value: 5.5, Unit: "h"},
			{Metric: "resting_heart_rate", Value: 62, Baseline: 58, DeltaAbs: 4, Unit: "bpm"},
		}},
	}
	snapshot := BuildDailyInsightSnapshot(resp, "en")
	facts := snapshot.NarrativeFacts
	byID := map[string]DailyInsightNarrativeFact{}
	for _, fact := range facts {
		byID[fact.ID] = fact
	}
	if _, found := byID["sleep_canonical_comparison"]; found {
		t.Fatalf("stale sleep fact entered B1: %#v", facts)
	}
	if _, found := byID["headline_sleep_total"]; found {
		t.Fatalf("stale sleep headline entered B1: %#v", facts)
	}
	recoveryHeadline, found := byID["headline_resting_heart_rate"]
	if !found || recoveryHeadline.Domain != "recovery" || recoveryHeadline.Window != "current day versus personal baseline" || len(recoveryHeadline.DisplayValues) != 3 {
		t.Fatalf("recovery headline fact = %#v", recoveryHeadline)
	}
	if _, found := byID["headline_unknown_metric"]; found {
		t.Fatalf("unknown headline metric entered B1: %#v", facts)
	}
	if HasEligibleDailyInsightNarrativeSlot(snapshot, "en", DailyInsightNarrativeOverallSlot) {
		t.Fatal("stale sleep headline faked a combined eligible packet")
	}

	// The exact same typed headline becomes usable only when its owning sleep
	// domain is current, final, and factual. Together with the current RHR
	// row, this supplies two genuinely fresh domains.
	resp.Sleep.LatestDate = resp.Date
	snapshot = BuildDailyInsightSnapshot(resp, "en")
	facts = snapshot.NarrativeFacts
	byID = map[string]DailyInsightNarrativeFact{}
	for _, fact := range facts {
		byID[fact.ID] = fact
	}
	sleepHeadline, found := byID["headline_sleep_total"]
	if !found || sleepHeadline.Domain != "sleep" || sleepHeadline.Window != "current day" || len(sleepHeadline.DisplayValues) != 1 || strings.Contains(sleepHeadline.Statement, "0.0") {
		t.Fatalf("aligned sleep headline fact = %#v", sleepHeadline)
	}
	if !HasEligibleDailyInsightNarrativeSlot(snapshot, "en", DailyInsightNarrativeOverallSlot) {
		t.Fatalf("aligned sleep and recovery headline facts were not eligible: %#v", facts)
	}

	resp.ReadinessTip = ""
	snapshot = BuildDailyInsightSnapshot(resp, "en")
	facts = snapshot.NarrativeFacts
	for _, fact := range facts {
		if fact.ID == "headline_resting_heart_rate" {
			t.Fatalf("non-factual recovery headline entered B1: %#v", facts)
		}
	}
	resp.Date = "not-a-date"
	resp.Sleep.LatestDate = resp.Date
	for _, fact := range BuildDailyInsightSnapshot(resp, "en").NarrativeFacts {
		if strings.HasPrefix(fact.ID, "headline_") {
			t.Fatalf("unaligned headline entered B1: %#v", fact)
		}
	}
}

func TestNarrativeFactsWithholdIncompleteDailyWindowsAndMissingReadiness(t *testing.T) {
	daily := make([]DailyHealthMetrics, 10)
	for index := range daily {
		sleep, steps := 7.0, 7000.0
		daily[index] = DailyHealthMetrics{Date: fmt.Sprintf("2026-09-%02d", 20-index), Sleep: &sleep, Steps: &steps}
	}
	daily[2].Sleep = nil
	daily[8].Steps = nil
	facts := buildDailyInsightNarrativeFacts(&BriefingResponse{RawMetrics: &RawMetrics{LastDate: "2026-09-20", Daily: daily}}, nil, "en")
	for _, fact := range facts {
		if fact.ID == "sleep_recent_four_day_pattern" || fact.ID == "activity_recent_steps_trend" {
			t.Fatalf("incomplete daily window leaked into packet: %#v", fact)
		}
	}
	if readinessNarrativeFresh(&BriefingResponse{ReadinessServing: nil, ReadinessToday: 0, ReadinessDisplayScore: 0}) {
		t.Fatal("missing readiness defaults became fresh")
	}
	zero := 0.0
	if !readinessNarrativeFresh(&BriefingResponse{RawMetrics: &RawMetrics{ReadinessEvidence: &ReadinessEvidenceInput{HRV: ReadinessComponentEvidence{Present: true, Value: &zero}}}}) {
		t.Fatal("present readiness evidence did not authorize a measured zero")
	}
}

func TestNarrativeFactsMakePartialSleepSemanticsExplicit(t *testing.T) {
	current, previous := 1.0, 6.8
	resp := &BriefingResponse{
		Date:         "2026-09-20",
		Sleep:        &SleepAnalysis{LatestDate: "2026-09-20", LatestTotal: &current, TotalAvg: 5.9},
		SleepQuality: &SleepQualityBreakdown{Confidence: SleepQualityConfidencePartial},
		RawMetrics: &RawMetrics{LastDate: "2026-09-20", Daily: []DailyHealthMetrics{
			{Date: "2026-09-20", Sleep: &current},
			{Date: "2026-09-19", Sleep: &previous},
		}},
	}
	snapshot := BuildDailyInsightSnapshot(resp, "en")
	facts := map[string]DailyInsightNarrativeFact{}
	for _, fact := range snapshot.NarrativeFacts {
		facts[fact.ID] = fact
	}
	for _, id := range []string{"sleep_current_recorded_duration"} {
		fact, ok := facts[id]
		if !ok || fact.Meaning != partialSleepCurrentDurationMeaning || fact.Window != partialSleepCurrentDurationWindow {
			t.Fatalf("partial-safe fact %q = %#v", id, fact)
		}
	}
	for _, locale := range []string{"en", "ru", "sr"} {
		statement := localizedNarrativePartialSleepFact(locale, current)
		if strings.Contains(strings.ToLower(statement), "sync") || strings.Contains(strings.ToLower(statement), "incomplete") || !strings.Contains(strings.ToLower(statement), "confirm") && locale == "en" {
			t.Fatalf("partial sleep statement overstates cause or omits uncertainty for %s: %q", locale, statement)
		}
	}
	for _, unsafe := range []string{"sleep_canonical_comparison", "sleep_quality", "sleep_recent_four_day_pattern"} {
		if _, ok := facts[unsafe]; ok {
			t.Fatalf("partial current sleep leaked unsafe fact %q: %#v", unsafe, facts[unsafe])
		}
	}

	resp.Sleep.LatestDate = "2026-09-19"
	stale := BuildDailyInsightSnapshot(resp, "en")
	for _, fact := range stale.NarrativeFacts {
		if fact.Domain == "sleep" {
			t.Fatalf("stale sleep entered partial-safe packet: %#v", fact)
		}
	}
}

func TestNarrativeEnergyFactDisplayValuesExcludeServerVerdictNumbers(t *testing.T) {
	facts := buildDailyInsightNarrativeFacts(&BriefingResponse{EnergyBank: &EnergyBank{
		Current: 51, Capacity: 84, DrainSoFar: 16, Strain: 11, Stress: 9,
		ActionVerdict: "moderate", VerdictReason: "HRV is 28 today.",
	}}, nil, "en")
	for _, fact := range facts {
		if fact.ID != "energy_authoritative_state" {
			continue
		}
		if got, want := strings.Join(fact.DisplayValues, ","), "51,84,16,11,9"; got != want || strings.Contains(fact.Statement, "28") {
			t.Fatalf("energy display values = %q, want %q for statement %q", got, want, fact.Statement)
		}
		return
	}
	t.Fatalf("energy fact missing: %#v", facts)
}

func TestBoundedNarrativeDailyFactsFailClosedOnGapsOrUnorderedInput(t *testing.T) {
	daily := narrativeDailyRows("2026-09-20", 11, 7, 100)
	daily[2].Date = "2026-09-16"
	if _, ok := boundedSleepPatternFact(daily, "2026-09-20", "en"); ok {
		t.Fatal("sleep fact accepted a calendar gap")
	}
	if _, ok := boundedActivityTrendFact(daily, "2026-09-20", "en"); ok {
		t.Fatal("activity fact accepted a calendar gap")
	}
	daily = narrativeDailyRows("2026-09-20", 11, 7, 100)
	daily[3], daily[4] = daily[4], daily[3]
	if _, ok := boundedSleepPatternFact(daily, "2026-09-20", "en"); ok {
		t.Fatal("sleep fact accepted unordered input")
	}
	if _, ok := boundedActivityTrendFact(daily, "2026-09-20", "en"); ok {
		t.Fatal("activity fact accepted unordered input")
	}
	daily = narrativeDailyRows("2026-09-20", 11, 7, 100)
	daily[0].Date = "2026-09-19"
	if _, ok := boundedSleepPatternFact(daily, "2026-09-20", "en"); ok {
		t.Fatal("sleep fact accepted a stale first row")
	}
	facts := buildDailyInsightNarrativeFacts(&BriefingResponse{Date: "2026-09-20", RawMetrics: &RawMetrics{LastDate: "2026-09-20", Daily: daily}}, nil, "en")
	for _, fact := range facts {
		if fact.ID == "sleep_recent_four_day_pattern" {
			t.Fatalf("stale first row entered narrative eligibility: %#v", fact)
		}
	}
}

func TestBoundedActivityTrendExcludesCurrentPartialDay(t *testing.T) {
	daily := narrativeDailyRows("2026-09-20", 11, 7, 100)
	today := 999999.0
	daily[0].Steps = &today
	fact, ok := boundedActivityTrendFact(daily, "2026-09-20", "en")
	if !ok {
		t.Fatal("activity fact missing for ten complete prior days")
	}
	if strings.Contains(fact.Statement, "999999") || len(fact.DisplayValues) != 2 || fact.DisplayValues[0] != "100" || fact.DisplayValues[1] != "100" {
		t.Fatalf("current partial activity affected trend: %#v", fact)
	}
}

func narrativeDailyRows(lastDate string, count int, sleepValue, stepsValue float64) []DailyHealthMetrics {
	last, _ := time.Parse("2006-01-02", lastDate)
	rows := make([]DailyHealthMetrics, count)
	for index := range rows {
		sleep, steps := sleepValue, stepsValue
		rows[index] = DailyHealthMetrics{Date: last.AddDate(0, 0, -index).Format("2006-01-02"), Sleep: &sleep, Steps: &steps}
	}
	return rows
}

func TestBriefingResponseJSONExcludesRawMetricsCarry(t *testing.T) {
	resp := ComputeBriefing(RawMetrics{LastDate: "2026-09-21", Daily: []DailyHealthMetrics{{Date: "private-date"}}}, "en")
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal briefing: %v", err)
	}
	if strings.Contains(string(encoded), "raw_metrics") || strings.Contains(string(encoded), "private-date") {
		t.Fatalf("briefing JSON leaked raw carry: %s", encoded)
	}
}

func narrativeTestSnapshot(t *testing.T) *DailyInsightSnapshot {
	t.Helper()
	duration := 7.2
	return BuildDailyInsightSnapshot(&BriefingResponse{Date: "2026-09-12", Sleep: &SleepAnalysis{LatestDate: "2026-09-12", LatestTotal: &duration, TotalAvg: 6.8}, ReadinessToday: 70, ReadinessTodayLabel: "Moderate", ReadinessServing: &ReadinessServingState{Status: ReadinessServingFresh, Confidence: ReadinessConfidenceFinal}, EnergyBank: &EnergyBank{Current: 56, Capacity: 80, ActionVerdict: "moderate", VerdictReason: "Current reserve is available."}}, "en")
}

func TestDailyInsightNarrativeSlotRejectsStandaloneEnergyParaphrase(t *testing.T) {
	duration := 7.2
	snapshot := BuildDailyInsightSnapshot(&BriefingResponse{
		Date: "2026-09-12", Sleep: &SleepAnalysis{LatestDate: "2026-09-12", LatestTotal: &duration, TotalAvg: 7.1},
		ReadinessToday: 42, ReadinessTodayBand: "low", ReadinessServing: &ReadinessServingState{Status: ReadinessServingFresh, Confidence: ReadinessConfidenceFinal},
		EnergyBank: &EnergyBank{Current: 45, Capacity: 80, ActionVerdict: "rest", VerdictReason: "The current reserve supports a quieter day."},
	}, "en")
	input, known := BuildDailyInsightNarrativeSlotInput(snapshot, "en", "energy")
	if !known || len(input.Slot.Claims) != 0 || input.Slot.Position != nil {
		t.Fatalf("energy slot should have no standalone model material: %#v", input)
	}
	section := DailyInsightNarrativeSlot{
		Version: DailyInsightNarrativeVersion, Locale: "en",
		Slot: DailyInsightNarrativeDomain{Key: "energy", Section: &DailyInsightNarrativeSection{Sentences: []DailyInsightNarrativeSentence{{
			Text:     "This is about energy available now, not a forecast for the day.",
			ClaimIDs: []string{"energy_current_verdict_context"}, QualifierIDs: []string{"current_context"}, MeaningIDs: []string{"energy_day_to_day_effect"},
		}}}},
	}
	if _, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, "en", "energy", section); err == nil || !strings.Contains(err.Error(), "no eligible claims") {
		t.Fatalf("standalone energy paraphrase was accepted: %v", err)
	}
}

func TestValidateDailyInsightNarrativeRejectsIndependentEnergyParaphrase(t *testing.T) {
	duration := 7.2
	snapshot := BuildDailyInsightSnapshot(&BriefingResponse{
		Date: "2026-09-12", Sleep: &SleepAnalysis{LatestDate: "2026-09-12", LatestTotal: &duration, TotalAvg: 7.1},
		ReadinessToday: 42, ReadinessTodayBand: "low", ReadinessServing: &ReadinessServingState{Status: ReadinessServingFresh, Confidence: ReadinessConfidenceFinal},
		EnergyBank: &EnergyBank{Current: 45, Capacity: 80, ActionVerdict: "rest", VerdictReason: "The current reserve supports a quieter day."},
	}, "en")
	candidate := DailyInsightNarrative{
		Version: DailyInsightNarrativeVersion,
		Locale:  "en",
		Domains: []DailyInsightNarrativeDomain{
			{Key: "sleep"},
			{Key: "recovery"},
			{Key: "energy", Section: &DailyInsightNarrativeSection{Sentences: []DailyInsightNarrativeSentence{{
				Text:     "This is about energy available now, not a forecast for the day.",
				ClaimIDs: []string{"energy_current_verdict_context"}, QualifierIDs: []string{"current_context"}, MeaningIDs: []string{"energy_day_to_day_effect"}, PositionIDs: []string{"daily_decision_position"},
			}}}},
		},
	}
	_, invalid, err := ValidateDailyInsightNarrative(snapshot, "en", candidate)
	if err != nil || invalid["energy"] == "" {
		t.Fatalf("combined validation accepted standalone energy paraphrase: invalid=%#v err=%v", invalid, err)
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

func TestDailyInsightNarrativeSlotRejectsUnsupportedReliabilityAndReaderFacingCaveats(t *testing.T) {
	snapshot := ApplyRecentSleepBelowReference(BuildDailyInsightSnapshot(&BriefingResponse{Date: "2026-09-12"}, "ru"), RecentSleepBelowReference{State: RecentSleepClaimTrue}, "ru")
	for _, text := range []string{
		"Это не случайность одной ночи.",
		"Такой рисунок делает вывод надёжнее.",
		"Данных пока недостаточно для вывода.",
		"This assessment has limited data quality.",
		"Ovo ima ograničen opseg.",
		"Несколько коротких ночей складываются в заметный рисунок сна на сегодня.",
		"Сегодня сил немного, поэтому энергию стоит распределить по дню.",
		"Сегодня у тебя больше свободы в выборе темпа дня.",
		"Several shorter nights form a real sleep pattern in today's picture.",
		"Energy is a resource to spread across the day.",
		"Imaš više slobode pri izboru ritma dana.",
	} {
		_, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, "ru", "sleep", DailyInsightNarrativeSlot{
			Version: DailyInsightNarrativeVersion,
			Locale:  "ru",
			Slot: DailyInsightNarrativeDomain{Key: "sleep", Section: &DailyInsightNarrativeSection{Sentences: []DailyInsightNarrativeSentence{{
				Text: text, ClaimIDs: []string{"recent_sleep_below_reference"}, QualifierIDs: []string{"personal_pattern", "current_context"}, MeaningIDs: []string{"sleep_personal_reference"},
			}}}},
		})
		if err == nil || !strings.Contains(err.Error(), "forbidden narrative content") {
			t.Fatalf("unsupported claim accepted for %q: %v", text, err)
		}
	}
}

func TestDailyInsightNarrativeSlotRejectsProviderAnchorSelection(t *testing.T) {
	snapshot := ApplyRecentSleepBelowReference(BuildDailyInsightSnapshot(&BriefingResponse{Date: "2026-09-12"}, "en"), RecentSleepBelowReference{State: RecentSleepClaimTrue}, "en")
	_, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, "en", "sleep", DailyInsightNarrativeSlot{
		Version: DailyInsightNarrativeVersion,
		Locale:  "en",
		Slot: DailyInsightNarrativeDomain{Key: "sleep", Section: &DailyInsightNarrativeSection{AnchorVariantID: "recent-shorter-than-usual", Sentences: []DailyInsightNarrativeSentence{{
			Text:     "The comparison stays with your own usual sleep, rather than a universal target.",
			ClaimIDs: []string{"recent_sleep_below_reference"}, QualifierIDs: []string{"personal_pattern", "current_context"}, MeaningIDs: []string{"sleep_personal_reference"},
		}}}},
	})
	if err == nil || !strings.Contains(err.Error(), "must not select") {
		t.Fatalf("unexpected provider-anchor validation error: %v", err)
	}
}

func TestDailyInsightNarrativeSlotRendersOneCompleteRichStory(t *testing.T) {
	snapshot := ApplyRecentSleepBelowReference(BuildDailyInsightSnapshot(&BriefingResponse{Date: "2026-09-12"}, "en"), RecentSleepBelowReference{State: RecentSleepClaimTrue}, "en")
	section := &DailyInsightNarrativeSection{Sentences: []DailyInsightNarrativeSentence{{
		Text: "Several recent nights were shorter than usual for you. The comparison stays with your own usual sleep rather than a universal target.", ClaimIDs: []string{"recent_sleep_below_reference"}, QualifierIDs: []string{"personal_pattern", "current_context"}, MeaningIDs: []string{"sleep_personal_reference"},
	}}}
	validated, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, "en", "sleep", DailyInsightNarrativeSlot{
		Version: DailyInsightNarrativeVersion,
		Locale:  "en",
		Slot:    DailyInsightNarrativeDomain{Key: "sleep", Section: section},
	})
	if err != nil {
		t.Fatalf("rich story rejected: %v", err)
	}
	if validated.AnchorVariantID != "" {
		t.Fatalf("rich story retained a legacy anchor: %#v", validated)
	}
	rendered, err := ApplyDailyInsightNarrativeSlot(snapshot, "en", "sleep", validated)
	if err != nil || rendered.Domains[0].Insight.Narrative == nil || rendered.Domains[0].Insight.Narrative.Text != section.Sentences[0].Text {
		t.Fatalf("rich-story overlay = %#v err=%v", rendered.Domains[0].Insight.Narrative, err)
	}
}

func TestDailyInsightNarrativeSlotExposesServerFormattedFactsAndRejectsInventedNumbers(t *testing.T) {
	snapshot := ApplyRecentSleepBelowReference(BuildDailyInsightSnapshot(&BriefingResponse{Date: "2026-09-12"}, "en"), RecentSleepBelowReference{State: RecentSleepClaimTrue, ReferenceHours: 7.8, CurrentShortNightCount: 3}, "en")
	input, known := BuildDailyInsightNarrativeSlotInput(snapshot, "en", "sleep")
	if !known || input.Slot.Story == nil || len(input.Slot.Facts) == 0 {
		t.Fatalf("rich sleep packet = %#v, known=%v", input, known)
	}
	foundDisplayValue := false
	for _, fact := range input.Slot.Facts {
		foundDisplayValue = foundDisplayValue || len(fact.DisplayValues) > 0
	}
	if !foundDisplayValue {
		t.Fatalf("packet has no display values: %#v", input.Slot.Facts)
	}
	valid := DailyInsightNarrativeSlot{Version: DailyInsightNarrativeVersion, Locale: "en", Slot: DailyInsightNarrativeDomain{Key: "sleep", Section: &DailyInsightNarrativeSection{Sentences: []DailyInsightNarrativeSentence{{
		Text: "Three shorter nights in the last four make the pattern worth carrying into today's picture.", ClaimIDs: []string{"recent_sleep_below_reference"}, QualifierIDs: []string{"personal_pattern", "current_context"}, MeaningIDs: []string{"sleep_personal_reference"},
	}}}}}
	if _, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, "en", "sleep", valid); err != nil {
		t.Fatalf("server-supplied number rejected: %v", err)
	}
	valid.Slot.Section.Sentences[0].Text = "9 shorter nights in the last four make the pattern worth carrying into today's picture."
	if _, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, "en", "sleep", valid); err == nil || !strings.Contains(err.Error(), "display catalog") {
		t.Fatalf("invented number accepted: %v", err)
	}
}

func TestDailyInsightNarrativeSlotRejectsUnsupportedConsecutiveSleepStreak(t *testing.T) {
	snapshot := ApplyRecentSleepBelowReference(BuildDailyInsightSnapshot(&BriefingResponse{Date: "2026-09-12"}, "en"), RecentSleepBelowReference{State: RecentSleepClaimTrue, ReferenceHours: 7.8, CurrentShortNightCount: 3}, "en")
	candidate := DailyInsightNarrativeSlot{Version: DailyInsightNarrativeVersion, Locale: "en", Slot: DailyInsightNarrativeDomain{Key: "sleep", Section: &DailyInsightNarrativeSection{Sentences: []DailyInsightNarrativeSentence{{
		Text: "Three shorter nights in a row make a calmer end to today more fitting.", ClaimIDs: []string{"recent_sleep_below_reference"}, QualifierIDs: []string{"personal_pattern", "current_context"}, MeaningIDs: []string{"sleep_personal_reference"},
	}}}}}
	if _, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, "en", "sleep", candidate); err == nil || !strings.Contains(err.Error(), "overstates the four-night count") {
		t.Fatalf("unsupported consecutive streak accepted: %v", err)
	}
}

func TestDailyInsightNarrativeSlotRequiresExactlyOneMeaning(t *testing.T) {
	snapshot := ApplyRecentSleepBelowReference(BuildDailyInsightSnapshot(&BriefingResponse{Date: "2026-09-12"}, "en"), RecentSleepBelowReference{State: RecentSleepClaimTrue}, "en")
	section := &DailyInsightNarrativeSection{
		Sentences: []DailyInsightNarrativeSentence{{
			Text:         "The comparison stays with your own usual sleep, rather than a universal target.",
			ClaimIDs:     []string{"recent_sleep_below_reference"},
			QualifierIDs: []string{"personal_pattern", "current_context"},
			MeaningIDs:   []string{"sleep_personal_reference", "sleep_personal_reference"},
		}},
	}
	_, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, "en", "sleep", DailyInsightNarrativeSlot{
		Version: DailyInsightNarrativeVersion,
		Locale:  "en",
		Slot:    DailyInsightNarrativeDomain{Key: "sleep", Section: section},
	})
	if err == nil || !strings.Contains(err.Error(), "exactly one meaning") {
		t.Fatalf("multiple meanings error = %v", err)
	}
}

func TestDailyInsightNarrativeSlotAllowsFactInCompleteStory(t *testing.T) {
	snapshot := ApplyRecentSleepBelowReference(BuildDailyInsightSnapshot(&BriefingResponse{Date: "2026-09-12"}, "en"), RecentSleepBelowReference{State: RecentSleepClaimTrue}, "en")
	section := &DailyInsightNarrativeSection{
		Sentences: []DailyInsightNarrativeSentence{{
			Text:         "Several recent nights were shorter than usual for you. Several recent nights were shorter than usual for you. The repeated pattern is more meaningful than one short night.",
			ClaimIDs:     []string{"recent_sleep_below_reference"},
			QualifierIDs: []string{"personal_pattern", "current_context"},
			MeaningIDs:   []string{"sleep_personal_reference"},
		}},
	}
	_, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, "en", "sleep", DailyInsightNarrativeSlot{
		Version: DailyInsightNarrativeVersion,
		Locale:  "en",
		Slot:    DailyInsightNarrativeDomain{Key: "sleep", Section: section},
	})
	if err != nil {
		t.Fatalf("complete story unexpectedly rejected: %v", err)
	}
}

func TestDailyInsightNarrativeSlotAllowsNaturalFactParaphrase(t *testing.T) {
	snapshot := ApplyRecentSleepBelowReference(BuildDailyInsightSnapshot(&BriefingResponse{Date: "2026-09-12"}, "en"), RecentSleepBelowReference{State: RecentSleepClaimTrue}, "en")
	for _, text := range []string{
		"Several, recent nights were shorter than usual for you; the repeated pattern matters more than one night.",
		"You slept less than usual over several recent nights, which makes the pattern more meaningful than one night.",
	} {
		_, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, "en", "sleep", DailyInsightNarrativeSlot{
			Version: DailyInsightNarrativeVersion,
			Locale:  "en",
			Slot: DailyInsightNarrativeDomain{Key: "sleep", Section: &DailyInsightNarrativeSection{Sentences: []DailyInsightNarrativeSentence{{
				Text: text, ClaimIDs: []string{"recent_sleep_below_reference"}, QualifierIDs: []string{"personal_pattern", "current_context"}, MeaningIDs: []string{"sleep_personal_reference"},
			}}}},
		})
		if err != nil {
			t.Fatalf("natural fact paraphrase rejected for %q: %v", text, err)
		}
	}
}

func TestDailyInsightNarrativeAnchorCatalogFingerprintIsNonEmpty(t *testing.T) {
	if fingerprint := DailyInsightNarrativeAnchorCatalogFingerprint(); len(fingerprint) != 64 {
		t.Fatalf("anchor catalog fingerprint = %q", fingerprint)
	}
	if fingerprint := DailyInsightNarrativeMeaningCatalogFingerprint(); len(fingerprint) != 64 {
		t.Fatalf("meaning catalog fingerprint = %q", fingerprint)
	}
}

func TestDailyInsightNarrativeAnchorsAreCompleteEverydaySentences(t *testing.T) {
	if got, want := localizedRecoveryNarrativeAnchors("ru", "low")[0].Text, "Сегодня восстановление не на пике."; got != want {
		t.Fatalf("low recovery anchor = %q, want %q", got, want)
	}
	if got, want := localizedEnergyNarrativeAnchors("ru", "rest")[0].Text, "Сегодня сил немного."; got != want {
		t.Fatalf("rest energy anchor = %q, want %q", got, want)
	}
	if got, want := localizedSleepNarrativeAnchors("sr")[0].Text, "Nekoliko poslednjih noći spavao si manje nego što je za tebe uobičajeno."; got != want {
		t.Fatalf("Serbian sleep anchor = %q, want %q", got, want)
	}
}

func TestDailyInsightNarrativeSlotRequiresSerbianLatinScript(t *testing.T) {
	snapshot := ApplyRecentSleepBelowReference(BuildDailyInsightSnapshot(&BriefingResponse{Date: "2026-09-12"}, "sr"), RecentSleepBelowReference{State: RecentSleepClaimTrue}, "sr")
	_, err := ValidateDailyInsightNarrativeSlotResponse(snapshot, "sr", "sleep", DailyInsightNarrativeSlot{
		Version: DailyInsightNarrativeVersion,
		Locale:  "sr",
		Slot: DailyInsightNarrativeDomain{Key: "sleep", Section: &DailyInsightNarrativeSection{Sentences: []DailyInsightNarrativeSentence{{
			Text:     "Нekoliko poslednjih noći odstupa od tvog uobičajenog sna.",
			ClaimIDs: []string{"recent_sleep_below_reference"}, QualifierIDs: []string{"personal_pattern", "current_context"}, MeaningIDs: []string{"sleep_personal_reference"},
		}}}},
	})
	if err == nil || !strings.Contains(err.Error(), "Latin script") {
		t.Fatalf("unexpected Serbian script validation error: %v", err)
	}
}

func TestDailyInsightNarrativeWithholdsStandaloneRecoveryAndEnergyParaphrases(t *testing.T) {
	duration := 7.2
	snapshot := BuildDailyInsightSnapshot(&BriefingResponse{
		Date:               "2026-09-12",
		Sleep:              &SleepAnalysis{LatestDate: "2026-09-12", LatestTotal: &duration, TotalAvg: 7.1},
		ReadinessToday:     42,
		ReadinessTodayBand: "low",
		ReadinessTip:       "Recovery signals are more limited today.",
		ReadinessServing:   &ReadinessServingState{Status: ReadinessServingFresh, Confidence: ReadinessConfidenceFinal},
		EnergyBank:         &EnergyBank{Current: 45, Capacity: 80, ActionVerdict: "rest", VerdictReason: "The current reserve supports a quieter day."},
	}, "en")
	input := BuildDailyInsightNarrativeInput(snapshot, "en")
	if got := narrativeInputClaimIDs(input, "recovery"); len(got) != 0 {
		t.Fatalf("recovery claims = %#v; a single readiness card is deterministic-only", got)
	}
	if got := narrativeInputClaimIDs(input, "energy"); len(got) != 0 {
		t.Fatalf("energy claims = %#v; a single energy card is deterministic-only", got)
	}
	if HasEligibleDailyInsightNarrativeSlot(snapshot, "en", "recovery") {
		t.Fatal("standalone recovery card unexpectedly eligible for B1")
	}
	if HasEligibleDailyInsightNarrativeSlot(ApplyRecentSleepBelowReference(snapshot, RecentSleepBelowReference{State: RecentSleepClaimTrue, CurrentShortNightCount: 3}, "en"), "en", "sleep") {
		t.Fatal("standalone B0 sleep pattern unexpectedly eligible for B1")
	}
	if got := dailyInsightDomain(t, snapshot, "energy").Band; got == "active_recovery" {
		t.Fatalf("energy display band was overwritten by narrative subject")
	}
	if got := dailyInsightDomain(t, snapshot, "energy").NarrativeSubject; got != "rest" {
		t.Fatalf("energy narrative subject = %q", got)
	}
}

func TestDailyInsightNarrativeWithholdsEveryStandaloneEnergyVerdict(t *testing.T) {
	for _, verdict := range []string{"push_hard", "rest", "active_recovery"} {
		snapshot := BuildDailyInsightSnapshot(&BriefingResponse{
			Date:       "2026-09-12",
			EnergyBank: &EnergyBank{Current: 45, Capacity: 80, ActionVerdict: verdict, VerdictReason: "The current reserve supports a quieter day."},
		}, "en")
		input, known := BuildDailyInsightNarrativeSlotInput(snapshot, "en", "energy")
		if !known || len(input.Slot.Claims) != 0 || input.Slot.Story != nil {
			t.Fatalf("%s energy should have no permitted standalone B1 meaning: %#v", verdict, input)
		}
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
	if got, want := localizedEnergyNarrativeAnchors("ru", "active_recovery")[0].Text, "Сегодня лучше не добавлять интенсивности."; got != want {
		t.Fatalf("Russian active-recovery anchor = %q, want %q", got, want)
	}
}

func TestRecoveryComponentNarrativeFactsRequireConfirmedCurrentCoverage(t *testing.T) {
	hrv, rhr := 54.0, 51.0
	evidence := &ReadinessEvidenceInput{Date: "2026-09-20",
		HRV: ReadinessComponentEvidence{Metric: "heart_rate_variability", Value: &hrv, Present: true, EvaluatedDate: "2026-09-20", SourceDate: "2026-09-20", Freshness: ReadinessFreshnessOK, SampleCount: MinSleepWindowHRVSamplesForFullConfidence, Confidence: ReadinessConfidenceFinal},
		RHR: ReadinessComponentEvidence{Metric: "resting_heart_rate", Value: &rhr, Present: true, EvaluatedDate: "2026-09-20", SourceDate: "2026-09-20", Freshness: ReadinessFreshnessOK, SampleCount: 1, Confidence: ReadinessConfidenceFinal},
	}
	resp := &BriefingResponse{Date: "2026-09-20", Headline: &HeadlineSignal{Metrics: []HeadlineMetricDelta{{Metric: "heart_rate_variability", Value: hrv, Baseline: 49, Unit: "ms"}}},
		RawMetrics: &RawMetrics{LastDate: "2026-09-20", Daily: []DailyHealthMetrics{{Date: "2026-09-20", HRV: &hrv, RHR: &rhr}}, ReadinessEvidence: evidence}}
	domains := []DailyInsightDomain{{Key: "recovery", DataState: "fresh", Confidence: "final", Insight: DailyInsight{State: "insight", AnswerKind: DailyInsightAnswerFactual}}}

	hrvFact, ok := readinessComponentNarrativeFact(resp, domains, "en", evidence.HRV)
	if !ok || hrvFact.ID != "readiness_hrv_current" || hrvFact.Window != "today versus confirmed personal baseline" || len(hrvFact.DisplayValues) != 3 || !strings.Contains(hrvFact.Statement, "personal baseline") {
		t.Fatalf("fresh HRV fact = %#v, ok=%v", hrvFact, ok)
	}
	rhrFact, ok := readinessComponentNarrativeFact(resp, domains, "en", evidence.RHR)
	if !ok || rhrFact.ID != "readiness_rhr_current" || rhrFact.Window != "today, confirmed same-day coverage" || len(rhrFact.DisplayValues) != 1 || strings.Contains(rhrFact.Statement, "baseline") {
		t.Fatalf("baseline-free RHR fact = %#v, ok=%v", rhrFact, ok)
	}
	for _, locale := range []string{"en", "ru", "sr"} {
		for _, count := range []int{1, 4} {
			component := evidence.RHR
			component.SampleCount = count
			fact, ok := readinessComponentNarrativeFact(resp, domains, locale, component)
			if !ok || len(fact.DisplayValues) != 1 || strings.Contains(fact.Statement, ";") || strings.Contains(fact.Meaning, "count") {
				t.Fatalf("%s RHR record count became a quality cue: %#v", locale, fact)
			}
		}
	}

	for name, component := range map[string]ReadinessComponentEvidence{
		"partial":      {Metric: "heart_rate_variability", Value: &hrv, Present: true, EvaluatedDate: resp.Date, SourceDate: resp.Date, Freshness: ReadinessFreshnessOK, SampleCount: 2, Confidence: ReadinessConfidenceProvisional},
		"missing":      {Metric: "heart_rate_variability", Present: false, EvaluatedDate: resp.Date, SourceDate: resp.Date, Freshness: ReadinessFreshnessMissing, Confidence: ReadinessConfidenceLow},
		"wrong date":   {Metric: "heart_rate_variability", Value: &hrv, Present: true, EvaluatedDate: resp.Date, SourceDate: "2026-09-19", Freshness: ReadinessFreshnessOK, SampleCount: 4, Confidence: ReadinessConfidenceFinal},
		"low coverage": {Metric: "heart_rate_variability", Value: &hrv, Present: true, EvaluatedDate: resp.Date, SourceDate: resp.Date, Freshness: ReadinessFreshnessOK, SampleCount: 3, Confidence: ReadinessConfidenceFinal},
	} {
		if fact, ok := readinessComponentNarrativeFact(resp, domains, "en", component); ok {
			t.Fatalf("%s component entered B1: %#v", name, fact)
		}
	}
	evidence.Date = "2026-09-19"
	if fact, ok := readinessComponentNarrativeFact(resp, domains, "en", evidence.HRV); ok {
		t.Fatalf("mismatched ReadinessEvidence date entered B1: %#v", fact)
	}
}

func TestLocalizedRecoveryComponentFactUsesLocalizedMetricAndSampleGrammar(t *testing.T) {
	for _, tc := range []struct {
		name, locale, metric, want string
		samples                    int
	}{
		{"english singular", "en", "resting heart rate", "resting heart rate: 51.0 bpm; 1 same-day sample.", 1},
		{"english plural", "en", "resting heart rate", "resting heart rate: 51.0 bpm; 4 same-day samples.", 4},
		{"russian singular", "ru", "resting heart rate", "пульса в покое: 51.0 bpm; 1 измерение за этот день.", 1},
		{"russian plural", "ru", "resting heart rate", "пульса в покое: 51.0 bpm; 4 измерения за этот день.", 4},
		{"serbian singular", "sr", "resting heart rate", "pulsa u mirovanju: 51.0 bpm; 1 merenje za taj dan.", 1},
		{"serbian plural", "sr", "resting heart rate", "pulsa u mirovanju: 51.0 bpm; 4 merenja za taj dan.", 4},
		{"serbian twenty-one", "sr", "resting heart rate", "pulsa u mirovanju: 51.0 bpm; 21 merenje za taj dan.", 21},
		{"serbian twenty-two", "sr", "resting heart rate", "pulsa u mirovanju: 51.0 bpm; 22 merenja za taj dan.", 22},
		{"serbian eleven", "sr", "resting heart rate", "pulsa u mirovanju: 51.0 bpm; 11 merenja za taj dan.", 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := localizedNarrativeRecoveryComponentFact(tc.locale, tc.metric, "bpm", 51, tc.samples, 0, false)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("localized fact = %q, want fragment %q", got, tc.want)
			}
		})
	}
	withBaseline := localizedNarrativeRecoveryComponentFact("ru", "resting heart rate", "bpm", 51, 1, 48, true)
	if !strings.Contains(withBaseline, "пульса в покое: 51.0 bpm") || !strings.Contains(withBaseline, "48.0 bpm") || !strings.Contains(withBaseline, "1 измерение") {
		t.Fatalf("baseline fact lost exact RHR values or singular grammar: %q", withBaseline)
	}
}

func TestSerbianServerInsightCopyAvoidsFormalAddressOnPacketPaths(t *testing.T) {
	copy := dailyInsightCopy("sr")
	texts := []string{
		localizedSleepComparison(copy, 7, 7),
		localizedSleepComparison(copy, 8, 7),
		localizedSleepComparison(copy, 6, 7),
	}
	for _, reason := range []string{GetStrings("sr")["energy_reason_low_capacity"], GetStrings("sr")["energy_reason_recovery_debt"]} {
		energy := buildEnergyInsightDomain(&BriefingResponse{Date: "2026-09-20", EnergyBank: &EnergyBank{Current: 20, Capacity: 80, ActionVerdict: "active_recovery", VerdictReason: reason}}, nil, copy)
		texts = append(texts, energy.Insight.Observation)
	}
	for _, text := range texts {
		if strings.Contains(strings.ToLower(text), "vaš") || strings.Contains(strings.ToLower(text), "držite") {
			t.Fatalf("formal Serbian B0 copy remained on Server Insight/packet path: %q", text)
		}
	}
}

func TestSerbianFairReadinessTipReachesRecoveryServerInsightWithoutFormalOrMixedCopy(t *testing.T) {
	label, tip := readinessLabelTip(65, GetStrings("sr"))
	if tip != "Malo odstupanje od lične norme. Umerena aktivnost je dobar izbor." {
		t.Fatalf("fair Serbian readiness tip = %q", tip)
	}
	snapshot := BuildDailyInsightSnapshot(&BriefingResponse{Date: "2026-09-20", ReadinessToday: 65, ReadinessTodayLabel: label, ReadinessTip: tip,
		ReadinessServing: &ReadinessServingState{Status: ReadinessServingFresh, Confidence: ReadinessConfidenceFinal}}, "sr")
	recovery := dailyInsightDomain(t, snapshot, "recovery")
	if recovery.Insight.Observation != tip || strings.Contains(strings.ToLower(recovery.Insight.Observation), "vaše") || strings.Contains(recovery.Insight.Observation, "Umjerena") {
		t.Fatalf("Recovery Server Insight retained formal or mixed Serbian copy: %#v", recovery.Insight)
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
