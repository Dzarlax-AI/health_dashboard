package notify

import (
	"context"
	"fmt"
	"strings"
	"time"

	"health-receiver/internal/storage"
)

// Pre-framework persistence key (string used directly, since the
// internal `energyBackfillNudgeSentKey` constant no longer exists).
// Kept as a LegacyKey on the registered notification so the first
// post-deploy tick respects the date the pre-framework code wrote
// — without it Maria's already-sent nudge would re-fire after deploy.
const legacyEnergyBackfillNudgeKey = "energy_backfill_nudge_last_sent"

// Gate thresholds. See the eligibility function below for the
// rationale on each value.
const (
	energyBackfillNudgeMinCompleteDays = 30
	energyBackfillNudgeMaxBackfilled   = 10
)

func init() {
	// Weekly nudge for users who imported Apple Health data but
	// haven't pressed the "Compute historical EnergyBank" button.
	// Without backfill, their per-user verdict bands fall back to
	// cold-start defaults (see storage.energyBandsMinPoints), so
	// Telegram rest/moderate/push_hard recommendations are biased
	// against their actual fitness.
	Register(ProactiveNotification{
		Name:      "energy_backfill",
		Cadence:   7 * 24 * time.Hour,
		HourOfDay: -1, // any morning tick
		LegacyKey: legacyEnergyBackfillNudgeKey,
		Eligible:  energyBackfillNudgeEligible,
		Render:    energyBackfillNudgeRender,
	})
}

func energyBackfillNudgeEligible(ctx context.Context, db *storage.DB, cfg Config) (bool, string) {
	// TZ is required because the backfill button rejects without
	// one (see /api/settings/energy-backfill precondition). Don't
	// nudge a user to press a button that will reject them — the
	// in-app TZ guidance handles that case.
	if cfg.Timezone == "" {
		return false, "no_tz"
	}
	complete, backfilled, err := db.EnergyBackfillCoverage(ctx)
	if err != nil {
		return false, "query_error"
	}
	if complete < energyBackfillNudgeMinCompleteDays {
		// Below this, personal verdict bands wouldn't activate
		// even after backfill — nudging is premature.
		return false, "not_enough_data"
	}
	if backfilled >= energyBackfillNudgeMaxBackfilled {
		// User clearly already pressed the button. Live ingest writes
		// ~12 snapshots/day too, but those aren't flagged `backfilled`
		// — so >=10 here means a real retrospective run happened.
		return false, "already_backfilled"
	}
	return true, ""
}

func energyBackfillNudgeRender(ctx context.Context, db *storage.DB, cfg Config, baseURL string) (string, error) {
	// Re-query here rather than threading the counts from
	// Eligible() — the framework calls Eligible+Render back-to-back
	// inside one ctx, so the extra query is microseconds and the
	// signature simplification is worth it. If we ever needed
	// Eligible→Render value passing we'd evolve the framework, not
	// hack this one notification.
	complete, backfilled, err := db.EnergyBackfillCoverage(ctx)
	if err != nil {
		return "", err
	}
	return formatEnergyBackfillNudge(complete, backfilled, cfg.Lang, baseURL), nil
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
