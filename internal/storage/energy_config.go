package storage

import "strconv"

// EnergyConfig holds the formula coefficients and personalisation state
// for EnergyBank v2 (and the v2.2 stress-drain extension). Values are
// read lazily from the settings table on every call (matching the
// AIConfig / NotifyConfig pattern); when a key is missing or
// unparseable, the documented default is returned. No pre-seeding into
// the settings table — defaults live in code so a schema with no rows
// still produces correct behaviour, and a missing key cannot diverge
// from its default after a deploy.
//
// Field meanings track ENERGY_BANK.md and STRESS_MEASUREMENT.md:
//   - Alpha: v2.0 calorie drain coefficient (drain ≈ Alpha · active_kcal)
//   - Beta:  v2.2 HR-stress drain coefficient. Multiplied by
//     `sustained_hr_load` z-units to form the autonomic-load drain term.
//     Default 0.8 is the §6 Q3 NON-PRODUCTION sanity anchor — only
//     applied when StressDrainEnabled=true (see EffectiveBeta).
//   - ZThreshold: v2.2 per-hour z-score threshold above which hours
//     contribute to `sustained_hr_load`. Default 0.5 per §4.4; tunable
//     to 0.7-0.8 for desk-heavy lifestyle profiles (§4.4 risk note).
//   - StressDrainEnabled: master gate for the v2.2 β term. Until the
//     §4.5 four-channel validation rubric returns `validated` for the
//     tenant, this stays false and EffectiveBeta returns 0 regardless
//     of Beta. The sustained_hr_load_z input is still computed and
//     written to components JSONB for audit while disabled (PR-8).
//   - AlphaFactor: v2.5 per-user multiplier; Alpha · AlphaFactor is the
//     effective coefficient applied to drain
//   - AlphaFactorSource: provenance for AlphaFactor — "default", "auto"
//     (calibrator-tuned), or "manual" (v3.0 override). Authority order
//     is manual > auto > default; the auto-calibrator skips its run when
//     source = "manual"
//   - FormulaVersion: bumped manually when constants change. Stamped on
//     every snapshot row so backfill never overwrites snapshots from a
//     different formula and week-over-week comparisons stay valid within
//     a version
type EnergyConfig struct {
	Alpha              float64
	Beta               float64
	ZThreshold         float64
	StressDrainEnabled bool
	AlphaFactor        float64
	AlphaFactorSource  string
	FormulaVersion     int
}

// EffectiveAlpha returns the personalised drain coefficient: Alpha
// scaled by AlphaFactor. Callers that want the unscaled base should
// read Alpha directly.
func (c EnergyConfig) EffectiveAlpha() float64 {
	return c.Alpha * c.AlphaFactor
}

// EffectiveBeta returns the live stress-drain coefficient applied by
// DrainV2. Returns 0 when StressDrainEnabled=false regardless of the
// stored Beta — the two-key design lets us pre-tune Beta and flip
// the master switch in one atomic step after the §4.5 validation
// rubric returns `validated` for the tenant, without leaving Beta
// briefly applied with no audit trail.
//
// Per STRESS_MEASUREMENT.md §6 Q3:
//
//	"Production β_effective remains 0 until the §4.5 four-channel
//	 validation rubric returns `validated` for this user. Until then,
//	 ship with settings.energy.stress_drain_enabled = false: compute
//	 sustained_hr_load_z into `components` for audit, but β_effective
//	 = 0 and the bank does not move on the new term."
func (c EnergyConfig) EffectiveBeta() float64 {
	if !c.StressDrainEnabled {
		return 0
	}
	return c.Beta
}

// DefaultEnergyConfig is the v2.0-launch starting point. Constants
// validated empirically on 31 days of historical data — see
// ENERGY_BANK.md § Validation. The v2.2 fields use the
// STRESS_MEASUREMENT.md §6 Q3 non-production placeholder values for
// Beta (0.8) and ZThreshold (0.5); StressDrainEnabled stays false
// until the §4.5 validation rubric clears it per tenant.
// Deliberately not exported as a `var` so callers can't mutate it.
func DefaultEnergyConfig() EnergyConfig {
	return EnergyConfig{
		Alpha:              0.08,
		Beta:               0.8,
		ZThreshold:         0.5,
		StressDrainEnabled: false,
		AlphaFactor:        1.0,
		AlphaFactorSource:  "default",
		// FormulaVersion 2 = DrainV2 accepts the v2.2
		// sustained_hr_load term. Bank values are identical to
		// FormulaVersion 1 when StressDrainEnabled=false (β=0 zeroes
		// the new term), so the bump doesn't shift history for
		// tenants who haven't enabled stress drain. The version
		// stamp lets calibration tooling (PR-11) distinguish "this
		// snapshot was computed by a formula that COULD apply β"
		// from "this snapshot pre-dates the v2.2 audit trail".
		FormulaVersion: 2,
	}
}

// GetEnergyConfig reads the EnergyBank v2 configuration from the
// settings table, falling back to DefaultEnergyConfig() for any key
// that is missing or unparseable. Safe to call on a fresh schema.
//
// AlphaFactorSource is whitelisted: a typo or stale value left in the
// settings table cannot silently break the authority order
// (manual > auto > default) the v2.5 calibrator depends on. Anything
// outside the whitelist falls back to the default.
func (s *DB) GetEnergyConfig() EnergyConfig {
	d := DefaultEnergyConfig()
	src := s.GetSetting("energy.alpha_factor_source", d.AlphaFactorSource)
	switch src {
	case "default", "auto", "manual":
	default:
		src = d.AlphaFactorSource
	}
	return EnergyConfig{
		Alpha:              getSettingFloat(s, "energy.alpha", d.Alpha),
		Beta:               getSettingFloat(s, "energy.beta", d.Beta),
		ZThreshold:         getSettingFloat(s, "energy.z_threshold", d.ZThreshold),
		StressDrainEnabled: getSettingBool(s, "energy.stress_drain_enabled", d.StressDrainEnabled),
		AlphaFactor:        getSettingFloat(s, "energy.alpha_factor", d.AlphaFactor),
		AlphaFactorSource:  src,
		FormulaVersion:     getSettingInt(s, "energy.formula_version", d.FormulaVersion),
	}
}

func getSettingFloat(s *DB, key string, fallback float64) float64 {
	v := s.GetSetting(key, "")
	if v == "" {
		return fallback
	}
	if n, err := strconv.ParseFloat(v, 64); err == nil {
		return n
	}
	return fallback
}
