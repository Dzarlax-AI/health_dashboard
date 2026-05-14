package health

// Stress v2.2 — verdict-layer overrides.
//
// STRESS_MEASUREMENT.md §4.3 routes the three multi-channel flags
// (illness_signature, recovery_debt, parasympathetic_rebound) to the
// verdict layer, NOT the drain math. This file applies them after
// ChooseVerdictV2 / BuildVerdictReasonV2 ran on the underlying
// physiology so the v2 bank gate still decides "normal" days while
// flagged days route to clinically appropriate guidance.
//
// Authority order (most-specific wins):
//   illness_signature       → force "rest", override reason text
//   recovery_debt           → downgrade push_hard → active_recovery,
//                             override reason text
//   parasympathetic_rebound → keep verdict, ENRICH reason text only
//                             (per spec: interpretation flag, not a
//                             correction term)
//
// Multiple flags can co-occur; the override order above is intentional
// — illness_signature is the safety-critical case ("user is fighting
// infection; AI must not say push_hard") and pre-empts the others.

import "strings"

// flagSet is a tiny helper — avoids repeated linear scans of
// `flags` while keeping the API as a plain []string for JSON / DB
// round-trips.
type flagSet map[string]struct{}

func newFlagSet(flags []string) flagSet {
	s := make(flagSet, len(flags))
	for _, f := range flags {
		s[f] = struct{}{}
	}
	return s
}

func (s flagSet) has(name string) bool {
	_, ok := s[name]
	return ok
}

// ApplyStressFlagVerdictOverride post-processes the v2 verdict +
// reason pair against the §4.3 multi-channel stress flags. Returns
// the (possibly modified) verdict + reason and `overridden=true` when
// any flag changed the output, so callers can persist an audit line.
//
// `flags` is the raw `stress_flags` array as written by
// ComputeSustainedHRLoadForDate. Only the multi-channel subset is
// consulted here — acute_stress / sustained_load already shaped the
// physiology via sustained_hr_load_z and don't need a second layer.
//
// Pure function — testable without DB. i18n strings come from `ls`.
func ApplyStressFlagVerdictOverride(
	verdict, reason string,
	flags []string,
	ls LangStrings,
) (newVerdict, newReason string, overridden bool) {
	set := newFlagSet(flags)

	// illness_signature — safety-critical. Force rest regardless of
	// bank position. The HR rise that fed sustained_hr_load is the
	// infection response; pushing hard while sick degrades recovery
	// AND prolongs the illness. AI prompt layer (PR-9c) reads the
	// same flag and suppresses push_hard recommendations in prose.
	if set.has("illness_signature") {
		return "rest", ls["energy_reason_illness_signature"], true
	}

	// recovery_debt — yesterday's autonomic cost surfaced overnight
	// (HRV down + RHR up). Don't force rest — the bank can still
	// allow moderate movement — but never green-light push_hard.
	// Downgrade to active_recovery; leave anything ≤ active_recovery
	// alone (rest stays rest).
	if set.has("recovery_debt") {
		if verdict == "push_hard" || verdict == "moderate" {
			return "active_recovery", ls["energy_reason_recovery_debt"], true
		}
	}

	// parasympathetic_rebound — interpretation flag. Don't change
	// verdict; append an explanation suffix so the user (and AI)
	// understands the mixed autonomic signal ("HR was up but HRV
	// also above baseline — recovery phase, not acute stress").
	// Suffix instead of replace because the underlying bank-derived
	// reason is still the primary "why".
	if set.has("parasympathetic_rebound") {
		suffix := ls["energy_reason_rebound_addon"]
		// Idempotency guard: never double-append on repeated calls
		// from the briefing pipeline (snapshot reload + recompute).
		if !strings.Contains(reason, suffix) {
			return verdict, strings.TrimSpace(reason) + " " + suffix, true
		}
	}

	return verdict, reason, false
}

// AISuppressPushHard returns true when the current flag set
// physiologically contraindicates a "push_hard" recommendation.
// The AI prompt-layer (internal/ai/blocks.go RECOMMENDATION block)
// reads this to inject a "user shows illness / recovery debt — do
// NOT recommend high-intensity training" instruction. Verdict
// override above already covers the dashboard / Telegram surface;
// this guard exists because the AI prose path can drift even when
// the verdict label says active_recovery (model "knows better" and
// suggests a hard session anyway).
func AISuppressPushHard(flags []string) bool {
	set := newFlagSet(flags)
	return set.has("illness_signature") || set.has("recovery_debt")
}
