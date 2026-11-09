package health

import (
	"strings"
	"testing"
)

func TestRecordedSleepAnalysisUsesNightFactsInsteadOfWeeklyStages(t *testing.T) {
	hours := 6.85
	b := &BriefingResponse{Date: "2026-11-09", Sleep: &SleepAnalysis{LatestDate: "2026-11-09", LatestTotal: &hours, Nights: 7, TotalAvg: 7.59, DeepAvg: 9, REMAvg: 9, Sources: []SleepSourceSummary{{Source: "watch", Total: hours, Deep: 0.82, REM: 1.78, Core: 4.25, Awake: 0.12}}}}
	e := RecordedSleepAggregate(b, b.Date, "watch", hours)
	if e == nil || e.DeepMinutes == nil || *e.DeepMinutes != 49 || e.REMMinutes == nil || *e.REMMinutes != 107 || e.AwakeMinutes == nil || *e.AwakeMinutes != 7 || e.AwakeBouts != nil || e.Start != nil {
		t.Fatalf("current-night facts missing or invented: %+v", e)
	}
	sleep := MorningReportSleep{ReportDate: b.Date, Date: b.Date, Hours: &hours, Capture: NightCaptureComplete, Assessment: NightDurationPlausible, Finalization: NightFinalProvisional, Episode: e}
	AttachRecentRecordedSleepComparison(&sleep, b)
	if sleep.RecentRecordedDifferenceMinutes == nil || *sleep.RecentRecordedDifferenceMinutes != -44 {
		t.Fatalf("comparison missing: %+v", sleep)
	}
	text := RecordedSleepAnalysisText(sleep, "ru")
	if !strings.Contains(text, "44 мин") || !strings.Contains(text, "7 мин") || strings.Contains(text, "нельзя оценить") {
		t.Fatalf("analysis replaced by generic caveat: %s", text)
	}
	facts := RecordedSleepNarrativeFacts(sleep, "en")
	if len(facts) != 3 || !strings.Contains(facts[1].Statement, "49") || !strings.Contains(facts[2].Statement, "including this one") {
		t.Fatalf("incomplete analysis evidence: %+v", facts)
	}
	if RecordedSleepAggregate(b, b.Date, "other", hours) != nil || RecordedSleepAggregate(b, "2026-11-08", "watch", hours) != nil || RecordedSleepAggregate(b, b.Date, "watch", 8) != nil {
		t.Fatal("another date, source or duration borrowed current-night architecture")
	}
	b.Sleep.Sources[0].Deep = 2
	if RecordedSleepAggregate(b, b.Date, "watch", hours) != nil {
		t.Fatal("inconsistent stage total admitted")
	}
}

func TestSleepAnalysisRevisionIgnoresFinalityAndSyncNoise(t *testing.T) {
	hours := 7.0
	sleep := MorningReportSleep{ReportDate: "2026-11-09", Date: "2026-11-09", Hours: &hours, Capture: NightCaptureComplete, Assessment: NightDurationPlausible, Finalization: NightFinalProvisional, InputHash: "first"}
	identity := SleepAnalysisIdentity(sleep)
	sleep.Finalization, sleep.InputHash = NightFinalFinal, "second-identical-replay"
	if identity == "" || SleepAnalysisIdentity(sleep) != identity {
		t.Fatal("technical finalization or replay changed analysis revision")
	}
	hours += 1.0 / 60
	if SleepAnalysisIdentity(sleep) != identity {
		t.Fatal("one-minute noise caused a new analysis")
	}
	hours += 15.0 / 60
	if SleepAnalysisIdentity(sleep) == identity {
		t.Fatal("meaningful recorded duration change suppressed")
	}
	sleep.Hours = new(float64)
	*sleep.Hours = 7
	awake := 20
	sleep.Episode = &MorningSleepEpisode{AwakeMinutes: &awake}
	if SleepAnalysisIdentity(sleep) == identity {
		t.Fatal("new continuity evidence suppressed")
	}
}
