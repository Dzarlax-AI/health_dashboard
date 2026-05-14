package notify

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

// stressValidationLastVerdictKey is the settings-table key carrying
// the most recent §4.5 verdict so the proactive rule fires only on
// *transitions* into "validated" — not every monthly tick.
//
// Stored verdicts: "" (never run) | "inconclusive" | "weak" |
// "wrong_direction" | "validated". The framework-managed
// `proactive_stress_validation_validated_last_sent` key handles
// cadence separately.
const stressValidationLastVerdictKey = "stress_validation_last_verdict"

func init() {
	// Monthly recompute of the §4.5 rubric. Fires a Telegram nudge
	// ONLY when the verdict newly transitions to "validated" — that's
	// the actionable signal (operator may now flip
	// settings.energy.stress_drain_enabled). Other verdicts (weak /
	// inconclusive / wrong_direction) stay silent; the operator can
	// click /admin → "Run validation" anytime to see the current
	// state.
	//
	// Cadence 30d keeps the framework-managed last_sent honest: even
	// if the rubric flips validated → weak → validated within a
	// week, we won't spam. After the first validated-transition
	// notify, the next earliest re-fire is 30 days later, by which
	// point the rolling 30-day Pearson windows have fully refreshed.
	Register(ProactiveNotification{
		Name:      "stress_validation_validated",
		Cadence:   30 * 24 * time.Hour,
		HourOfDay: -1,
		Eligible:  stressValidationNudgeEligible,
		Render:    stressValidationNudgeRender,
	})
}

func stressValidationNudgeEligible(
	ctx context.Context,
	db *storage.DB,
	cfg Config,
) (bool, string) {
	if cfg.Timezone == "" {
		// Endpoint accepts empty TZ (falls back to UTC) but a
		// tenant without TZ probably hasn't completed onboarding —
		// no point nudging.
		return false, "no_tz"
	}
	report, err := db.ComputeStressValidationReport(ctx, cfg.Timezone, "", 30)
	if err != nil {
		return false, "compute_error"
	}
	last := db.GetSetting(stressValidationLastVerdictKey, "")

	// Always update the stored verdict so the next monthly tick
	// knows the current baseline. Done inside Eligible because the
	// framework gives Render only the cadence/last-sent state;
	// persisting the verdict here is the only place that has the
	// report in hand.
	if report.Verdict != "" && report.Verdict != last {
		if err := db.SaveSettings(map[string]string{
			stressValidationLastVerdictKey: report.Verdict,
		}); err != nil {
			log.Printf("[stress_validation_nudge] persist verdict: %v", err)
			// Fall through — we'll just re-evaluate on the next
			// tick if the write didn't land.
		}
	}

	// Fire only on a fresh transition INTO "validated". Other
	// transitions update the stored state above but stay silent.
	if report.Verdict != "validated" {
		return false, "not_validated"
	}
	if last == "validated" {
		return false, "already_validated"
	}
	return true, ""
}

func stressValidationNudgeRender(
	ctx context.Context,
	db *storage.DB,
	cfg Config,
	baseURL string,
) (string, error) {
	// Re-query — same pattern as energy_backfill_nudge. Render
	// runs back-to-back with Eligible so the extra microseconds are
	// cheap and the signature stays clean.
	report, err := db.ComputeStressValidationReport(ctx, cfg.Timezone, "", 30)
	if err != nil {
		return "", err
	}
	if report.Verdict != "validated" {
		// Defensive: if the verdict changed between Eligible and
		// Render (unlikely — same ctx, sub-second gap), skip
		// rather than send a wrong claim. Empty return is
		// idiomatic "skip" per the framework.
		return "", nil
	}
	return formatStressValidationNudge(report, cfg.Lang, baseURL), nil
}

func formatStressValidationNudge(
	report health.ValidationReport,
	lang, baseURL string,
) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>%s</b>\n\n", tr(lang, "tg_stress_validation_header"))

	body := tr(lang, "tg_stress_validation_body")
	body = strings.ReplaceAll(body, "{verdict}", report.Verdict)
	body = strings.ReplaceAll(body, "{reason}", report.Reason)
	sb.WriteString(body)
	sb.WriteString("\n\n")

	link := strings.TrimSuffix(baseURL, "/") + "/admin"
	fmt.Fprintf(&sb, `<a href="%s">%s</a>`, link, tr(lang, "tg_stress_validation_cta"))
	return sb.String()
}
