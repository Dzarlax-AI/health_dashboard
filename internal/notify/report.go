package notify

import (
	"fmt"
	"strings"
	"time"

	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

// Config holds Telegram credentials and per-weekday schedule.
type Config struct {
	Token  string
	ChatID string
	Lang   string

	// Hour (0–23) at which to send the morning sleep report.
	MorningWeekdayHour int
	MorningWeekendHour int

	// Hour (0–23) at which to send the evening day summary.
	EveningWeekdayHour int
	EveningWeekendHour int
}

// Enabled returns true when Telegram credentials are configured.
func (c Config) Enabled() bool {
	return c.Token != "" && c.ChatID != ""
}

func (c Config) morningHour(wd time.Weekday) int {
	if wd == time.Saturday || wd == time.Sunday {
		return c.MorningWeekendHour
	}
	return c.MorningWeekdayHour
}

func (c Config) eveningHour(wd time.Weekday) int {
	if wd == time.Saturday || wd == time.Sunday {
		return c.EveningWeekendHour
	}
	return c.EveningWeekdayHour
}

// NextMorning returns the next time the morning report should fire.
func (c Config) NextMorning(from time.Time) time.Time {
	h := c.morningHour(from.Weekday())
	t := time.Date(from.Year(), from.Month(), from.Day(), h, 0, 0, 0, from.Location())
	if !t.After(from) {
		t = t.Add(24 * time.Hour)
		t = time.Date(t.Year(), t.Month(), t.Day(), c.morningHour(t.Weekday()), 0, 0, 0, t.Location())
	}
	return t
}

// NextEvening returns the next time the evening report should fire.
func (c Config) NextEvening(from time.Time) time.Time {
	h := c.eveningHour(from.Weekday())
	t := time.Date(from.Year(), from.Month(), from.Day(), h, 0, 0, 0, from.Location())
	if !t.After(from) {
		t = t.Add(24 * time.Hour)
		t = time.Date(t.Year(), t.Month(), t.Day(), c.eveningHour(t.Weekday()), 0, 0, 0, t.Location())
	}
	return t
}

// SendMorning sends the sleep report for the most recent night.
func SendMorning(bot *Bot, db *storage.DB, lang string) error {
	briefing, err := db.GetHealthBriefing(lang)
	if err != nil {
		return err
	}
	return bot.Send(formatMorning(briefing, lang))
}

// SendEvening sends the daily activity summary.
func SendEvening(bot *Bot, db *storage.DB, lang string) error {
	briefing, err := db.GetHealthBriefing(lang)
	if err != nil {
		return err
	}
	dash, err := db.GetDashboard()
	if err != nil {
		return err
	}
	return bot.Send(formatEvening(briefing, dash, lang))
}

// ── formatters ───────────────────────────────────────────────────────────────

var morningHeader = map[string]string{
	"en": "🌅 Morning report",
	"ru": "🌅 Утренний отчёт",
	"sr": "🌅 Jutarnji izveštaj",
}
var eveningHeader = map[string]string{
	"en": "🌆 Day summary",
	"ru": "🌆 Итоги дня",
	"sr": "🌆 Pregled dana",
}
var statusEmoji = map[string]string{
	"good": "🟢",
	"fair": "🟡",
	"low":  "🔴",
}

func formatMorning(b *health.BriefingResponse, lang string) string {
	hdr := morningHeader[lang]
	if hdr == "" {
		hdr = morningHeader["en"]
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>%s — %s</b>\n\n", hdr, b.Date))

	// Sleep section
	if b.Sleep != nil {
		// Find the sleep section for its details
		for _, sec := range b.Sections {
			if sec.Key != "sleep" {
				continue
			}
			sb.WriteString(fmt.Sprintf("%s <b>%s</b> — %s\n", statusEmoji[sec.Status], sec.Title, sec.Summary))
			for _, d := range sec.Details {
				sb.WriteString(fmt.Sprintf("  • %s: %s", d.Label, d.Value))
				if d.Note != "" {
					sb.WriteString(fmt.Sprintf(" <i>(%s)</i>", d.Note))
				}
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		}

		// Per-source breakdown if multiple devices
		if len(b.Sleep.Sources) > 1 {
			sb.WriteString("📱 <i>Sources:</i>\n")
			for _, src := range b.Sleep.Sources {
				sb.WriteString(fmt.Sprintf("  %s — %.1fh\n", src.Source, src.Total))
			}
			sb.WriteString("\n")
		}
	}

	// Readiness
	emoji := statusEmoji["good"]
	if b.ReadinessScore < 60 {
		emoji = statusEmoji["low"]
	} else if b.ReadinessScore < 75 {
		emoji = statusEmoji["fair"]
	}
	sb.WriteString(fmt.Sprintf("%s <b>Readiness: %d/100</b> — %s\n", emoji, b.ReadinessScore, b.ReadinessLabel))
	if b.ReadinessTip != "" {
		sb.WriteString(fmt.Sprintf("<i>%s</i>\n", b.ReadinessTip))
	}
	sb.WriteString("\n")

	// Recovery section (HRV / RHR)
	for _, sec := range b.Sections {
		if sec.Key != "recovery" {
			continue
		}
		sb.WriteString(fmt.Sprintf("%s <b>%s</b> — %s\n", statusEmoji[sec.Status], sec.Title, sec.Summary))
		for _, d := range sec.Details {
			sb.WriteString(fmt.Sprintf("  • %s: %s", d.Label, d.Value))
			if d.Note != "" {
				sb.WriteString(fmt.Sprintf(" <i>(%s)</i>", d.Note))
			}
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

func formatEvening(b *health.BriefingResponse, dash *storage.DashboardResponse, lang string) string {
	hdr := eveningHeader[lang]
	if hdr == "" {
		hdr = eveningHeader["en"]
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>%s — %s</b>\n\n", hdr, b.Date))

	// Activity section
	for _, sec := range b.Sections {
		if sec.Key != "activity" {
			continue
		}
		sb.WriteString(fmt.Sprintf("%s <b>%s</b> — %s\n", statusEmoji[sec.Status], sec.Title, sec.Summary))
		for _, d := range sec.Details {
			sb.WriteString(fmt.Sprintf("  • %s: %s", d.Label, d.Value))
			if d.Note != "" {
				sb.WriteString(fmt.Sprintf(" <i>(%s)</i>", d.Note))
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// Cardio section
	for _, sec := range b.Sections {
		if sec.Key != "cardio" {
			continue
		}
		sb.WriteString(fmt.Sprintf("%s <b>%s</b> — %s\n", statusEmoji[sec.Status], sec.Title, sec.Summary))
		for _, d := range sec.Details {
			sb.WriteString(fmt.Sprintf("  • %s: %s", d.Label, d.Value))
			if d.Note != "" {
				sb.WriteString(fmt.Sprintf(" <i>(%s)</i>", d.Note))
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// Today's dashboard values (steps, calories, exercise)
	if dash != nil {
		dashMap := make(map[string]storage.CardData)
		for _, c := range dash.Cards {
			dashMap[c.Metric] = c
		}
		sb.WriteString("📊 <b>Today</b>\n")
		for _, metric := range []string{"step_count", "active_energy", "apple_exercise_time"} {
			if c, ok := dashMap[metric]; ok && c.Value > 0 {
				icon := map[string]string{
					"step_count":          "👟",
					"active_energy":       "🔥",
					"apple_exercise_time": "🏃",
				}[metric]
				trend := ""
				if c.Prev > 0 {
					pct := (c.Value - c.Prev) / c.Prev * 100
					if pct > 5 {
						trend = fmt.Sprintf(" <i>(+%.0f%% vs yesterday)</i>", pct)
					} else if pct < -5 {
						trend = fmt.Sprintf(" <i>(%.0f%% vs yesterday)</i>", pct)
					}
				}
				sb.WriteString(fmt.Sprintf("  %s %.0f %s%s\n", icon, c.Value, c.Unit, trend))
			}
		}
		sb.WriteString("\n")
	}

	// Readiness
	emoji := statusEmoji["good"]
	if b.ReadinessScore < 60 {
		emoji = statusEmoji["low"]
	} else if b.ReadinessScore < 75 {
		emoji = statusEmoji["fair"]
	}
	sb.WriteString(fmt.Sprintf("%s <b>Readiness: %d/100</b> — %s\n\n", emoji, b.ReadinessScore, b.ReadinessLabel))

	// Top insights
	if len(b.Insights) > 0 {
		sb.WriteString("💡 <b>Insights</b>\n")
		for i, ins := range b.Insights {
			if i >= 3 {
				break
			}
			icon := "✅"
			if ins.Type == "warning" {
				icon = "⚠️"
			}
			sb.WriteString(fmt.Sprintf("  %s %s\n", icon, ins.Text))
		}
	}

	return sb.String()
}
