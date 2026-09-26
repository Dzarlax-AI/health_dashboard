package health

import (
	"encoding/json"
	"strings"
	"testing"
)

func aiInsightTestSnapshot() *DailyInsightSnapshot {
	return &DailyInsightSnapshot{
		Date: "2026-09-24",
		Primary: DailyInsight{State: "insight", Observation: "A measured day is reasonable.", Meaning: "Recovery is mixed.",
			NextStep: &DailyInsightAction{ID: "moderate", Text: "Keep activity light."}},
		Domains: []DailyInsightDomain{{Key: "sleep", DataState: "fresh", Summary: "Sleep was shorter than usual.",
			Insight: DailyInsight{State: "insight", Observation: "Sleep was short.", Meaning: "This is one signal."}},
			{Key: "recovery", DataState: "stale", Insight: DailyInsight{State: "insight", Observation: "Old recovery signal."}}},
		NarrativeFacts: []DailyInsightNarrativeFact{{ID: "sleep-short", Domain: "sleep", Statement: "Sleep was shorter than usual.", Fresh: true,
			EvidenceIDs: []string{"sleep-evidence"}, DisplayValues: []string{"6.5"}},
			{ID: "recovery-old", Domain: "recovery", Statement: "Old recovery signal.", Fresh: false,
				EvidenceIDs: []string{"recovery-evidence"}}},
	}
}

func TestAIInsightInputSeparatesDomainsAndBindsOverallSiblings(t *testing.T) {
	snapshot := aiInsightTestSnapshot()
	sleep, ok := BuildAIInsightInput(snapshot, "en", "sleep", nil)
	if !ok || len(sleep.Facts) != 1 || sleep.Facts[0].ID != "sleep-short" || sleep.ServerInsight.Observation != "Sleep was short." {
		t.Fatalf("sleep input = %#v eligible=%v", sleep, ok)
	}
	if _, ok := BuildAIInsightInput(snapshot, "en", "recovery", nil); ok {
		t.Fatal("stale recovery became eligible")
	}
	overall, ok := BuildAIInsightInput(snapshot, "en", "overall", []AIInsightSibling{{Slot: "sleep", State: "ready", Text: "A quiet day still has room for a walk."}})
	if !ok || len(overall.Facts) != 1 || len(overall.Siblings) != 1 {
		t.Fatalf("overall input = %#v eligible=%v", overall, ok)
	}
	changed := overall
	changed.Siblings = []AIInsightSibling{{Slot: "sleep", State: "ready", Text: "Different reading."}}
	if AIInsightInputHash(overall) == AIInsightInputHash(changed) {
		t.Fatal("overall hash ignored a changed sibling opinion")
	}
}

func TestAIInsightInputKeepsServerOpinionWithoutDuplicateVisibleCopy(t *testing.T) {
	input, eligible := BuildAIInsightInput(aiInsightTestSnapshot(), "en", "sleep", nil)
	if !eligible || input.ServerInsight.Observation == "" {
		t.Fatalf("missing separate Server Insight: %#v", input)
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"visible_copy"`) {
		t.Fatalf("duplicate server prose remains in packet: %s", encoded)
	}
}

func TestAIInsightInputDeduplicatesRecoveryHeadlineWhenComponentHasSameMetric(t *testing.T) {
	snapshot := aiInsightTestSnapshot()
	snapshot.Domains[1].DataState = "fresh"
	snapshot.NarrativeFacts = []DailyInsightNarrativeFact{
		{ID: "readiness_hrv_current", Domain: "recovery", Fresh: true, Statement: "HRV 54 ms versus 49 ms personal baseline.", DisplayValues: []string{"54", "49"}, EvidenceIDs: []string{"component"}},
		{ID: "headline_heart_rate_variability", Domain: "recovery", Fresh: true, Statement: "HRV 54 ms versus 49 ms personal baseline.", DisplayValues: []string{"54", "49"}, EvidenceIDs: []string{"headline"}},
		{ID: "readiness_rhr_current", Domain: "recovery", Fresh: true, Statement: "Resting heart rate 51 bpm today.", DisplayValues: []string{"51"}, EvidenceIDs: []string{"rhr"}},
	}
	input, eligible := BuildAIInsightInput(snapshot, "en", "recovery", nil)
	if !eligible || len(input.Facts) != 2 || input.Facts[0].ID != "readiness_hrv_current" || input.Facts[1].ID != "readiness_rhr_current" {
		t.Fatalf("duplicate headline reached Luna: %#v eligible=%v", input.Facts, eligible)
	}
}

func TestAIInsightEnergyFactContainsMeasurementsNotRepeatedServerVerdict(t *testing.T) {
	snapshot := BuildDailyInsightSnapshot(&BriefingResponse{Date: "2026-09-26", EnergyBank: &EnergyBank{
		Current: 42, Capacity: 90, DrainSoFar: 8, Strain: 3, Stress: 2,
		ActionVerdict: "moderate", VerdictLabel: "Moderate day", VerdictReason: "Unique server conclusion.",
	}}, "en")
	if snapshot == nil {
		t.Fatal("missing snapshot")
	}
	for _, fact := range snapshot.NarrativeFacts {
		if fact.ID != "energy_authoritative_state" {
			continue
		}
		if !strings.Contains(fact.Statement, "42") || !strings.Contains(fact.Statement, "90") || strings.Contains(fact.Statement, "Unique server conclusion") || strings.Contains(fact.Statement, "Moderate day") || strings.Contains(fact.Meaning, "non-comparable") {
			t.Fatalf("energy fact duplicates server interpretation: %#v", fact)
		}
		return
	}
	t.Fatal("missing Energy measurements fact")
}

func TestAIInsightOverallCanUseFreshFactsWhenServerPrimaryHasDataGap(t *testing.T) {
	snapshot := aiInsightTestSnapshot()
	snapshot.Primary.State = "insufficient_data"
	snapshot.Primary.AnswerKind = DailyInsightAnswerDataGuidance
	snapshot.Primary.GapReason = "recovery_data_accruing"
	snapshot.Primary.Remediation = "sync_recovery"
	input, eligible := BuildAIInsightInput(snapshot, "en", "overall", nil)
	if !eligible || input.ServerInsight.State != "insufficient_data" || input.ServerInsight.GapReason != "recovery_data_accruing" || len(input.Facts) != 1 || len(input.DomainStates) != 2 || input.DomainStates[1].DataState != "stale" {
		t.Fatalf("overall input = %#v eligible=%v", input, eligible)
	}
	snapshot.NarrativeFacts[0].Fresh = false
	if _, eligible := BuildAIInsightInput(snapshot, "en", "overall", nil); eligible {
		t.Fatal("overall without fresh facts became eligible")
	}
}

func TestAIInsightDoesNotTreatPartialSleepAsCompletedNight(t *testing.T) {
	snapshot := aiInsightTestSnapshot()
	snapshot.Domains[0].DataState = "partial"
	snapshot.Domains[0].Insight.State = "insufficient_data"
	snapshot.NarrativeFacts = []DailyInsightNarrativeFact{
		{ID: "sleep_canonical_comparison", Domain: "sleep", Fresh: true, Statement: "Canonical sleep duration: 1.0 h against a recent average of 5.9 h.", DisplayValues: []string{"1.0", "5.9"}, EvidenceIDs: []string{"sleep-current"}},
		{ID: "sleep_recent_four_day_pattern", Domain: "sleep", Fresh: true, Statement: "The newest two-day average is 4.7 h.", DisplayValues: []string{"4.7"}, EvidenceIDs: []string{"sleep-pattern"}},
		{ID: "energy_authoritative_state", Domain: "energy", Fresh: true, Statement: "Energy reserve is 52 of 93.", DisplayValues: []string{"52", "93"}, EvidenceIDs: []string{"energy"}},
	}
	input, eligible := BuildAIInsightInput(snapshot, "en", "overall", nil)
	if !eligible || len(input.Facts) != 1 || input.Facts[0].ID != "energy_authoritative_state" || input.DomainStates[0].DataState != "partial" {
		t.Fatalf("partial sleep leaked into overall packet: %#v, eligible=%v", input, eligible)
	}
	if _, eligible := BuildAIInsightInput(snapshot, "en", "sleep", nil); eligible {
		t.Fatal("partial sleep became a domain opinion")
	}
}

func TestAIInsightExplainsDistinctSleepComparisonWindows(t *testing.T) {
	snapshot := aiInsightTestSnapshot()
	snapshot.NarrativeFacts = []DailyInsightNarrativeFact{
		{ID: "sleep_canonical_comparison", Domain: "sleep", Fresh: true, Statement: "Canonical sleep duration: 7.5 h against a recent average of 7.0 h.", EvidenceIDs: []string{"sleep-current"}},
		{ID: "headline_sleep_total", Domain: "sleep", Fresh: true, Statement: "Sleep shift: 7.5 against a 6.8 baseline.", EvidenceIDs: []string{"sleep-headline"}},
	}
	input, eligible := BuildAIInsightInput(snapshot, "en", "sleep", nil)
	if !eligible || len(input.Facts) != 2 || !strings.Contains(input.Facts[0].Meaning, "including latest") || !strings.Contains(input.Facts[1].Meaning, "older") {
		t.Fatalf("sleep comparison windows remain ambiguous: %#v, eligible=%v", input, eligible)
	}
}

func TestAIInsightRecoveryReceivesFreshEnergyContext(t *testing.T) {
	snapshot := aiInsightTestSnapshot()
	snapshot.Domains[1] = DailyInsightDomain{Key: "recovery", DataState: "fresh", Confidence: "final",
		Insight: DailyInsight{State: "insight", Observation: "Readiness supports a normal session."}}
	snapshot.Domains = append(snapshot.Domains, DailyInsightDomain{Key: "energy", DataState: "fresh", Confidence: "final",
		Insight: DailyInsight{State: "insight", Observation: "Reserve is low; keep intensity light."}})
	snapshot.NarrativeFacts = append(snapshot.NarrativeFacts,
		DailyInsightNarrativeFact{ID: "readiness_current", Domain: "recovery", Fresh: true, Statement: "Readiness is optimal.", EvidenceIDs: []string{"recovery-evidence"}},
		DailyInsightNarrativeFact{ID: "energy_authoritative_state", Domain: "energy", Fresh: true, Statement: "Reserve is low; keep intensity light.", EvidenceIDs: []string{"energy-evidence"}})
	input, eligible := BuildAIInsightInput(snapshot, "en", "recovery", nil)
	if !eligible || len(input.Facts) != 2 || input.Facts[0].ID != "readiness_current" || input.Facts[1].ID != "energy_authoritative_state" || len(input.DomainStates) != 2 || input.DomainStates[1].DataState != "fresh" {
		t.Fatalf("Recovery lacks same-day Energy context: %#v eligible=%v", input, eligible)
	}
	changed := *snapshot
	changed.NarrativeFacts = append([]DailyInsightNarrativeFact(nil), snapshot.NarrativeFacts...)
	changed.NarrativeFacts[3].Statement = "Reserve is high; a normal session is fine."
	other, eligible := BuildAIInsightInput(&changed, "en", "recovery", nil)
	if !eligible || AIInsightInputHash(input) == AIInsightInputHash(other) {
		t.Fatal("Recovery cache hash ignored changed Energy context")
	}
	// A provisional Energy reading is disclosed as a state, not promoted to
	// a fresh fact that could justify a new cross-domain recommendation.
	snapshot.Domains[2].DataState, snapshot.Domains[2].Confidence = "partial", "provisional"
	partial, eligible := BuildAIInsightInput(snapshot, "en", "recovery", nil)
	if !eligible || len(partial.Facts) != 1 || len(partial.DomainStates) != 2 || partial.DomainStates[1].DataState != "partial" {
		t.Fatalf("partial Energy became Recovery evidence: %#v eligible=%v", partial, eligible)
	}
}

func TestAIInsightRecoveryMustCiteOwnDomainWithEnergyContext(t *testing.T) {
	snapshot := aiInsightTestSnapshot()
	snapshot.Domains[1].DataState = "fresh"
	snapshot.NarrativeFacts = []DailyInsightNarrativeFact{
		{ID: "readiness_current", Domain: "recovery", Fresh: true, Statement: "Readiness is fair.", EvidenceIDs: []string{"recovery-evidence"}},
		{ID: "energy_authoritative_state", Domain: "energy", Fresh: true, Statement: "Energy is low.", EvidenceIDs: []string{"energy-evidence"}},
	}
	snapshot.Domains = append(snapshot.Domains, DailyInsightDomain{Key: "energy", DataState: "fresh", Confidence: "final",
		Insight: DailyInsight{State: "insight", Observation: "Energy is low."}})
	input, eligible := BuildAIInsightInput(snapshot, "en", "recovery", nil)
	if !eligible {
		t.Fatal("Recovery input missing")
	}
	_, err := ValidateAIInsightSlot(input, AIInsightSlotResponse{Version: AIInsightVersion, Locale: "en", Slot: "recovery", Insight: &AIInsightSection{
		Text: "Energy is low, so take an easy day.", FactIDs: []string{"energy_authoritative_state"}, Stance: "qualify",
	}})
	if err == nil {
		t.Fatal("Recovery card accepted an Energy-only opinion")
	}
}

func TestAIInsightAcceptsGroundedDisagreementAndLongNaturalText(t *testing.T) {
	input, ok := BuildAIInsightInput(aiInsightTestSnapshot(), "en", "overall", nil)
	if !ok {
		t.Fatal("overall input missing")
	}
	longText := strings.Repeat("I would keep the day flexible rather than treat the server action as a rule. ", 8)
	candidate := AIInsightSlotResponse{Version: AIInsightVersion, Locale: "en", Slot: "overall", Insight: &AIInsightSection{
		Text: longText, FactIDs: []string{"sleep-short"}, Stance: "disagree", AlternativeAction: "Try a familiar easy walk.",
	}}
	insight, err := ValidateAIInsightSlot(input, candidate)
	if err != nil || insight == nil || insight.Stance != "disagree" || insight.EvidenceIDs[0] != "sleep-evidence" {
		t.Fatalf("validated = %#v err=%v", insight, err)
	}
	if _, err := ValidateAIInsightSlot(input, AIInsightSlotResponse{Version: AIInsightVersion, Locale: "en", Slot: "overall",
		Insight: &AIInsightSection{Text: "Your score is 99 today.", FactIDs: []string{"sleep-short"}, Stance: "agree"}}); err == nil {
		t.Fatal("invented number passed validation")
	}
	if _, err := ValidateAIInsightSlot(input, AIInsightSlotResponse{Version: AIInsightVersion, Locale: "en", Slot: "overall",
		Insight: &AIInsightSection{Text: "A useful day remains possible.", FactIDs: []string{"missing"}, Stance: "agree"}}); err == nil {
		t.Fatal("unknown fact ID passed validation")
	}
}

func TestAIInsightRejectsUnexpectedScriptInSerbian(t *testing.T) {
	input, ok := BuildAIInsightInput(aiInsightTestSnapshot(), "sr", "overall", nil)
	if !ok {
		t.Fatal("overall input missing")
	}
	for _, text := range []string{"Danas je plan fleksibilan מח.", "Danas je plan fleksibilan Ђ."} {
		_, err := ValidateAIInsightSlot(input, AIInsightSlotResponse{Version: AIInsightVersion, Locale: "sr", Slot: "overall", Insight: &AIInsightSection{
			Text: text, FactIDs: []string{"sleep-short"}, Stance: "qualify",
		}})
		if err == nil {
			t.Fatalf("unexpected script passed: %q", text)
		}
	}
}

func TestAIInsightRejectsEditingArtifactsAndGenderedAddress(t *testing.T) {
	for _, tc := range []struct {
		locale string
		text   string
	}{
		{"sr", "Danas biraj lakši tempo. [Correction: rewrite this sentence.]"},
		{"ru", "Прошлой ночью ты спал(а) дольше обычного."},
		{"ru", "Сегодня ты готов(а) к нагрузке."},
		{"ru", "Сегодня вы спали дольше обычного."},
		{"ru", "Ты сегодня спал дольше обычного."},
		{"ru", "Ты восстановился после сна."},
		{"sr", "Spavala/spavao si duže nego obično."},
		{"sr", "To ne znači da si potpuno oporavljen/a."},
		{"sr", "Poslednjih dana si se više kretao/la."},
		{"sr", "Danas si spavao duže nego obično."},
		{"sr", "Sačuvaj mirniji tempo.\u200c"},
	} {
		input, eligible := BuildAIInsightInput(aiInsightTestSnapshot(), tc.locale, "overall", nil)
		if !eligible {
			t.Fatal("overall input missing")
		}
		_, err := ValidateAIInsightSlot(input, AIInsightSlotResponse{Version: AIInsightVersion, Locale: tc.locale, Slot: "overall", Insight: &AIInsightSection{
			Text: tc.text, FactIDs: []string{"sleep-short"}, Stance: "qualify",
		}})
		if err == nil {
			t.Fatalf("user-visible editing or gender artifact passed: %q", tc.text)
		}
	}
	for _, tc := range []struct{ locale, text string }{
		{"ru", "Сегодня выбирай комфортный темп и при необходимости сделай паузу."},
		{"ru", "Ты можешь выбрать комфортный темп и сделать паузу."},
		{"ru", "Ты видишь сигнал."},
		{"ru", "Ты можешь выбрать интервал."},
		{"sr", "Danas biraj prijatan tempo i napravi pauzu ako ti prija."},
		{"sr", "Danas si u umerenoj zoni; biraj prijatan tempo."},
		{"sr", "Podaci ukazuju na mirniji tempo, ali izbor ostaje otvoren."},
		{"sr", "Ne bih da menjaš plan samo zbog jednog signala."},
	} {
		input, _ := BuildAIInsightInput(aiInsightTestSnapshot(), tc.locale, "overall", nil)
		_, err := ValidateAIInsightSlot(input, AIInsightSlotResponse{Version: AIInsightVersion, Locale: tc.locale, Slot: "overall", Insight: &AIInsightSection{
			Text: tc.text, FactIDs: []string{"sleep-short"}, Stance: "qualify",
		}})
		if err != nil {
			t.Fatalf("natural gender-neutral prose rejected: %q: %v", tc.text, err)
		}
	}
	srInput, _ := BuildAIInsightInput(aiInsightTestSnapshot(), "sr", "overall", nil)
	for _, text := range []string{
		"Primetio sam razliku u oporavku.",
		"Ja sam zaključila da je potreban odmor.",
		"Zato bih danas izbegla veći napor.",
		"Zato bih dan držao laganim.",
		"Ne bih menjao plan samo zbog jednog signala.",
		"Zato bih preporuku za lakši tempo shvatio kao okvir, ne pravilo.",
		"Zato bih preporuku za normalan trening ublažio.",
		"Zato bih zadržao mirniji tempo.",
		"Uz energiju nižu od očekivane, pa bih tvrdnju o većem naporu uzela s rezervom.",
		"Energetska procena je označena kao privremena, pa bih tvrdnju da je normalan trening sigurno u redu uzela s rezervom.",
		"Podaci ukazuju da si danas spreman za veći napor.",
		"Podaci ukazuju da si danas spremna za veći napor.",
		"Spreman si za veći napor.",
		"Preporučio bih mirniji tempo.",
	} {
		_, err := ValidateAIInsightSlot(srInput, AIInsightSlotResponse{Version: AIInsightVersion, Locale: "sr", Slot: "overall", Insight: &AIInsightSection{
			Text: text, FactIDs: []string{"sleep-short"}, Stance: "qualify",
		}})
		if err == nil {
			t.Fatalf("gender-marked Serbian AI voice passed: %q", text)
		}
	}
	input, _ := BuildAIInsightInput(aiInsightTestSnapshot(), "ru", "overall", nil)
	for _, text := range []string{
		"Оцени, насколько ты готов сегодня к более высокой нагрузке.",
		"Проверь, насколько ты готова сегодня к более высокой нагрузке.",
		"Готов ли ты сегодня к более высокой нагрузке?",
		"Сегодня ты можешь быть более готовым или готовой к нагрузке.",
	} {
		_, err := ValidateAIInsightSlot(input, AIInsightSlotResponse{Version: AIInsightVersion, Locale: "ru", Slot: "overall", Insight: &AIInsightSection{
			Text: text, FactIDs: []string{"sleep-short"}, Stance: "qualify",
		}})
		if err == nil {
			t.Fatalf("gender-marked Russian reader state passed reader-copy guard: %q", text)
		}
	}
	_, err := ValidateAIInsightSlot(input, AIInsightSlotResponse{Version: AIInsightVersion, Locale: "ru", Slot: "overall", Insight: &AIInsightSection{
		Text: "Выбирай комфортный темп.", AlternativeAction: "Ты вчера спал дольше обычного.", FactIDs: []string{"sleep-short"}, Stance: "qualify",
	}})
	if err == nil {
		t.Fatal("gendered alternative action passed reader-copy guard")
	}
}

func TestApplyAIInsightDoesNotMutateServerSnapshot(t *testing.T) {
	snapshot := aiInsightTestSnapshot()
	insight := &DailyInsightAIInsight{Text: "I read this differently.", Stance: "disagree", FactIDs: []string{"sleep-short"}}
	out, err := ApplyAIInsightSlot(snapshot, "sleep", insight)
	if err != nil || out.Domains[0].AIInsight == nil || snapshot.Domains[0].AIInsight != nil || out.Primary.Observation != snapshot.Primary.Observation {
		t.Fatalf("apply = %#v err=%v", out, err)
	}
	out.Domains[0].AIInsight.FactIDs[0] = "changed"
	if insight.FactIDs[0] != "sleep-short" {
		t.Fatal("insight slices were aliased")
	}
}

func TestAIInsightLocaleChecksDoNotCrossFieldBoundaries(t *testing.T) {
	input, _ := BuildAIInsightInput(aiInsightTestSnapshot(), "ru", "overall", nil)
	// Each field is neutral in its own grammatical context. Joining them
	// invents "ты спал", which does not occur in either field.
	_, err := ValidateAIInsightSlot(input, AIInsightSlotResponse{Version: AIInsightVersion, Locale: "ru", Slot: "overall", Insight: &AIInsightSection{
		Text: "Выбор делаешь ты", AlternativeAction: "Спал интерес к нагрузке? Можно выбрать прогулку.", FactIDs: []string{"sleep-short"}, Stance: "qualify",
	}})
	if err != nil {
		t.Fatalf("grammar crossed the field boundary: %v", err)
	}
}

func TestApplyNilAIInsightKeepsIndependentServerInsight(t *testing.T) {
	snapshot := aiInsightTestSnapshot()
	server := snapshot.Domains[0].Insight
	out, err := ApplyAIInsightSlot(snapshot, "sleep", nil)
	if err != nil || out.Domains[0].AIInsight != nil || out.Domains[0].Insight.Observation != server.Observation || out.Domains[0].Insight.Meaning != server.Meaning || snapshot.Domains[0].AIInsight != nil {
		t.Fatalf("nil AI changed server fallback: out=%#v err=%v", out, err)
	}
}
