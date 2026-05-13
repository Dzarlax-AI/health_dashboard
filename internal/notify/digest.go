package notify

import (
	"context"
	"fmt"
	"strings"
	"time"

	"health-receiver/internal/storage"
)

// SendWeeklyDigestForce sends regardless of findings — used by the admin
// "send test digest" button. When stats are empty, an "all clean" message is
// produced so the user sees the button worked. The scheduled / cadence-aware
// path lives in weeklyDigestRender (registered with the proactive framework
// in init() below) — only the forced admin button calls in here directly.
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

// Pre-framework persistence key. Kept as the registered notification's
// LegacyKey so a tenant that received this week's digest before the
// framework rollout doesn't get a duplicate on the next morning tick.
const legacyDigestSentKey = "weekly_digest_last_sent"

// digestLookbackDays mirrors the value the pre-framework code passed
// to SendWeeklyDigest. Hard-coded; if anyone ever wants it
// configurable, push it into a setting rather than hop through env.
const digestLookbackDays = 7

func init() {
	// Day-of-week is read at eligibility time so it can be tuned
	// in the settings table without a restart. Cadence is 6 days
	// (not 7) so a tenant who toggled their `weekly_digest_dow`
	// mid-week still sees the digest on the new day — strict 7d
	// would skip them if the new dow lands within the 7d window
	// of the last send.
	Register(ProactiveNotification{
		Name:      "weekly_digest",
		Cadence:   6 * 24 * time.Hour,
		HourOfDay: -1,
		LegacyKey: legacyDigestSentKey,
		Eligible:  weeklyDigestEligible,
		Render:    weeklyDigestRender,
	})
}

func weeklyDigestEligible(ctx context.Context, db *storage.DB, cfg Config) (bool, string) {
	loc := cfg.location()
	now := time.Now().In(loc)

	dow := db.GetSettingInt("weekly_digest_dow", 1) // Monday default
	if int(now.Weekday()) != dow {
		return false, "wrong_dow"
	}
	return true, ""
}

func weeklyDigestRender(ctx context.Context, db *storage.DB, cfg Config, baseURL string) (string, error) {
	stats, err := db.WeeklyQualityReport(digestLookbackDays)
	if err != nil {
		return "", err
	}
	if !stats.HasFindings() {
		// Empty digest: don't message the user, but DO let the
		// framework mark "sent" for the cadence. The pre-framework
		// code did the same — preserves "we ran our weekly check
		// today" semantics so we don't retry every tick.
		//
		// Implementation note: framework treats empty-string return
		// from Render as "skip with no persist", but we want "skip
		// the SEND but persist". The clean answer is to special-case
		// empty findings here by sending a *no-op marker*: persist
		// the date manually and return empty.
		//
		// Trade-off rejected: adding a third return value
		// (sentBool) to Render polyfills every notification with a
		// concept they don't need. Better to handle the digest's
		// special case inline.
		_ = db.SaveSettings(map[string]string{proactiveSentKey("weekly_digest"): time.Now().In(cfg.location()).Format("2006-01-02")})
		return "", nil
	}
	return formatWeeklyDigest(stats, cfg.Lang), nil
}
