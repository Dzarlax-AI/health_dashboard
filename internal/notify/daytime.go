package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"time"

	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

// DaytimeCategoryAvailability explains why unsupported categories stay silent.
func DaytimeCategoryAvailability() map[string]string {
	return map[string]string{"sleep_analysis_update": "enabled_when_recorded_facts_change", "guidance_change": "enabled_when_proven", "multisignal": "no_validated_rule", "source_gap": "no_configured_source_contract"}
}

const daytimeGuidanceRule = "final_sleep_guidance_change"

type daytimeCandidate struct {
	event    storage.ProactiveEventCandidate
	decision *health.DailyDecision
	sleep    health.CompletedNightSleep
	morning  *storage.ArchivedGuidance
}

type archivedGuidanceContext struct {
	Decision *health.DailyDecision     `json:"decision"`
	Sleep    health.MorningReportSleep `json:"sleep"`
	Basis    map[string]string         `json:"guidance_basis"`
}

// evaluateDaytimeGuidance uses an archived rule basis to hold Energy and other
// caps constant. A coincident Energy-only change cannot trigger this rule.
func evaluateDaytimeGuidance(ctx context.Context, db *storage.DB, cfg Config, now time.Time, loadBriefing func() (*health.BriefingResponse, error)) (*daytimeCandidate, error) {
	date := now.In(cfg.location()).Format("2006-01-02")
	morning, err := db.LastDeliveredGuidance(ctx, date, "morning")
	if err != nil || morning == nil {
		return nil, err
	}
	var archived archivedGuidanceContext
	if json.Unmarshal(morning.Context, &archived) != nil || archived.Decision == nil || archived.Basis["rule_version"] != "dashboard-guidance-caps-v1" || archived.Basis["energy_action"] == "" {
		return nil, nil
	}
	briefing, err := loadBriefing()
	if err != nil {
		return nil, err
	}
	if briefing == nil || briefing.Date != date || briefing.DailyDecision == nil || briefing.SleepQuality == nil || briefing.SleepQuality.Confidence != health.SleepQualityConfidenceFinal {
		return nil, nil
	}
	nights, err := db.ListCompletedNightSleep(ctx, date, date)
	if err != nil {
		return nil, err
	}
	if len(nights) != 1 {
		return nil, nil
	}
	night := nights[0]
	if !night.IsDefinitiveClaimEligible() || night.InputHash == "" || night.FinalizedAt == nil || !night.FinalizedAt.After(morning.PreparedAt) || night.FinalizedAt.After(now) || night.InputHash == archived.Sleep.InputHash {
		return nil, nil
	}
	if !sleepChangedGuidance(archived, briefing, cfg.Lang) {
		return nil, nil
	}
	if update, e := db.LastDeliveredProactiveAnalysis(ctx, date, daytimeSleepAnalysisRule); e != nil {
		return nil, e
	} else if update != nil && update.DecisionID == briefing.DailyDecision.ID {
		return nil, nil
	}
	evening, err := db.LastDeliveredGuidance(ctx, date, "evening")
	if err != nil {
		return nil, err
	}
	if evening != nil && evening.PreparedAt.After(morning.PreparedAt) {
		var explained archivedGuidanceContext
		if evening.DecisionID == briefing.DailyDecision.ID || (json.Unmarshal(evening.Context, &explained) == nil && explained.Decision != nil && explained.Decision.Mode == briefing.DailyDecision.Mode) {
			return nil, nil
		}
	}
	return &daytimeCandidate{event: storage.ProactiveEventCandidate{Rule: daytimeGuidanceRule, FactualIdentity: night.InputHash, PreviousConclusion: archived.Decision.Mode, CurrentConclusion: briefing.DailyDecision.Mode, SourceReportID: morning.ReportID}, decision: briefing.DailyDecision, sleep: night, morning: morning}, nil
}

// sleepChangedGuidance attributes the change only when changing sleep quality
// under the original rule inputs alone yields the actual new mode.
func sleepChangedGuidance(archived archivedGuidanceContext, briefing *health.BriefingResponse, lang string) bool {
	if archived.Decision == nil || briefing == nil || briefing.DailyDecision == nil || briefing.SleepQuality == nil || archived.Basis["rule_version"] != "dashboard-guidance-caps-v1" || archived.Basis["energy_action"] == "" {
		return false
	}
	energy := &health.EnergyBank{ActionVerdict: archived.Basis["energy_action"]}
	var readiness *health.ReadinessServingState
	if archived.Basis["readiness_status"] != "" {
		readiness = &health.ReadinessServingState{Status: archived.Basis["readiness_status"], Confidence: archived.Basis["readiness_confidence"]}
	}
	var illness *health.IllnessSuspicion
	if archived.Basis["illness_confidence"] != "" {
		illness = &health.IllnessSuspicion{Confidence: archived.Basis["illness_confidence"]}
	}
	var oldSleep *health.SleepQualityBreakdown
	if archived.Basis["sleep_confidence"] != "" {
		oldSleep = &health.SleepQualityBreakdown{Confidence: archived.Basis["sleep_confidence"]}
	}
	old := health.ResolveDashboardTodayGuidance(energy, readiness, illness, oldSleep, health.GetStrings(lang))
	updated := health.ResolveDashboardTodayGuidance(energy, readiness, illness, briefing.SleepQuality, health.GetStrings(lang))
	return old != nil && updated != nil && old.Action == archived.Decision.Mode && updated.Action != old.Action && updated.Action == briefing.DailyDecision.Mode
}

func daytimeWindow(now time.Time, loc *time.Location, start, end int) bool {
	if loc == nil || start < 0 || start > 23 || end < 0 || end > 23 || start == end {
		return false
	}
	hour := now.In(loc).Hour()
	if start < end {
		return hour >= start && hour < end
	}
	return hour >= start || hour < end
}

// MaybeFireDaytime is the isolated minute lifecycle. It never calls an AI provider.
// Unsupported source-gap and multisignal categories remain explicitly disabled.
func MaybeFireDaytime(ctx context.Context, bot *Bot, db *storage.DB, cfg Config, baseURL string, now time.Time) {
	maybeFireDaytime(ctx, bot, db, cfg, baseURL, now, func() (*health.BriefingResponse, error) { return db.GetHealthBriefing(cfg.Lang) })
}
func maybeFireDaytime(ctx context.Context, bot *Bot, db *storage.DB, cfg Config, baseURL string, now time.Time, loadBriefing func() (*health.BriefingResponse, error)) {
	if !cfg.DaytimeEnabled || !cfg.Enabled() || !daytimeWindow(now, cfg.location(), cfg.DaytimeStartHour, cfg.DaytimeEndHour) {
		return
	}
	if maybeFireSleepAnalysisUpdate(ctx, bot, db, cfg, baseURL, now, loadBriefing) {
		return
	}
	first, err := evaluateDaytimeGuidance(ctx, db, cfg, now, loadBriefing)
	if err != nil {
		return
	}
	if first == nil {
		// Missing inputs are not proof that an episode resolved. Only a
		// current decision returning to a confirmed morning conclusion is.
		active, e := db.ActiveProactiveEpisode(ctx, daytimeGuidanceRule)
		if e != nil || active == nil {
			return
		}
		date := now.In(cfg.location()).Format("2006-01-02")
		morning, e := db.LastDeliveredGuidance(ctx, date, "morning")
		if e == nil && morning != nil {
			var baseline archivedGuidanceContext
			if json.Unmarshal(morning.Context, &baseline) == nil && baseline.Decision != nil {
				current, e := loadBriefing()
				if e == nil && current != nil && current.Date == date && current.DailyDecision != nil && current.DailyDecision.Mode == baseline.Decision.Mode {
					_, _ = db.ResolveProactiveEvent(ctx, daytimeGuidanceRule, *active, now)
				}
			}
		}
		return
	}
	// Re-read immediately before reservation; vanished or superseded facts do not queue.
	current, err := evaluateDaytimeGuidance(ctx, db, cfg, now, loadBriefing)
	if err != nil {
		return
	}
	if current == nil || current.event != first.event || current.decision.ID != first.decision.ID {
		return
	}
	active, err := db.ActiveProactiveEpisode(ctx, daytimeGuidanceRule)
	if err != nil {
		return
	}
	if active != nil {
		current.event.ExpectedEpisodeID = active.ID
		current.event.ExpectedRevision = active.Revision
		if active.Conclusion != current.event.CurrentConclusion {
			current.event.PreviousConclusion = active.Conclusion
		}
	}
	msg := formatDaytimeGuidance(current, cfg, baseURL, now)
	snapshot, _ := json.Marshal(map[string]any{"date": now.In(cfg.location()).Format("2006-01-02"), "sleep_date": current.sleep.WakeDate, "sleep_finalized_at": current.sleep.FinalizedAt, "decision": current.decision})
	h := storage.ReportHistory{Kind: "proactive", LocalDate: now.In(cfg.location()).Format("2006-01-02"), Timezone: cfg.location().String(), Language: cfg.Lang, PlainBody: msg, DecisionID: current.decision.ID, Context: snapshot, FallbackReason: "deterministic_daytime"}
	token, reserved, err := db.ReserveProactive(ctx, &h, "proactive:daytime:pending", &current.event, now, cfg.location())
	if err != nil || !reserved {
		return
	}
	_, _ = sendReservedArchivedReport(reportDeliveryDurable, db, bot, cfg, *h.DeliveryKey, &h, token)
}

func formatDaytimeGuidance(c *daytimeCandidate, cfg Config, baseURL string, now time.Time) string {
	before := health.BuildVerdictLabel(c.event.PreviousConclusion, health.GetStrings(cfg.Lang))
	after := c.decision.Label
	if before == "" {
		before = c.event.PreviousConclusion
	}
	if after == "" {
		after = c.event.CurrentConclusion
	}
	title, fact, action, at, link := "Your recommendation changed", "A finalized sleep record changed the guidance.", "Review your planned activity in the app.", "Data at", "Open Health"
	switch cfg.Lang {
	case "ru":
		title, fact, action, at, link = "Рекомендация изменилась", "Завершённая запись сна изменила рекомендацию.", "Проверьте запланированную нагрузку в приложении.", "Данные на", "Открыть Health"
	case "sr":
		title, fact, action, at, link = "Preporuka je promenjena", "Završeni zapis spavanja promenio je preporuku.", "Proverite planiranu aktivnost u aplikaciji.", "Podaci u", "Otvori Health"
	}
	factTime := ""
	if c.sleep.FinalizedAt != nil {
		factTime = c.sleep.FinalizedAt.In(cfg.location()).Format("2006-01-02 15:04")
	}
	msg := fmt.Sprintf("<b>%s</b>\n%s → %s\n%s\n%s\n%s %s", title, html.EscapeString(before), html.EscapeString(after), fact, action, at, factTime)
	if baseURL != "" {
		msg += fmt.Sprintf("\n<a href=\"%s\">%s</a>", html.EscapeString(baseURL), link)
	}
	return msg
}
