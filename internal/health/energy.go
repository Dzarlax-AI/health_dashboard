package health

import (
	"fmt"
	"math"
)

// Energy Bank — Bevel-inspired prescriptive metric.
//
// Capacity is the user's "energy budget" at the start of the day, drawn from
// the same readiness signal that already aggregates sleep + autonomic markers.
// Throughout the day the budget drains in proportion to:
//   • observed activity load (ACWR-style ratio of today vs 28-day chronic
//     baseline — Gabbett 2016 [[36]]),
//   • autonomic stress (one-sided z-scores of RHR and HRV against personal
//     baseline — Plews 2014 [[18]], Beattie 2024 [[6]]).
//
// The drain is non-linear: stress *amplifies* the cost of strain (allostatic
// load model — McEwen 1998 [[38]]). The same active day costs more when
// autonomics are already tax-loaded.
//
// The action verdict gates primarily on the raw HRV z-score against a ±0.5 SD
// "smallest worthwhile change" band — the standard threshold in HRV-guided
// training prescription (Plews 2014 [[18]], Vesterinen 2016 [[37]],
// Düking 2023 [[39]]). The current bank balance acts as a secondary clamp so
// a "feel-fine HRV but already moved a lot today" pattern still gets the right
// downgrade.
//
// Coherence: when the cross-metric Headline (see headline.go) flags `stress`,
// `push_hard` is forbidden — the multi-signal converging-evidence rule
// (Meeusen 2013 [[16]]) is stronger than any single-marker green light.

const (
	// Strain weights — both channels weighted equally; capping at ratio=1.0
	// (today equals chronic) maps to strain=100 (a maximally typical day's
	// expenditure). Above-1.0 days saturate the score, but the verdict logic
	// still picks them up via the `current ≤ 25` rest threshold.
	strainStepWeight   = 0.5
	strainEnergyWeight = 0.5

	// Stress amplifier — when stress=100, drain costs 1.5× the strain
	// (allostatic load multiplier). Conservative; literature supports
	// 1.3–2.0× depending on cohort.
	stressDrainMultiplier = 0.5

	// Plews 2014 / Vesterinen 2016 prescription thresholds.
	hrvZUpperBand = 0.5  // ≥ +0.5 SD → green light for high intensity
	hrvZLowerBand = -0.5 // ≤ -0.5 SD → drop intensity
	hrvZRestBand  = -1.0 // ≤ -1.0 SD → rest day

	// Bank-balance clamp thresholds (secondary gate).
	currentRestCutoff       = 25 // ≤ this → rest regardless of HRV
	currentRecoveryCutoff   = 45 // ≤ this → at most active recovery
	currentPushHardMin      = 60 // need this and HRV green to push hard
)

// computeEnergyBank produces the day's prescriptive verdict. Returns nil when
// the underlying readiness/sleep data is too thin — same gate as the readiness
// score itself, keeps the briefing JSON clean for new accounts.
//
// `readinessScore` is the score already computed by computeReadiness — it
// already blends sleep + HRV + RHR with the U-curve penalty, so we re-use it
// as morning capacity rather than re-deriving an analogous number here.
//
// `headline` is consulted for the stress-coherence rule and may be nil.
func computeEnergyBank(d RawMetrics, readinessScore int, headline *HeadlineSignal, ls LangStrings) *EnergyBank {
	if len(d.HRV) < minBaseline+2 || len(d.Sleep) < minBaseline+2 {
		return nil
	}
	if readinessScore <= 0 || d.Sleep[0] <= 0 {
		return nil
	}

	capacity := readinessScore
	strain, strainNote := computeStrain(d)
	stress, stressNote, hrvZRaw := computeAutonomicStress(d)

	// Allostatic load: drain = strain × (1 + α·stress)
	drain := float64(strain) * (1.0 + stressDrainMultiplier*float64(stress)/100.0)
	current := clampScore(float64(capacity) - drain)
	drainSoFar := clampScore(drain)

	verdict := chooseVerdict(hrvZRaw, current)
	if headline != nil && headline.Key == "stress" && verdict == "push_hard" {
		// Multi-signal stress vetoes any single-metric green light.
		verdict = "active_recovery"
	}

	reason := buildVerdictReason(verdict, current, capacity, strain, stress, hrvZRaw, ls)

	return &EnergyBank{
		Capacity:      capacity,
		Current:       current,
		DrainSoFar:    drainSoFar,
		Strain:        strain,
		Stress:        stress,
		ActionVerdict: verdict,
		VerdictReason: reason,
		Components: []EnergyBankComponent{
			{Name: "morning_capacity", Value: capacity,
				Note: fmt.Sprintf(ls["energy_note_capacity"], d.Sleep[0])},
			{Name: "activity_load", Value: strain, Note: strainNote},
			{Name: "autonomic_stress", Value: stress, Note: stressNote},
		},
	}
}

// computeStrain returns 0..100 ACWR-flavoured load score plus a human note.
// Falls back gracefully when chronic baseline or today's partial sums are
// missing (early morning before any sync, or new accounts).
func computeStrain(d RawMetrics) (score int, note string) {
	stepRatio := 0.0
	if d.StepsChronic28d > 1 {
		stepRatio = d.StepsToday / d.StepsChronic28d
	}
	energyRatio := 0.0
	if d.ActiveEnergyChronic28d > 1 {
		energyRatio = d.ActiveEnergyToday / d.ActiveEnergyChronic28d
	}
	combined := strainStepWeight*stepRatio + strainEnergyWeight*energyRatio
	score = clampScore(clamp01(combined) * 100)
	note = fmt.Sprintf("steps %.0f vs 28d avg %.0f, kcal %.0f vs %.0f",
		d.StepsToday, d.StepsChronic28d, d.ActiveEnergyToday, d.ActiveEnergyChronic28d)
	return score, note
}

// computeAutonomicStress returns 0..100 stress score (only the BAD direction
// of each axis matters), human note, and the *raw* HRV z-score (used by the
// verdict logic below — the verdict needs sign, the score doesn't).
func computeAutonomicStress(d RawMetrics) (score int, note string, hrvZRaw float64) {
	rhrToday := d.RHR[0]
	hrvToday := d.HRV[0]
	rhrBase := avg(safeSlice(d.RHR, 7, len(d.RHR)))
	hrvBase := avg(safeSlice(d.HRV, 7, len(d.HRV)))
	rhrSD := stddev(safeSlice(d.RHR, 7, len(d.RHR)))
	hrvSD := stddev(safeSlice(d.HRV, 7, len(d.HRV)))

	rhrZRaw := zScore(rhrToday, rhrBase, rhrSD)
	hrvZRaw = zScore(hrvToday, hrvBase, hrvSD)

	// Only positive RHR deviation and negative HRV deviation indicate stress.
	rhrStress := math.Max(0, rhrZRaw)
	hrvStress := math.Max(0, -hrvZRaw)
	combined := (rhrStress + hrvStress) / 2 // ~0 at z=0, ~0.5 at z=1, ~1.0 at z=2
	score = clampScore(clamp01(combined) * 100)

	note = fmt.Sprintf("HRV %.0fms (%+.1f SD), RHR %.0fbpm (%+.1f bpm)",
		hrvToday, hrvZRaw, rhrToday, rhrToday-rhrBase)
	return score, note, hrvZRaw
}

func chooseVerdict(hrvZRaw float64, current int) string {
	switch {
	case hrvZRaw <= hrvZRestBand || current <= currentRestCutoff:
		return "rest"
	case hrvZRaw <= hrvZLowerBand || current <= currentRecoveryCutoff:
		return "active_recovery"
	case hrvZRaw >= hrvZUpperBand && current >= currentPushHardMin:
		return "push_hard"
	default:
		return "moderate"
	}
}

// buildVerdictReason picks the i18n template that best explains *why* the
// verdict came out the way it did, prioritising the most-actionable signal.
func buildVerdictReason(
	verdict string, current, capacity, strain, stress int, hrvZRaw float64,
	ls LangStrings,
) string {
	switch verdict {
	case "push_hard":
		return fmt.Sprintf(ls["energy_reason_full_capacity"], current)
	case "rest":
		if hrvZRaw <= hrvZRestBand {
			return fmt.Sprintf(ls["energy_reason_high_stress"], hrvZRaw, stress)
		}
		return fmt.Sprintf(ls["energy_reason_low_capacity"], current)
	case "active_recovery":
		if stress >= 50 {
			return fmt.Sprintf(ls["energy_reason_high_stress"], hrvZRaw, stress)
		}
		if strain >= 80 {
			return fmt.Sprintf(ls["energy_reason_acwr_spike"], float64(strain))
		}
		return fmt.Sprintf(ls["energy_reason_low_capacity"], current)
	default: // moderate
		return fmt.Sprintf(ls["energy_reason_optimal"], current)
	}
}
