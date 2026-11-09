package storage

import (
	"context"
	"math"

	"health-receiver/internal/health"
)

// Canonical finality bounds narrative claims, independently of the legacy
// physiology score's confidence. This does not recompute scores or actions.
func briefingForInsightSleep(b *health.BriefingResponse, sleep health.MorningReportSleep) *health.BriefingResponse {
	if b == nil || b.Sleep == nil {
		return b
	}
	if sleep.HasUsableFinalNight(b.Date) && b.Sleep.LatestDate == b.Date && b.Sleep.LatestTotal != nil && math.Abs(*b.Sleep.LatestTotal-*sleep.Hours) < 0.000001 {
		return b
	}
	copy := *b
	quality := health.SleepQualityBreakdown{Confidence: health.SleepQualityConfidencePartial}
	copy.SleepQuality = &quality
	return &copy
}

func (s *DB) insightSleepBriefing(ctx context.Context, b *health.BriefingResponse) (*health.BriefingResponse, health.MorningReportSleep) {
	if b == nil {
		return nil, health.MorningReportSleep{}
	}
	nights, err := s.ListCompletedNightSleep(ctx, b.Date, b.Date)
	if err != nil {
		return briefingForInsightSleep(b, health.MorningReportSleep{}), health.MorningReportSleep{}
	}
	sleep := health.SelectMorningReportSleep(b.Date, nights)
	for _, night := range nights {
		if night.WakeDate == b.Date && health.MorningSleepAnalysisEligible(&sleep, b.Date) {
			sleep.Episode, sleep.AnalysisStatus, _ = s.recordedMorningSleepEpisode(ctx, night)
			if sleep.Episode == nil {
				sleep.Episode = health.RecordedSleepAggregate(b, b.Date, night.Source, night.DurationHours)
			}
		}
	}
	health.AttachRecentRecordedSleepComparison(&sleep, b)
	return briefingForInsightSleep(b, sleep), sleep
}

// A complete recorded episode is fresh factual data even while the future
// calendar window remains open. Keep its confidence provisional for claims.
func applyRecordedSleepPresentation(snapshot *health.DailyInsightSnapshot, sleep health.MorningReportSleep, lang string) {
	if snapshot == nil || sleep.Date != snapshot.Date || sleep.Capture != health.NightCaptureComplete || sleep.Assessment != health.NightDurationPlausible || sleep.Hours == nil {
		return
	}
	if sleep.Finalization == health.NightFinalFinal {
		sleep.ReportDate = snapshot.Date
		for _, fact := range health.RecordedSleepNarrativeFacts(sleep, lang) {
			if fact.ID == "sleep_recorded_architecture" {
				snapshot.NarrativeFacts = append(snapshot.NarrativeFacts, fact)
			}
		}
		return
	}
	if sleep.Finalization != health.NightFinalProvisional {
		return
	}
	facts := make([]health.DailyInsightNarrativeFact, 0, len(snapshot.NarrativeFacts)+1)
	sleep.ReportDate = snapshot.Date
	for _, fact := range snapshot.NarrativeFacts {
		if fact.Domain != "sleep" {
			facts = append(facts, fact)
		}
	}
	currentFacts := health.RecordedSleepNarrativeFacts(sleep, lang)
	snapshot.NarrativeFacts = append(facts, currentFacts...)
	for i := range snapshot.Domains {
		d := &snapshot.Domains[i]
		if d.Key != "sleep" {
			continue
		}
		d.DataState, d.Confidence = "fresh", "provisional"
		d.Insight.State, d.Insight.AnswerKind = "insight", health.DailyInsightAnswerFactual
		d.Insight.GapReason, d.Insight.Remediation = "sleep_recorded_episode", ""
		d.Insight.Meaning = ""
		d.Insight.Observation = health.RecordedSleepAnalysisText(sleep, lang)
		if d.Insight.Observation == "" {
			d.Insight.Observation = health.RecordedSleepEpisodeFact(lang, *sleep.Hours).Statement
		}
		d.Insight.ClaimID = ""
		d.Insight.NextStep = nil
	}
}
