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

func TestBuildDailyInsightSnapshotDoesNotInventSleepInterpretationWithoutContext(t *testing.T) {
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
		if domain.Insight.Observation != "" {
			t.Fatalf("observation = %q, want empty without baseline or quality", domain.Insight.Observation)
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

func TestBuildDailyInsightSnapshotWithholdsRecoveryGuidanceForInsufficientServingStates(t *testing.T) {
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
			if domain.Insight.Observation != "" {
				t.Fatalf("observation = %q, want empty", domain.Insight.Observation)
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
