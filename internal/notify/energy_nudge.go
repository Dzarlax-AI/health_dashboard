package notify

import (
	"context"
	"fmt"
	"strings"
	"time"

	"health-receiver/internal/storage"
)

// energyBackfillNudgeSentKey stores the date the last EnergyBank
// backfill nudge was sent. Date string (YYYY-MM-DD), not timestamp,
// for the same reason `digestSentSettingKey` uses one: we re-prompt
// at most once per day, and a stale clock or NTP jitter shouldn't
// fire two nudges in one morning.
const energyBackfillNudgeSentKey = "energy_backfill_nudge_last_sent"

// energyBackfillNudgeMinCompleteDays is the threshold below which the
// nudge stays silent. Imported users with <30 days of complete history
// can run backfill but the personal verdict bands won't kick in (see
// energyBandsMinPoints in storage/energy_bands.go), so the value
// proposition isn't there yet. Above 30, calibration starts paying
// off the moment the backfill finishes.
const energyBackfillNudgeMinCompleteDays = 30

// energyBackfillNudgeMaxBackfilled is the upper bound: once a tenant
// has >=10 backfilled snapshots, they've already pressed the button
// (live ingest writes ~12 snapshots/day, but those aren't flagged
// `backfilled` — only retrospective runs are). Stop nudging once
// some history is in place even if it's not the full available range.
const energyBackfillNudgeMaxBackfilled = 10

// SendEnergyBackfillNudge composes and sends the one-time-per-day
// Telegram nudge urging the user to run retrospective EnergyBank
// backfill. Returns sent=false, reason="not_needed" when the
// preconditions don't apply (TZ not set, too little data, already
// backfilled), sent=false, reason="not_enabled" when Telegram is off.
//
// Caller should mark the date-sent setting after a successful send;
// MaybeSendEnergyBackfillNudge does this automatically.
func SendEnergyBackfillNudge(bot *Bot, db *storage.DB, cfg Config, baseURL string) (sent bool, reason string, err error) {
	if !cfg.Enabled() {
		return false, "not_enabled", nil
	}
	// A TZ-less tenant can't compute backfill EOD timestamps
	// correctly (see /api/settings/energy-backfill precondition).
	// Don't nudge them to press a button that will reject them —
	// the in-app TZ guidance handles that case.
	if cfg.Timezone == "" {
		return false, "no_tz", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	complete, backfilled, err := db.EnergyBackfillCoverage(ctx)
	if err != nil {
		return false, "query_error", err
	}
	if complete < energyBackfillNudgeMinCompleteDays {
		return false, "not_enough_data", nil
	}
	if backfilled >= energyBackfillNudgeMaxBackfilled {
		return false, "already_backfilled", nil
	}

	msg := formatEnergyBackfillNudge(complete, backfilled, cfg.Lang, baseURL)
	return true, "sent", bot.Send(msg)
}

func formatEnergyBackfillNudge(complete, backfilled int, lang, baseURL string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>%s</b>\n\n", tr(lang, "tg_energy_backfill_nudge_header"))

	body := tr(lang, "tg_energy_backfill_nudge_body")
	body = strings.ReplaceAll(body, "{complete}", fmt.Sprintf("%d", complete))
	body = strings.ReplaceAll(body, "{backfilled}", fmt.Sprintf("%d", backfilled))
	sb.WriteString(body)
	sb.WriteString("\n\n")

	link := strings.TrimSuffix(baseURL, "/") + "/settings"
	fmt.Fprintf(&sb, `<a href="%s">%s</a>`, link, tr(lang, "tg_energy_backfill_nudge_cta"))
	return sb.String()
}

// reNudgeInterval is the minimum gap between two nudges. The intent
// is "remind weekly while preconditions hold, then go silent on
// success". 7 days mirrors the weekly digest cadence; users who
// dismiss the first nudge get a second one the following week and
// no more after that round of backfill completes.
const reNudgeInterval = 7 * 24 * time.Hour

// MaybeSendEnergyBackfillNudge sends the nudge if preconditions hold
// AND we haven't sent one in the last 7 days. Idempotent. Safe to
// call from the morning scheduler tick.
//
// baseURL is the install's public URL (without trailing slash) so
// the link in the Telegram message points at the right host. The
// caller (cmd/server/main.go) reads it from the BASE_URL env var
// the same way the MCP server endpoint does.
func MaybeSendEnergyBackfillNudge(bot *Bot, db *storage.DB, cfg Config, baseURL string) {
	loc := cfg.location()
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")

	if last := db.GetSetting(energyBackfillNudgeSentKey, ""); last != "" {
		// Date parses are forgiving — if a previous version wrote
		// something else, just treat it as "long ago".
		if t, err := time.ParseInLocation("2006-01-02", last, loc); err == nil {
			if now.Sub(t) < reNudgeInterval {
				return
			}
		}
	}

	sent, _, err := SendEnergyBackfillNudge(bot, db, cfg, baseURL)
	if err != nil {
		return
	}
	if sent {
		db.SaveSettings(map[string]string{energyBackfillNudgeSentKey: today})
	}
}
