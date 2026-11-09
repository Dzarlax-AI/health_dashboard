package health

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnergyInsightIncludesRecoveryBasisAndFinalGuidance(t *testing.T) {
	s := aiInsightTestSnapshot()
	s.Domains = append(s.Domains, DailyInsightDomain{Key: "energy", DataState: "fresh", Confidence: "final", Insight: DailyInsight{State: "insight", Observation: "Current reserve is available."}})
	s.Domains[1].DataState, s.Domains[1].Confidence = "fresh", "final"
	s.NarrativeFacts = append(s.NarrativeFacts,
		DailyInsightNarrativeFact{ID: "readiness_hrv_current", Domain: "recovery", Fresh: true, Statement: "HRV is 49.2 ms.", DisplayValues: []string{"49.2"}},
		DailyInsightNarrativeFact{ID: "energy_authoritative_state", Domain: "energy", Fresh: true, Statement: "Reserve is 55.", DisplayValues: []string{"55"}})
	input, ok := BuildAIInsightInput(s, "en", "energy", nil)
	if !ok {
		t.Fatal("energy input unavailable")
	}
	found := false
	for _, fact := range input.Facts {
		found = found || fact.ID == "readiness_hrv_current"
	}
	if !found {
		t.Fatal("Energy cannot assess an HRV-based verdict without the available HRV fact")
	}
	s.Domains[1].Confidence = "provisional"
	provisional, eligible := BuildAIInsightInput(s, "en", "energy", nil)
	confirmedComponent := false
	for _, fact := range provisional.Facts {
		confirmedComponent = confirmedComponent || fact.ID == "readiness_hrv_current"
	}
	if !eligible || !confirmedComponent {
		t.Fatal("provisional overall Recovery hid independently confirmed HRV from Energy")
	}
	s.Domains[1].Confidence = "final"
	before := AIInsightInputHash(input)
	s.Primary.NextStep.Text = "A new final action"
	after, _ := BuildAIInsightInput(s, "en", "energy", nil)
	if AIInsightInputHash(after) == before {
		t.Fatal("final guidance change did not invalidate Energy context")
	}
}

func TestEnergyContextKeepsConfirmedComponentWhenAggregateCoverageIsLow(t *testing.T) {
	const date = "2026-11-09"
	hrv := 49.2
	b := &BriefingResponse{Date: date, ReadinessConfidence: ReadinessConfidenceLow, ReadinessServing: &ReadinessServingState{Status: ReadinessServingLowCoverage, Confidence: ReadinessConfidenceLow}, EnergyBank: &EnergyBank{Current: 55, Capacity: 90, ActionVerdict: "rest", VerdictReason: "Use a quieter plan."}, RawMetrics: &RawMetrics{LastDate: date, ReadinessEvidence: &ReadinessEvidenceInput{Date: date, HRV: ReadinessComponentEvidence{Metric: "heart_rate_variability", Value: &hrv, Present: true, EvaluatedDate: date, SourceDate: date, Freshness: ReadinessFreshnessOK, SampleCount: MinSleepWindowHRVSamplesForFullConfidence, Confidence: ReadinessConfidenceFinal}}}}
	input, eligible := BuildAIInsightInput(BuildDailyInsightSnapshot(b, "en"), "en", "energy", nil)
	found := false
	for _, fact := range input.Facts {
		found = found || fact.ID == "readiness_hrv_current"
	}
	if !eligible || !found {
		t.Fatalf("confirmed component lost behind aggregate coverage: %+v", input)
	}
	b.RawMetrics.ReadinessEvidence.HRV.Confidence = ReadinessConfidenceLow
	input, _ = BuildAIInsightInput(BuildDailyInsightSnapshot(b, "en"), "en", "energy", nil)
	for _, fact := range input.Facts {
		if fact.ID == "readiness_hrv_current" {
			t.Fatal("insufficient component coverage became eligible")
		}
	}
}

func TestRecoveryInsightDescribesDomainWithoutCompetingAction(t *testing.T) {
	b := &BriefingResponse{Date: "2026-11-09", ReadinessToday: 71, ReadinessTodayBand: "fair", ReadinessTodayLabel: "Fair", ReadinessTip: "Moderate activity is a good choice.", ReadinessConfidence: "final", ReadinessServing: &ReadinessServingState{Status: ReadinessServingFresh, Confidence: ReadinessConfidenceFinal}}
	d := buildRecoveryInsightDomain(b, nil, dailyInsightCopy("en"))
	if strings.Contains(d.Insight.Observation, "activity is a good choice") {
		t.Fatal("domain assessment repeated an independent training recommendation")
	}
}

func TestEnergyInsightUsesFinalGuidanceWhenRawVerdictIsCapped(t *testing.T) {
	b := &BriefingResponse{Date: "2026-11-09", EnergyBank: &EnergyBank{Current: 80, Capacity: 90, ActionVerdict: "push_hard", VerdictReason: "Green light for hard training."}, TodayGuidance: &DashboardTodayGuidance{Action: "moderate", Reason: "Sleep coverage does not support higher intensity.", Label: "Moderate"}}
	d := buildEnergyInsightDomain(b, nil, dailyInsightCopy("en"))
	if strings.Contains(d.Insight.Observation, "Green light") {
		t.Fatal("Energy presented the uncapped recommendation alongside a more cautious final decision")
	}
	if d.NarrativeSubject == "push_hard" {
		t.Fatal("Energy's permitted proposition bypassed final guidance")
	}
}

func TestUnconfirmedSleepCannotEnterGuidanceOrOtherProseChannels(t *testing.T) {
	s := aiInsightTestSnapshot()
	s.Domains[0].DataState, s.Domains[0].Confidence = "partial", "partial"
	s.Domains[0].Insight = DailyInsight{State: "insufficient_data", AnswerKind: DailyInsightAnswerDataGuidance, GapReason: "sleep_partial", Observation: "Last night was 1 hour versus 6 hours baseline."}
	s.Primary = DailyInsight{State: "insight", AnswerKind: DailyInsightAnswerFactual, Observation: "Last night was 1 hour versus 6 hours baseline.", Meaning: "Last night was 1 hour versus 6 hours baseline.", NextStep: &DailyInsightAction{ID: "daily-decision-moderate", Text: "Last night was 1 hour versus 6 hours baseline."}}
	s.Domains = append(s.Domains, DailyInsightDomain{Key: "energy", DataState: "fresh", Confidence: "final", Insight: DailyInsight{State: "insight", Observation: s.Primary.Observation, Meaning: s.Primary.Meaning, NextStep: s.Primary.NextStep}})
	s.NarrativeFacts = []DailyInsightNarrativeFact{
		{ID: "sleep_current_recorded_duration", Domain: "sleep", Fresh: true, Meaning: partialSleepCurrentDurationMeaning, Window: partialSleepCurrentDurationWindow, Statement: "Currently recorded sleep duration is 1.0 h; completeness and quality are unconfirmed.", EvidenceIDs: []string{"sleep_current_recorded_duration"}},
		{ID: "energy_authoritative_state", Domain: "energy", Fresh: true, Statement: "Energy reserve is 52 of 93.", EvidenceIDs: []string{"energy"}},
		{ID: "mixed_headline", Domain: "energy", Fresh: true, Statement: "Last night was 1 hour versus 6 hours sleep baseline.", EvidenceIDs: []string{"sleep-evidence"}},
	}
	for _, slot := range []string{"sleep", "energy", "overall"} {
		input, ok := BuildAIInsightInput(s, "en", slot, []AIInsightSibling{{Slot: "energy", State: "ready", Text: s.Primary.Observation, AlternativeAction: s.Primary.Observation}})
		if !ok {
			t.Fatalf("%s input missing", slot)
		}
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "Last night was 1 hour versus 6 hours") {
			t.Fatalf("%s leaked unsupported prose: %s", slot, raw)
		}
		if input.DailyGuidance.ActionMode != "moderate" || input.DailyGuidance.State != "insight" {
			t.Fatalf("%s lost structured final decision: %#v", slot, input.DailyGuidance)
		}
	}
	context := BuildInsightFactContext(s)
	raw, _ := json.Marshal(context)
	if strings.Contains(string(raw), "Last night was 1 hour versus 6 hours") {
		t.Fatalf("context leaked unsupported prose: %s", raw)
	}
	if context.Guidance.ActionMode != "moderate" {
		t.Fatalf("lost final decision mode: %#v", context.Guidance)
	}
	s.Domains[0].DataState, s.Domains[0].Confidence = "fresh", "final"
	restored := BuildInsightFactContext(s)
	if restored.Guidance.Observation != s.Primary.Observation || restored.Guidance.Action != s.Primary.NextStep.Text {
		t.Fatalf("final context changed: %#v", restored.Guidance)
	}
}
