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
