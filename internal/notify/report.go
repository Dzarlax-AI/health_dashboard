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
	Token    string
	ChatID   string
	Lang     string
	Timezone string // IANA tz name, e.g. "Europe/Belgrade"; empty = system local

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

// location returns the configured time.Location, falling back to local.
func (c Config) location() *time.Location {
	if c.Timezone != "" {
		if loc, err := time.LoadLocation(c.Timezone); err == nil {
			return loc
		}
	}
	return time.Local
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

// NextMorning returns the next time the morning report should fire (in configured tz).
func (c Config) NextMorning(from time.Time) time.Time {
	loc := c.location()
	now := from.In(loc)
	h := c.morningHour(now.Weekday())
	t := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, loc)
	if !t.After(now) {
		t = t.Add(24 * time.Hour)
		t = time.Date(t.Year(), t.Month(), t.Day(), c.morningHour(t.Weekday()), 0, 0, 0, loc)
	}
	return t
}

// NextEvening returns the next time the evening report should fire (in configured tz).
func (c Config) NextEvening(from time.Time) time.Time {
	loc := c.location()
	now := from.In(loc)
	h := c.eveningHour(now.Weekday())
	t := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, loc)
	if !t.After(now) {
		t = t.Add(24 * time.Hour)
		t = time.Date(t.Year(), t.Month(), t.Day(), c.eveningHour(t.Weekday()), 0, 0, 0, loc)
	}
	return t
}

// SendMorning sends the sleep report for the most recent night.
// If an AI insight has been saved for today it is prepended to the message.
func SendMorning(bot *Bot, db *storage.DB, cfg Config) error {
	briefing, err := db.GetHealthBriefing(cfg.Lang)
	if err != nil {
		return err
	}
	loc := cfg.location()
	today := time.Now().In(loc).Format("2006-01-02")
	aiInsight := db.GetAIBriefing(today)
	return bot.Send(formatMorning(briefing, aiInsight, cfg.Lang, loc))
}

// SendEvening sends the daily activity summary.
func SendEvening(bot *Bot, db *storage.DB, cfg Config) error {
	briefing, err := db.GetHealthBriefing(cfg.Lang)
	if err != nil {
		return err
	}
	dash, err := db.GetDashboard()
	if err != nil {
		return err
	}
	return bot.Send(formatEvening(briefing, dash, cfg.Lang, cfg.location()))
}

// ── formatters ───────────────────────────────────────────────────────────────

var morningHeader = map[string]string{
	"en": "🌅 Morning report",
	"ru": "🌅 Утренний отчёт",
	"sr": "🌅 Jutarnji izveštaj",
}

var staleWarning = map[string]string{
	"en": "⚠️ <i>Data is %d day(s) old — Apple Health may not have synced yet.</i>\n\n",
	"ru": "⚠️ <i>Данные устарели на %d дн. — возможно, синхронизация ещё не прошла.</i>\n\n",
	"sr": "⚠️ <i>Podaci su stari %d dan(a) — sinhronizacija možda još nije završena.</i>\n\n",
}
var noSleepWarning = map[string]string{
	"en": "😴 <i>No sleep data for last night yet — phone may not have synced after waking up.</i>\n\n",
	"ru": "😴 <i>Данных о сне прошлой ночи пока нет — возможно, телефон ещё не синхронизировался.</i>\n\n",
	"sr": "😴 <i>Nema podataka o snu za prošlu noć — telefon možda još nije sinhronizovan.</i>\n\n",
}
var noActivityWarning = map[string]string{
	"en": "📭 <i>No activity data for today yet.</i>\n\n",
	"ru": "📭 <i>Данных об активности за сегодня пока нет.</i>\n\n",
	"sr": "📭 <i>Nema podataka o aktivnosti za danas.</i>\n\n",
}

// staleDays returns how many calendar days ago dataDate is relative to today in loc.
func staleDays(dataDate string, loc *time.Location) int {
	if dataDate == "" {
		return 999
	}
	t, err := time.Parse("2006-01-02", dataDate)
	if err != nil {
		return 0
	}
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	dataDay := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	return int(today.Sub(dataDay).Hours() / 24)
}

func warnMsg(m map[string]string, lang string, args ...any) string {
	tpl, ok := m[lang]
	if !ok {
		tpl = m["en"]
	}
	if len(args) > 0 {
		return fmt.Sprintf(tpl, args...)
	}
	return tpl
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

func formatMorning(b *health.BriefingResponse, aiInsight, lang string, loc *time.Location) string {
	hdr := morningHeader[lang]
	if hdr == "" {
		hdr = morningHeader["en"]
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>%s — %s</b>\n\n", hdr, b.Date))
	if aiInsight != "" {
		sb.WriteString(fmt.Sprintf("🤖 <i>%s</i>\n\n", aiInsight))
	}

	// Morning report should show last night's sleep (stored with today's date in Apple Health).
	// If data is 1+ day old, the phone hasn't synced after waking up yet.
	if d := staleDays(b.Date, loc); d >= 1 {
		sb.WriteString(warnMsg(staleWarning, lang, d))
	}

	// Sleep section
	if b.Sleep == nil {
		sb.WriteString(warnMsg(noSleepWarning, lang))
	} else {
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

	// Readiness: show both today and 7-day trend
	todayEmoji := statusEmoji["good"]
	if b.ReadinessToday < 60 {
		todayEmoji = statusEmoji["low"]
	} else if b.ReadinessToday < 75 {
		todayEmoji = statusEmoji["fair"]
	}
	sb.WriteString(fmt.Sprintf("%s <b>Readiness today: %d/100</b> — %s\n", todayEmoji, b.ReadinessToday, b.ReadinessTodayLabel))
	sb.WriteString(fmt.Sprintf("📈 7-day trend: %d/100 — %s\n", b.ReadinessScore, b.ReadinessLabel))
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

func formatEvening(b *health.BriefingResponse, dash *storage.DashboardResponse, lang string, loc *time.Location) string {
	hdr := eveningHeader[lang]
	if hdr == "" {
		hdr = eveningHeader["en"]
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>%s — %s</b>\n\n", hdr, b.Date))

	// Evening report shows today's activity. If data is 1+ day old, it's stale.
	if d := staleDays(b.Date, loc); d >= 1 {
		sb.WriteString(warnMsg(staleWarning, lang, d))
	}

	// Check if dashboard has no data for today specifically.
	now := time.Now().In(loc)
	today := fmt.Sprintf("%d-%02d-%02d", now.Year(), int(now.Month()), now.Day())
	if dash == nil || dash.Date != today || len(dash.Cards) == 0 {
		sb.WriteString(warnMsg(noActivityWarning, lang))
	}

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
