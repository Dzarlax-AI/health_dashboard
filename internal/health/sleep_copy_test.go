package health

import (
	"strings"
	"testing"
)

func TestSleepCopyUsesReadableTimeAndExactMinuteDifference(t *testing.T) {
	latest, average := 7.0+22.0/60, 8.0+6.0/60
	copy := dailyInsightCopy("ru")
	if got := localizedSleepDuration(copy, latest); got != "Сон за ночь — 7 ч 22 мин." {
		t.Fatalf("duration still uses a decimal hour: %q", got)
	}
	if got := localizedSleepComparison(copy, latest, average); got != "На 44 мин короче среднего." {
		t.Fatalf("comparison still uses a decimal hour: %q", got)
	}
	for _, locale := range []string{"en", "ru", "sr"} {
		fact := RecordedSleepEpisodeFact(locale, latest)
		statement := localizedNarrativeSleepFact(locale, latest, average)
		for _, text := range []string{fact.Statement, statement} {
			if strings.Contains(text, "7.4") || !strings.Contains(text, "22") {
				t.Fatalf("%s input has no exact readable duration: %q", locale, text)
			}
		}
		if !containsDailyInsightID(fact.DisplayValues, "22") {
			t.Fatalf("%s readable minutes would be rejected by number validation", locale)
		}
	}
}

func TestReadableSleepTimeRoundsAcrossHourBoundary(t *testing.T) {
	for _, tc := range []struct {
		hours float64
		want  string
	}{
		{0, "0 мин"}, {0.75, "45 мин"}, {1, "1 ч"}, {1.5, "1 ч 30 мин"}, {1.999, "2 ч"},
	} {
		if got := readableSleepDuration(tc.hours, "ru"); got != tc.want {
			t.Fatalf("%v: %q, want %q", tc.hours, got, tc.want)
		}
	}
}

func TestHumanSleepNumbersRemainGroundedInFinalInput(t *testing.T) {
	latest, average, quality := 7.0+22.0/60, 8.0+6.0/60, 85
	snapshot := BuildDailyInsightSnapshot(&BriefingResponse{Date: "2026-10-10",
		Sleep:        &SleepAnalysis{LatestDate: "2026-10-10", LatestTotal: &latest, TotalAvg: average},
		SleepQuality: &SleepQualityBreakdown{ScorePct: &quality, Confidence: SleepQualityConfidenceFinal}}, "ru")
	input, eligible := BuildAIInsightInput(snapshot, "ru", "sleep", nil)
	if !eligible {
		t.Fatal("complete final sleep is ineligible")
	}
	candidate := AIInsightSlotResponse{Version: AIInsightVersion, Locale: "ru", Slot: "sleep",
		Insight: &AIInsightSection{Text: "За ночь — 7 ч 22 мин сна, на 44 мин короче среднего.", Stance: "agree", FactIDs: []string{"sleep_canonical_comparison"}}}
	if _, err := ValidateAIInsightSlot(input, candidate); err != nil {
		t.Fatalf("supplied hours/minutes rejected: %v", err)
	}
}
