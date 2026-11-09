package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"math"
	"time"

	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

const daytimeSleepAnalysisRule = "sleep_analysis_update"

type sleepAnalysisSnapshot struct {
	Identity string                    `json:"identity"`
	Text     string                    `json:"text"`
	Source   string                    `json:"source"`
	Sleep    health.MorningReportSleep `json:"sleep"`
}

func newSleepAnalysisSnapshot(sleep health.MorningReportSleep, text string) sleepAnalysisSnapshot {
	return sleepAnalysisSnapshot{Identity: health.SleepAnalysisIdentity(sleep), Text: text, Sleep: sleep, Source: "server"}
}

type sleepAnalysisUpdate struct {
	event              storage.ProactiveEventCandidate
	previous, current  sleepAnalysisSnapshot
	source, supersedes *storage.ArchivedGuidance
	briefing           *health.BriefingResponse
}

func evaluateSleepAnalysisUpdate(ctx context.Context, db *storage.DB, cfg Config, now time.Time, loadBriefing func() (*health.BriefingResponse, error)) (*sleepAnalysisUpdate, error) {
	date := now.In(cfg.location()).Format("2006-01-02")
	morning, err := db.LastDeliveredGuidance(ctx, date, "morning")
	if err != nil || morning == nil {
		return nil, err
	}
	previousReport := morning
	if later, e := db.LastDeliveredProactiveAnalysis(ctx, date, daytimeSleepAnalysisRule); e != nil {
		return nil, e
	} else if later != nil && later.PreparedAt.After(morning.PreparedAt) {
		previousReport = later
	}
	var previous struct {
		Analysis sleepAnalysisSnapshot     `json:"sleep_analysis"`
		Sleep    health.MorningReportSleep `json:"sleep"`
	}
	if json.Unmarshal(previousReport.Context, &previous) != nil {
		return nil, nil
	}
	if previous.Analysis.Identity == "" {
		previous.Analysis = newSleepAnalysisSnapshot(previous.Sleep, "")
	}
	b, err := loadBriefing()
	if err != nil || b == nil || b.Date != date {
		return nil, err
	}
	sleep, _, err := db.MorningReportEvidence(ctx, storage.AIConfig{}, cfg.Lang, date, b)
	if err != nil {
		return nil, err
	}
	if b.Sleep == nil || b.Sleep.LatestDate != date || b.Sleep.LatestTotal == nil || sleep.Hours == nil || math.Abs(*b.Sleep.LatestTotal-*sleep.Hours)*60 > 1 {
		return nil, nil
	}
	current := newSleepAnalysisSnapshot(sleep, health.RecordedSleepAnalysisText(sleep, cfg.Lang))
	if current.Identity == "" || current.Identity == previous.Analysis.Identity {
		return nil, nil
	}
	// A matching current Sleep opinion may improve the explanation. Waiting
	// for it does not delay a useful factual analysis of the changed record.
	if snapshot, e := db.BuildTodayInsightSnapshot(ctx, cfg.Lang, now); e == nil && snapshot != nil && matchingRecordedSleepFacts(snapshot, sleep, cfg.Lang) {
		for _, d := range snapshot.Domains {
			if d.Key == "sleep" && d.AIInsight != nil && !d.AIInsight.Stale && d.AIInsight.Text != "" {
				current.Text = d.AIInsight.Text
				current.Source = "model"
			}
		}
	}
	if current.Text == "" {
		return nil, nil
	}
	return &sleepAnalysisUpdate{event: storage.ProactiveEventCandidate{Rule: daytimeSleepAnalysisRule, FactualIdentity: current.Identity, PreviousConclusion: previous.Analysis.Identity, CurrentConclusion: current.Identity, SourceReportID: morning.ReportID}, previous: previous.Analysis, current: current, source: morning, supersedes: previousReport, briefing: b}, nil
}

func matchingRecordedSleepFacts(snapshot *health.DailyInsightSnapshot, sleep health.MorningReportSleep, lang string) bool {
	if snapshot.Date != sleep.Date {
		return false
	}
	for _, wanted := range health.RecordedSleepNarrativeFacts(sleep, lang) {
		if wanted.ID != "sleep_recorded_architecture" {
			continue
		}
		for _, actual := range snapshot.NarrativeFacts {
			if actual.ID == wanted.ID && actual.Statement == wanted.Statement {
				return true
			}
		}
		return false
	}
	return false
}

func maybeFireSleepAnalysisUpdate(ctx context.Context, bot *Bot, db *storage.DB, cfg Config, baseURL string, now time.Time, loadBriefing func() (*health.BriefingResponse, error)) bool {
	first, err := evaluateSleepAnalysisUpdate(ctx, db, cfg, now, loadBriefing)
	if err != nil || first == nil {
		return false
	}
	current, err := evaluateSleepAnalysisUpdate(ctx, db, cfg, now, loadBriefing)
	if err != nil || current == nil || current.current.Identity != first.current.Identity || current.supersedes.ReportID != first.supersedes.ReportID {
		return true
	}
	active, err := db.ActiveProactiveEpisode(ctx, daytimeSleepAnalysisRule)
	if err != nil {
		return true
	}
	if active != nil {
		current.event.ExpectedEpisodeID = active.ID
		current.event.ExpectedRevision = active.Revision
		current.event.PreviousConclusion = active.Conclusion
	}
	msg := formatSleepAnalysisUpdate(current, cfg.Lang)
	date := now.In(cfg.location()).Format("2006-01-02")
	contextJSON, _ := json.Marshal(map[string]any{"date": date, "sleep": current.current.Sleep, "sleep_analysis": current.current, "supersedes_report_id": current.supersedes.ReportID, "source_report_id": current.source.ReportID, "decision": current.briefing.DailyDecision})
	decisionID := ""
	if current.briefing.DailyDecision != nil {
		decisionID = current.briefing.DailyDecision.ID
	}
	aiMetadata, _ := json.Marshal(map[string]any{"sleep_analysis_source": current.current.Source, "sleep_analysis_identity": current.current.Identity})
	h := storage.ReportHistory{Kind: "proactive", LocalDate: date, Timezone: cfg.location().String(), Language: cfg.Lang, PlainBody: msg, DecisionID: decisionID, Context: contextJSON, AIMetadata: aiMetadata, FallbackReason: "sleep_analysis_revision"}
	token, reserved, err := db.ReserveProactive(ctx, &h, "proactive:sleep-analysis:pending", &current.event, now, cfg.location())
	if err == nil && reserved {
		_, _ = sendReservedArchivedReport(reportDeliveryDurable, db, bot, cfg, *h.DeliveryKey, &h, token)
	}
	return true
}

func formatSleepAnalysisUpdate(c *sleepAnalysisUpdate, lang string) string {
	copy := map[string][3]string{"ru": {"Обновлённый анализ сна", "Пришли новые данные за ночь %s.", "Предыдущий анализ сохранён в истории."}, "sr": {"Ažurirana analiza sna", "Stigli su novi podaci za noć %s.", "Prethodna analiza je sačuvana u istoriji."}, "en": {"Updated sleep analysis", "New data arrived for the night of %s.", "The previous analysis is preserved in history."}}
	words, ok := copy[lang]
	if !ok {
		words = copy["en"]
	}
	change := ""
	if c.previous.Sleep.Hours != nil && c.current.Sleep.Hours != nil && *c.previous.Sleep.Hours != *c.current.Sleep.Hours {
		change = fmt.Sprintf("\n%s → %s", formatSleepDuration(*c.previous.Sleep.Hours, lang), formatSleepDuration(*c.current.Sleep.Hours, lang))
	}
	if change == "" && c.current.Sleep.Episode != nil {
		before := c.previous.Sleep.Episode
		labels := map[string][]string{"ru": {"Глубокий сон", "REM", "Бодрствование"}, "sr": {"Duboki san", "REM", "Budnost"}, "en": {"Deep sleep", "REM", "Wakefulness"}}[lang]
		if labels == nil {
			labels = []string{"Deep sleep", "REM", "Wakefulness"}
		}
		old := []*int{nil, nil, nil}
		if before != nil {
			old = []*int{before.DeepMinutes, before.REMMinutes, before.AwakeMinutes}
		}
		for i, value := range []*int{c.current.Sleep.Episode.DeepMinutes, c.current.Sleep.Episode.REMMinutes, c.current.Sleep.Episode.AwakeMinutes} {
			if value != nil && (old[i] == nil || *old[i] != *value) {
				if old[i] == nil {
					change = fmt.Sprintf("\n%s: %d min", labels[i], *value)
				} else {
					change = fmt.Sprintf("\n%s: %d → %d min", labels[i], *old[i], *value)
				}
				break
			}
		}
	}
	decision := ""
	if c.briefing != nil && c.briefing.DailyDecision != nil && c.source != nil {
		var old archivedGuidanceContext
		if json.Unmarshal(c.source.Context, &old) == nil && old.Decision != nil && old.Decision.Mode != c.briefing.DailyDecision.Mode {
			decision = "\n\n" + html.EscapeString(c.briefing.DailyDecision.Label) + ": " + html.EscapeString(c.briefing.DailyDecision.Reason)
		}
	}
	return "<b>😴 " + html.EscapeString(words[0]) + "</b>\n" + fmt.Sprintf(words[1], html.EscapeString(c.current.Sleep.Date)) + change + "\n\n" + html.EscapeString(c.current.Text) + decision + "\n\n" + words[2]
}
