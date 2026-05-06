package notify

import (
	"fmt"
	"strings"
	"time"

	"health-receiver/internal/storage"
)

// SendWeeklyDigest builds and sends the data-quality digest. Called from the
// morning scheduler on a cadence (default Monday). No-op when there's nothing
// to report — we don't want to train the user to ignore digest pings.
//
// Returns sent=false, reason="empty" when there are no findings, sent=false,
// reason="not_enabled" when Telegram isn't configured.
func SendWeeklyDigest(bot *Bot, db *storage.DB, cfg Config, days int) (bool, string, error) {
	if !cfg.Enabled() {
		return false, "not_enabled", nil
	}
	stats, err := db.WeeklyQualityReport(days)
	if err != nil {
		return false, "query_error", err
	}
	if !stats.HasFindings() {
		return false, "empty", nil
	}
	msg := formatWeeklyDigest(stats, cfg.Lang)
	return true, "sent", bot.Send(msg)
}

// SendWeeklyDigestForce sends regardless of findings — used by the admin
// "send test digest" button. When stats are empty, an "all clean" message is
// produced so the user sees the button worked.
func SendWeeklyDigestForce(bot *Bot, db *storage.DB, cfg Config, days int) error {
	if !cfg.Enabled() {
		return fmt.Errorf("Telegram not configured")
	}
	stats, err := db.WeeklyQualityReport(days)
	if err != nil {
		return err
	}
	return bot.Send(formatWeeklyDigest(stats, cfg.Lang))
}

func formatWeeklyDigest(s storage.WeeklyQualityStats, lang string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>%s</b>\n\n", tr(lang, "tg_digest_header"))

	if !s.HasFindings() {
		fmt.Fprintf(&sb, tr(lang, "tg_digest_clean")+"\n", s.Days)
		return sb.String()
	}

	if len(s.Impossible) > 0 {
		fmt.Fprintf(&sb, "%s\n", tr(lang, "tg_digest_impossible"))
		for metric, n := range s.Impossible {
			fmt.Fprintf(&sb, "  • %s — %d\n", prettyMetric(metric), n)
		}
		sb.WriteByte('\n')
	}

	if len(s.Suspect) > 0 {
		fmt.Fprintf(&sb, "%s\n", tr(lang, "tg_digest_suspect"))
		for metric, n := range s.Suspect {
			fmt.Fprintf(&sb, "  • %s — %d\n", prettyMetric(metric), n)
		}
		sb.WriteByte('\n')
	}

	if len(s.MissedSleepNights) > 0 {
		fmt.Fprintf(&sb, "%s (%d):\n", tr(lang, "tg_digest_missed"), len(s.MissedSleepNights))
		for _, d := range s.MissedSleepNights {
			fmt.Fprintf(&sb, "  • %s\n", d)
		}
		sb.WriteByte('\n')
	}

	if s.WatchOffHoursTotal > 0 {
		fmt.Fprintf(&sb, tr(lang, "tg_digest_watch_off")+"\n\n", s.WatchOffHoursTotal)
	}

	sb.WriteString(tr(lang, "tg_digest_more_in_ui") + "\n")
	return strings.TrimRight(sb.String(), "\n")
}

// prettyMetric is a tiny renamer so the digest reads better than raw column
// names. Falls back to the original if no mapping exists.
func prettyMetric(m string) string {
	switch m {
	case "heart_rate_variability":
		return "HRV"
	case "resting_heart_rate":
		return "RHR"
	case "oxygen_saturation":
		return "SpO₂"
	case "respiratory_rate":
		return "Resp"
	case "wrist_temperature":
		return "Wrist temp"
	case "vo2_max":
		return "VO₂ max"
	case "body_mass":
		return "Weight"
	case "step_count":
		return "Steps"
	case "active_energy":
		return "Active kcal"
	case "sleep_total":
		return "Sleep"
	}
	return m
}

// ── scheduling ──────────────────────────────────────────────────────────────

// digestSentSettingKey stores the date the last digest was sent. Date string,
// not bool, so we can implement "weekly cadence" without a separate cron.
const digestSentSettingKey = "weekly_digest_last_sent"

// MaybeSendWeeklyDigest sends the digest if today is the configured day-of-week
// and we haven't sent it yet today. Idempotent — safe to call from the morning
// scheduler tick. The day-of-week is configurable via `weekly_digest_dow`
// setting (0=Sunday … 6=Saturday); default Monday (1).
func MaybeSendWeeklyDigest(bot *Bot, db *storage.DB, cfg Config) {
	loc := cfg.location()
	now := time.Now().In(loc)

	dow := db.GetSettingInt("weekly_digest_dow", 1) // Monday default
	if int(now.Weekday()) != dow {
		return
	}
	today := now.Format("2006-01-02")
	if last := db.GetSetting(digestSentSettingKey, ""); last == today {
		return
	}

	sent, reason, err := SendWeeklyDigest(bot, db, cfg, 7)
	if err != nil {
		// Log via caller's logger — return silently so the morning report
		// flow isn't disrupted by a digest hiccup.
		return
	}
	if sent || reason == "empty" {
		// Mark sent in both cases — "empty" still counts as "we did our weekly
		// check today" so we don't keep retrying every tick.
		db.SaveSettings(map[string]string{digestSentSettingKey: today})
	}
}
