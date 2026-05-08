package storage

import "strconv"

// EnergyConfig holds the formula coefficients and personalisation state
// for EnergyBank v2. Values are read lazily from the settings table on
// every call (matching the AIConfig / NotifyConfig pattern); when a key
// is missing or unparseable, the documented default is returned. No
// pre-seeding into the settings table — defaults live in code so a
// schema with no rows still produces correct behaviour, and a missing
// key cannot diverge from its default after a deploy.
//
// Field meanings track ENERGY_BANK.md:
//   - Alpha: v2.0 calorie drain coefficient (drain ≈ Alpha · active_kcal)
//   - Beta:  v2.2 HR-baseline drain term, reserved (β · max(0, HR−RHR) · duration)
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
	Alpha             float64
	Beta              float64
	AlphaFactor       float64
	AlphaFactorSource string
	FormulaVersion    int
}

// EffectiveAlpha returns the personalised drain coefficient: Alpha
// scaled by AlphaFactor. Callers that want the unscaled base should
// read Alpha directly.
func (c EnergyConfig) EffectiveAlpha() float64 {
	return c.Alpha * c.AlphaFactor
}

// DefaultEnergyConfig is the v2.0-launch starting point. Constants
// validated empirically on 31 days of historical data — see
// ENERGY_BANK.md § Validation. Deliberately not exported as a `var` so
// callers can't mutate it.
func DefaultEnergyConfig() EnergyConfig {
	return EnergyConfig{
		Alpha:             0.08,
		Beta:              0.0,
		AlphaFactor:       1.0,
		AlphaFactorSource: "default",
		FormulaVersion:    1,
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
		Alpha:             getSettingFloat(s, "energy.alpha", d.Alpha),
		Beta:              getSettingFloat(s, "energy.beta", d.Beta),
		AlphaFactor:       getSettingFloat(s, "energy.alpha_factor", d.AlphaFactor),
		AlphaFactorSource: src,
		FormulaVersion:    getSettingInt(s, "energy.formula_version", d.FormulaVersion),
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
