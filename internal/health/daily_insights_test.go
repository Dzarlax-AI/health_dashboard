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
	section := &DailyInsightNarrativeSection{Sentences: []DailyInsightNarrativeSentence{{
		Text:     "The recommendation brings recovery and energy together instead of relying on one measure.",
		ClaimIDs: []string{"overall_daily_decision_context"}, QualifierIDs: []string{"current_context"}, MeaningIDs: []string{"overall_combined_context"},
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
