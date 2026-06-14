package notify

import (
	"fmt"
	"log"
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
	EveningWeekdayHour   int
	EveningWeekendHour   int
	TelegramRichMessages bool

	// Smart-retry deadline. The morning trigger keeps deferring until sleep
	// data settles (see storage.SleepSettled); past this hour it force-sends
	// with a banner. Caller is responsible for picking a sensible default
	// (typically MorningHour + 4, floor 11). Zero means "no cap" — use with
	// care; can lead to no morning report on watch-off days.
	MorningCapHour int

	// Adaptive cap from the user's typical wake-time (median over recent
	// days, +60min). When TypicalWakeOK is true these override MorningCapHour
	// so the deadline tracks late-rising days. Populated by the scheduler
	// from storage.GetTypicalWakeTime; zero when not enough segment data.
	TypicalWakeHour   int
	TypicalWakeMinute int
	TypicalWakeOK     bool
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

// MinPromptWindow is the minimum stretch between the moment
// runMorningSmartRetry enters and MorningCapTime, so the gate always
// has time to send a check-in prompt and wait for an answer before the
// cap-driven force-send fires. Without this floor, an adaptive cap
// (typical_wake + 60min) earlier than the configured morning_hour
// collapses the prompt window to zero and the gate jumps straight to
// MorningActionForce on entry, silently disabling check-in. Pinned
// by TestMorningCapTime_FloorsPastCapsToPromptWindow.
const MinPromptWindow = 60 * time.Minute

// MorningCapTime returns the deadline timestamp for today's morning report.
// Past this time the smart-retry loop force-sends. Falls back to a sensible
// default (morning hour + 4, never earlier than 11:00) if MorningCapHour is
// unset, so a brand-new install with no override still has a deadline.
//
// Floors the result to now + MinPromptWindow when the computed cap is
// already in the past at call time. Adaptive cap (typical_wake + 60min)
// can land BEFORE the configured morning_hour for users whose schedule
// puts the morning report well after their typical wake — without the
// floor the smart-retry loop enters past cap and skips the check-in
// prompt entirely.
func (c Config) MorningCapTime(now time.Time) time.Time {
	loc := c.location()
	now = now.In(loc)
	cap := c.computeMorningCap(now, loc)
	if !cap.After(now) {
		cap = now.Add(MinPromptWindow)
	}
	return cap
}

// computeMorningCap is the pre-floor cap calculation. Split from
// MorningCapTime so the floor is the single place it gets applied.
func (c Config) computeMorningCap(now time.Time, loc *time.Location) time.Time {
	if c.TypicalWakeOK {
		t := time.Date(now.Year(), now.Month(), now.Day(),
			c.TypicalWakeHour, c.TypicalWakeMinute, 0, 0, loc).Add(60 * time.Minute)
		// Adding 60 min can roll the timestamp into tomorrow (e.g. typical wake
		// 23:30 -> cap 00:30 next day, which would defer the morning report
		// indefinitely). Detect via date components and clamp to today 23:00.
		if t.Year() != now.Year() || t.Month() != now.Month() || t.Day() != now.Day() {
			t = time.Date(now.Year(), now.Month(), now.Day(), 23, 0, 0, 0, loc)
		}
		return t
	}
	cap := c.MorningCapHour
	if cap <= 0 {
		cap = c.morningHour(now.Weekday()) + 4
		if cap < 11 {
			cap = 11
		}
	}
	if cap > 23 {
		cap = 23
	}
	return time.Date(now.Year(), now.Month(), now.Day(), cap, 0, 0, 0, loc)
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
	_, _, err := SendMorningSmart(bot, db, cfg, true)
	return err
}

// MorningSendOpts configures the morning report send. Existing callers
// continue to use SendMorningSmart(force) which builds a default opts;
// runMorningSmartRetry passes CheckinExpired=true on the expire-and-
// force path so formatMorning appends the soft "answer tomorrow" note.
type MorningSendOpts struct {
	Force          bool
	CheckinExpired bool
}

// SendMorningSmart is the smart-retry-aware morning sender. When force is
// false, it only sends if sleep data has settled (see storage.SleepSettled);
// otherwise returns sent=false with a non-"ok" reason so the caller can retry
// later. When force is true, it sends regardless and prepends a banner
// explaining why the data is incomplete.
//
// Thin wrapper over SendMorningSmartOpts that preserves the historical
// signature for the two non-cap call sites (ingest trigger + webhook
// retrigger) — neither of those knows about check-in expiry.
//
// Returns (sent, reason, error). reason is the SleepSettleStatus.Reason —
// "ok" when settled, otherwise "no_data" / "recent_segment" / "still_writing".
func SendMorningSmart(bot *Bot, db *storage.DB, cfg Config, force bool) (bool, string, error) {
	return SendMorningSmartOpts(bot, db, cfg, MorningSendOpts{Force: force})
}

// SendMorningSmartOpts is the configurable variant. Callers that know
// extra context (e.g. cap-path saw a prompted check-in expire) pass it
// via MorningSendOpts so formatMorning can render the right copy.
func SendMorningSmartOpts(bot *Bot, db *storage.DB, cfg Config, opts MorningSendOpts) (bool, string, error) {
	loc := cfg.location()
	today := time.Now().In(loc).Format("2006-01-02")

	status := db.SleepSettled(today)
	if !status.Settled && !opts.Force {
		return false, status.Reason, nil
	}

	briefing, err := db.GetHealthBriefing(cfg.Lang)
	if err != nil {
		return false, status.Reason, err
	}
	aiBlocks := db.GetAIBlocks(today, cfg.Lang)
	fresh := computeFreshness(db, time.Now())
	msg := formatMorning(briefing, aiBlocks, cfg.Lang, loc, fresh, opts.CheckinExpired)
	richMsg := ""
	if cfg.TelegramRichMessages {
		richMsg = formatMorningRich(briefing, aiBlocks, cfg.Lang, loc, fresh, opts.CheckinExpired, "")
	}

	if !status.Settled {
		if banner := tr(cfg.Lang, "tg_stale_"+status.Reason); banner != "" && banner != "tg_stale_"+status.Reason {
			msg = banner + "\n\n" + msg
			if cfg.TelegramRichMessages {
				richMsg = formatMorningRich(briefing, aiBlocks, cfg.Lang, loc, fresh, opts.CheckinExpired, banner)
			}
		}
	}
	return true, status.Reason, sendReportHTML(bot, cfg, "morning", richMsg, msg)
}

// SendEvening sends a "today so far" snapshot. Activity bullets are intentionally
// omitted because the briefing's activity/cardio sections describe yesterday —
// users get the full retrospective in the morning report.
func SendEvening(bot *Bot, db *storage.DB, cfg Config) error {
	briefing, err := db.GetHealthBriefing(cfg.Lang)
	if err != nil {
		return err
	}
	dash, err := db.GetDashboard()
	if err != nil {
		return err
	}
	fresh := computeFreshness(db, time.Now())
	fallback := formatEvening(briefing, dash, cfg.Lang, cfg.location(), fresh)
	rich := ""
	if cfg.TelegramRichMessages {
		rich = formatEveningRich(briefing, dash, cfg.Lang, cfg.location(), fresh)
	}
	return sendReportHTML(bot, cfg, "evening", rich, fallback)
}

type htmlReportSender interface {
	Send(text string) error
	SendRichHTML(html string) error
}

func sendReportHTML(bot htmlReportSender, cfg Config, label, richHTML, fallbackHTML string) error {
	if cfg.TelegramRichMessages && strings.TrimSpace(richHTML) != "" {
		if err := bot.SendRichHTML(richHTML); err == nil {
			return nil
		} else {
			log.Printf("telegram %s rich send failed, falling back to sendMessage HTML: %v", label, err)
		}
	}
	return bot.Send(fallbackHTML)
}

// ── helpers ──────────────────────────────────────────────────────────────────

// Per-metric silence thresholds for the "device off" banners. Tuned to be
// forgiving — a 24h gap is "phone in airplane mode for the day", a 36h gap
// is "watch wasn't worn last night either". Beyond that we replace the
// section with a banner instead of pretending to have data.
const (
	silenceSleep = 36 * time.Hour
	silenceWatch = 36 * time.Hour
	silencePhone = 24 * time.Hour
)

// freshness summarises per-metric-group silence durations for the report
// formatter. Computed once per send (one DB query), passed to both
// formatMorning and formatEvening so they don't duplicate the lookup.
type freshness struct {
	sleep, watch, phone time.Duration
	// known flags distinguish "no data ever recorded" (e.g. user just signed
	// up) from "data exists but it's old". For the banners we treat both the
	// same — but logging is clearer this way.
	sleepKnown, watchKnown, phoneKnown bool
}

// computeFreshness queries the storage layer for last-seen times of the four
// metrics that gate the morning report's main sections. Watch presence is the
// MIN age across HRV and RHR — if either was recorded recently, the watch was
// on. Phone presence uses step_count, which is written by both iPhone and
// Apple Watch, so it stays fresh as long as either device is moving.
func computeFreshness(db *storage.DB, now time.Time) freshness {
	seen := db.MetricsLastSeen(
		"sleep_total",
		"heart_rate_variability",
		"resting_heart_rate",
		"step_count",
	)
	f := freshness{}
	age := func(metric string) (time.Duration, bool) {
		t, ok := seen[metric]
		if !ok {
			return 0, false
		}
		d := now.Sub(t)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	f.sleep, f.sleepKnown = age("sleep_total")
	hrv, hrvK := age("heart_rate_variability")
	rhr, rhrK := age("resting_heart_rate")
	if hrvK || rhrK {
		f.watchKnown = true
		// Use the freshest of the two — watch is "on" if either sensor wrote
		// recently. Default the missing sensor to a huge value so MIN works.
		if !hrvK {
			hrv = 9999 * time.Hour
		}
		if !rhrK {
			rhr = 9999 * time.Hour
		}
		f.watch = hrv
		if rhr < hrv {
			f.watch = rhr
		}
	}
	f.phone, f.phoneKnown = age("step_count")
	return f
}

func (f freshness) sleepStale() bool { return !f.sleepKnown || f.sleep > silenceSleep }
func (f freshness) watchOff() bool   { return !f.watchKnown || f.watch > silenceWatch }
func (f freshness) phoneOff() bool   { return !f.phoneKnown || f.phone > silencePhone }

// fmtSilence renders a duration as "Nh" or "N days" via the configured lang.
// Switches to days at the 48h mark — "73h ago" reads worse than "3 days ago".
func fmtSilence(d time.Duration, lang string) string {
	h := int(d / time.Hour)
	if h >= 48 {
		return fmt.Sprintf(tr(lang, "tg_dur_days"), h/24)
	}
	if h < 1 {
		h = 1 // round up tiny gaps so banners don't say "0h ago"
	}
	return fmt.Sprintf(tr(lang, "tg_dur_hours"), h)
}

func tr(lang, key string) string {
	if v, ok := health.GetStrings(lang)[key]; ok {
		return v
	}
	if v, ok := health.GetStrings("en")[key]; ok {
		return v
	}
	return key
}

var statusEmoji = map[string]string{
	"good": "🟢",
	"fair": "🟡",
	"low":  "🔴",
}

func headlineEmoji(severity string) string {
	switch severity {
	case "warning":
		return "🚨"
	case "positive":
		return "✅"
	default:
		return "💡"
	}
}

func alertEmoji(severity string) string {
	if severity == "critical" {
		return "🔴"
	}
	return "⚠️"
}

func readinessEmoji(score int) string {
	switch {
	case score < 60:
		return statusEmoji["low"]
	case score < 75:
		return statusEmoji["fair"]
	default:
		return statusEmoji["good"]
	}
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

// isBoringNote suppresses parenthetical notes that just restate "all is normal".
// These add visual noise without information; the section status emoji already
// conveys "fine". Notes carrying numerical signal (digits, ±, %, SD, z=) are
// always kept.
func isBoringNote(note string) bool {
	if note == "" {
		return true
	}
	if strings.ContainsAny(note, "0123456789±%") {
		return false
	}
	low := strings.ToLower(note)
	// Phrases that mean "nothing to flag here" across en/ru/sr.
	boring := []string{
		"stable", "well rested", "consistent with", "good ratio", "healthy range",
		"on par", "meeting the daily",
		"стабильно", "хорош", "соответств", "в рамках", "в пределах нормы", "выполня",
		"stabil", "konzist", "zdrav",
	}
	for _, b := range boring {
		if strings.Contains(low, b) {
			return true
		}
	}
	return false
}

func findSection(b *health.BriefingResponse, key string) *health.BriefingSection {
	for i := range b.Sections {
		if b.Sections[i].Key == key {
			return &b.Sections[i]
		}
	}
	return nil
}

// renderSectionBullets renders the section header + filtered bullet list.
// Rule-based output is the source of truth (medical-literature-backed); the AI
// take, when present, is layered underneath as commentary, not a replacement —
// so a Gemini hallucination can be cross-checked against the bullets above it.
func renderSectionBullets(sb *strings.Builder, sec *health.BriefingSection) {
	if sec == nil {
		return
	}
	fmt.Fprintf(sb, "%s <b>%s</b> — %s\n", statusEmoji[sec.Status], sec.Title, sec.Summary)
	for _, d := range sec.Details {
		fmt.Fprintf(sb, "  • %s: %s", d.Label, d.Value)
		if d.Note != "" && !isBoringNote(d.Note) {
			fmt.Fprintf(sb, " <i>(%s)</i>", d.Note)
		}
		sb.WriteByte('\n')
	}
}

// renderAITake prints the AI prose for a section underneath rule-based bullets.
// Marked with 🤖 so the user immediately sees this is the LLM layer, not the
// scoring engine. Empty body is a no-op.
func renderAITake(sb *strings.Builder, body string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	fmt.Fprintf(sb, "  🤖 <i>%s</i>\n", body)
}

func renderHeadline(sb *strings.Builder, h *health.HeadlineSignal) {
	if h == nil || h.Title == "" {
		return
	}
	// "stable" / "good_recovery" headlines are good news — keep them concise.
	fmt.Fprintf(sb, "%s <b>%s</b>\n", headlineEmoji(h.Severity), h.Title)
	if h.Detail != "" {
		fmt.Fprintf(sb, "<i>%s</i>\n", h.Detail)
	}
	sb.WriteByte('\n')
}

func renderEnergyBank(sb *strings.Builder, eb *health.EnergyBank, lang string) {
	if eb == nil || eb.Capacity == 0 {
		return
	}
	verdict := tr(lang, "energy_verdict_"+eb.ActionVerdict)
	if verdict == "energy_verdict_"+eb.ActionVerdict {
		verdict = eb.ActionVerdict // fallback to enum if key missing
	}
	fmt.Fprintf(sb, "⚡ <b>%s: %d/%d</b> — %s\n",
		tr(lang, "tg_energy"), eb.Current, eb.Capacity, verdict)
	if eb.VerdictReason != "" {
		fmt.Fprintf(sb, "<i>%s</i>\n", eb.VerdictReason)
	}
	sb.WriteByte('\n')
}

func renderReadiness(sb *strings.Builder, b *health.BriefingResponse, lang string) {
	today := b.ReadinessToday
	trend := b.ReadinessScore
	collapsed := abs(today-trend) <= 2

	if collapsed {
		fmt.Fprintf(sb, "%s <b>%s: %d/100</b> — %s\n",
			readinessEmoji(today), tr(lang, "tg_readiness"), today, b.ReadinessTodayLabel)
	} else {
		fmt.Fprintf(sb, "%s <b>%s %s: %d/100</b> — %s\n",
			readinessEmoji(today), tr(lang, "tg_readiness"), tr(lang, "tg_readiness_today"),
			today, b.ReadinessTodayLabel)
		fmt.Fprintf(sb, "📈 %s: %d/100 — %s\n",
			tr(lang, "tg_readiness_trend"), trend, b.ReadinessLabel)
	}
	if b.ReadinessTip != "" {
		fmt.Fprintf(sb, "<i>%s</i>\n", b.ReadinessTip)
	}
	sb.WriteByte('\n')
}

func renderAlerts(sb *strings.Builder, alerts []health.Alert, lang string) {
	if len(alerts) == 0 {
		return
	}
	fmt.Fprintf(sb, "⚠️ <b>%s</b>\n", tr(lang, "tg_alerts"))
	for _, a := range alerts {
		fmt.Fprintf(sb, "  %s %s\n", alertEmoji(a.Severity), a.Text)
	}
	sb.WriteByte('\n')
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// ── morning ──────────────────────────────────────────────────────────────────

func formatMorning(b *health.BriefingResponse, aiBlocks map[string]string, lang string, loc *time.Location, f freshness, checkinExpired bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>🌅 %s — %s</b>\n\n", tr(lang, "tg_morning_header"), b.Date)

	if d := staleDays(b.Date, loc); d >= 1 {
		fmt.Fprintf(&sb, tr(lang, "tg_warn_stale")+"\n\n", d)
	}

	// Headline first — sets the frame for everything below ("see headline above"
	// references in section summaries now actually resolve).
	renderHeadline(&sb, b.Headline)

	// AI blocks come pre-split from ai_briefing_blocks; no parsing needed.
	ai := struct{ Sleep, Yesterday, Recovery, Recommendation string }{
		Sleep:          aiBlocks["SLEEP"],
		Yesterday:      aiBlocks["YESTERDAY"],
		Recovery:       aiBlocks["RECOVERY"],
		Recommendation: aiBlocks["RECOMMENDATION"],
	}

	renderEnergyBank(&sb, b.EnergyBank, lang)
	renderReadiness(&sb, b, lang)
	renderAlerts(&sb, b.Alerts, lang)

	// Sleep — rule-based bullets always; AI take layered underneath. If sleep
	// data is silent for ≥36h, the briefing is from a stale night and the
	// section misleads — replace with banner.
	switch {
	case f.sleepStale() && f.sleepKnown:
		fmt.Fprintf(&sb, tr(lang, "tg_sleep_silence")+"\n\n", fmtSilence(f.sleep, lang))
	case b.Sleep == nil:
		sb.WriteString(tr(lang, "tg_warn_no_sleep") + "\n\n")
	default:
		renderSectionBullets(&sb, findSection(b, "sleep"))
		if len(b.Sleep.Sources) > 1 {
			fmt.Fprintf(&sb, "📱 <i>%s:</i>\n", tr(lang, "tg_sources"))
			for _, src := range b.Sleep.Sources {
				fmt.Fprintf(&sb, "  %s — %.1fh\n", src.Source, src.Total)
			}
		}
		renderAITake(&sb, ai.Sleep)
		sb.WriteByte('\n')
	}

	// Yesterday — activity + cardio as the rule-based retrospective. Phone-off
	// (no step data ≥24h) collapses the whole block into a banner.
	if f.phoneOff() && f.phoneKnown {
		fmt.Fprintf(&sb, tr(lang, "tg_phone_off")+"\n\n", fmtSilence(f.phone, lang))
	} else {
		actSec := findSection(b, "activity")
		cardioSec := findSection(b, "cardio")
		if actSec != nil || cardioSec != nil || ai.Yesterday != "" {
			fmt.Fprintf(&sb, "📅 <b>%s</b>\n", tr(lang, "tg_yesterday"))
			if actSec != nil {
				renderSectionBullets(&sb, actSec)
			}
			if cardioSec != nil {
				renderSectionBullets(&sb, cardioSec)
			}
			renderAITake(&sb, ai.Yesterday)
			sb.WriteByte('\n')
		}
	}

	// Recovery — watch off (HRV+RHR both silent ≥36h) collapses to a banner.
	// Without HRV/RHR the section's numbers are days-old and would mislead.
	if f.watchOff() && f.watchKnown {
		fmt.Fprintf(&sb, tr(lang, "tg_watch_off")+"\n\n", fmtSilence(f.watch, lang))
	} else if recSec := findSection(b, "recovery"); recSec != nil {
		renderSectionBullets(&sb, recSec)
		renderAITake(&sb, ai.Recovery)
		sb.WriteByte('\n')
	}

	// Recommendation — actionable closer. AI-only; rule-based equivalent is
	// already covered by EnergyBank.VerdictReason and ReadinessTip rendered above.
	if ai.Recommendation != "" {
		fmt.Fprintf(&sb, "🎯 <b>%s</b>\n%s\n", tr(lang, "tg_recommendation"), strings.TrimSpace(ai.Recommendation))
	}

	// Soft footer: when the cap-path forced the report after the user
	// didn't tap the check-in button in time, append a one-line italic
	// nudge. Drives the next morning's engagement without scolding —
	// "want the report to reflect your state better? answer tomorrow".
	// Only the cap-path passes checkinExpired=true; ingest trigger and
	// webhook-retrigger paths skip the note (it would be misleading
	// when the user did answer or was never prompted).
	if checkinExpired {
		if note := tr(lang, "checkin_expired_note"); note != "" && note != "checkin_expired_note" {
			fmt.Fprintf(&sb, "\n%s\n", note)
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}

// ── evening ──────────────────────────────────────────────────────────────────

func formatEvening(b *health.BriefingResponse, dash *storage.DashboardResponse, lang string, loc *time.Location, f freshness) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>🌆 %s — %s</b>\n\n", tr(lang, "tg_evening_header"), b.Date)

	if d := staleDays(b.Date, loc); d >= 1 {
		fmt.Fprintf(&sb, tr(lang, "tg_warn_stale")+"\n\n", d)
	}

	now := time.Now().In(loc)
	today := fmt.Sprintf("%d-%02d-%02d", now.Year(), int(now.Month()), now.Day())
	hasDash := dash != nil && dash.Date == today && len(dash.Cards) > 0

	switch {
	case f.phoneOff() && f.phoneKnown:
		// Steps haven't arrived in ≥24h — phone or watch silent. The dashboard
		// "today" cards would all be 0/missing, so collapse to a banner.
		fmt.Fprintf(&sb, tr(lang, "tg_phone_off")+"\n\n", fmtSilence(f.phone, lang))
	case !hasDash:
		sb.WriteString(tr(lang, "tg_warn_no_activity") + "\n\n")
	default:
		fmt.Fprintf(&sb, "📊 <b>%s</b>\n", tr(lang, "tg_today"))
		dashMap := make(map[string]storage.CardData, len(dash.Cards))
		for _, c := range dash.Cards {
			dashMap[c.Metric] = c
		}
		icons := map[string]string{
			"step_count":          "👟",
			"active_energy":       "🔥",
			"apple_exercise_time": "🏃",
		}
		for _, metric := range []string{"step_count", "active_energy", "apple_exercise_time"} {
			c, ok := dashMap[metric]
			if !ok || c.Value <= 0 {
				continue
			}
			trend := ""
			if c.Prev > 0 {
				pct := (c.Value - c.Prev) / c.Prev * 100
				switch {
				case pct > 5:
					trend = " <i>(" + fmt.Sprintf(tr(lang, "tg_vs_yesterday_up"), pct) + ")</i>"
				case pct < -5:
					trend = " <i>(" + fmt.Sprintf(tr(lang, "tg_vs_yesterday_down"), pct) + ")</i>"
				}
			}
			fmt.Fprintf(&sb, "  %s %.0f %s%s\n", icons[metric], c.Value, c.Unit, trend)
		}
		sb.WriteByte('\n')
	}

	// Energy Bank — most useful evening signal: shows how much capacity drained
	// since morning, which directly answers "should I still train tonight?".
	renderEnergyBank(&sb, b.EnergyBank, lang)

	// Readiness — current 7-day trend (today's score is morning-based and stale by evening).
	emoji := readinessEmoji(b.ReadinessScore)
	fmt.Fprintf(&sb, "%s <b>%s: %d/100</b> — %s\n\n",
		emoji, tr(lang, "tg_readiness"), b.ReadinessScore, b.ReadinessLabel)

	// Alerts that surfaced during the day still matter at evening.
	renderAlerts(&sb, b.Alerts, lang)

	if len(b.Insights) > 0 {
		fmt.Fprintf(&sb, "💡 <b>%s</b>\n", tr(lang, "tg_insights"))
		for i, ins := range b.Insights {
			if i >= 3 {
				break
			}
			icon := "✅"
			if ins.Type == "warning" {
				icon = "⚠️"
			}
			fmt.Fprintf(&sb, "  %s %s\n", icon, ins.Text)
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}
