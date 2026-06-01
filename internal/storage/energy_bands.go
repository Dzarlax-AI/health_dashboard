package storage

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"health-receiver/internal/health"
)

// energyBandsMinPoints is the minimum number of distinct eligible local dates
// required before personal percentile bands replace the cold-start
// defaults. Below this the percentile estimates are too noisy to
// produce stable verdict assignments — a single outlier day shifts a
// quartile by 10+ points on N=15, and that flapping is worse UX than
// running on slightly-off defaults.
//
// 30 days chosen as the inflection point where bootstrap-derived
// confidence intervals on the p20/p50/p80 estimates tighten to ±3
// (acceptable: a verdict band moving ±3 doesn't flip many days
// between buckets). Below ~20 the CI widens to ±8, well into the
// flapping regime.
const energyBandsMinPoints = 30

// energyBandsProvisionalMinPoints is the lower confidence gate for an
// explicitly marked warmup mode. It exists only to avoid overly
// permissive cold-start defaults for tenants with enough compatible
// history to be directionally useful, but not enough for mature
// personal calibration.
const energyBandsProvisionalMinPoints = 20

// energyBandsWindowDays is the rolling window over which percentiles
// are computed. 180 days balances two concerns: long enough to span
// seasonal variation in activity (winter sedentary, summer active),
// short enough that thresholds reflect the user's *current* fitness
// rather than what they could do a year ago.
const energyBandsWindowDays = 180

// ComputeUserVerdictBands returns per-tenant calibrated verdict
// thresholds derived from the user's own energy_snapshots
// distribution. Falls back to DefaultV2VerdictBands when the tenant
// hasn't accumulated enough distinct eligible local dates yet.
//
// Inputs filter:
//   - last `energyBandsWindowDays` days
//   - exclude rows flagged 'imputed_sleep' or 'imputed_activity' — these
//     contain trailing-average substitutions, not real measurements,
//     and would bias the percentiles toward the mean
//   - exclude 'bootstrap_tail' rows from the early iteration warmup
//
// No manual override path here (yet) — admin override via settings
// table is a separate code path the caller (briefing.go) checks
// before invoking this. Keeping ComputeUserVerdictBands pure means
// the tests don't need a settings mock.
func (s *DB) ComputeUserVerdictBands(ctx context.Context) (health.VerdictBands, error) {
	cfg := s.GetEnergyConfig()
	compatibleVersions := compatibleEnergyBandFormulaVersions(cfg)
	calibrationCutoff, err := s.energyBandCalibrationCutoff(ctx, cfg)
	if err != nil {
		return health.VerdictBands{}, err
	}

	latest, err := s.computeVerdictBandsForVersions(ctx, []int{cfg.FormulaVersion}, calibrationCutoff)
	if err != nil {
		return health.VerdictBands{}, err
	}
	compatible, err := s.computeVerdictBandsForVersions(ctx, mapKeys(compatibleVersions), calibrationCutoff)
	if err != nil {
		return health.VerdictBands{}, err
	}

	if latest.n >= energyBandsMinPoints && latest.complete() {
		return verdictBandsFromSample(latest, "personal_latest_formula", latest.n, compatible.n), nil
	}
	if compatible.n >= energyBandsMinPoints && compatible.complete() {
		return verdictBandsFromSample(compatible, "personal_mixed_formula_warmup", latest.n, compatible.n), nil
	}
	if compatible.n >= energyBandsProvisionalMinPoints && compatible.complete() {
		if bands, ok := provisionalVerdictBandsFromSample(compatible, latest.n, compatible.n); ok {
			return bands, nil
		}
	}

	def := health.DefaultV2VerdictBands()
	def.CalibrationMode = "default_warmup"
	def.NDataPoints = 0
	def.UsedDays = 0
	def.LatestFormulaDays = latest.n
	def.CompatibleFormulaDays = compatible.n
	return def, nil
}

type verdictBandSample struct {
	p20 *float64
	p50 *float64
	p80 *float64
	n   int
}

func (s *DB) computeVerdictBandsForVersions(ctx context.Context, versions []int, calibrationCutoff *time.Time) (verdictBandSample, error) {
	if len(versions) == 0 {
		return verdictBandSample{}, nil
	}

	var p20, p50, p80 *float64
	var n int
	placeholders := make([]string, len(versions))
	args := make([]any, 0, len(versions)+1)
	args = append(args, energyBandsWindowDays)
	for i, version := range versions {
		placeholders[i] = fmt.Sprintf("$%d", i+2)
		args = append(args, version)
	}
	cutoffClause := ""
	if calibrationCutoff != nil {
		args = append(args, *calibrationCutoff)
		cutoffClause = fmt.Sprintf("AND computed_at >= $%d", len(args))
	}

	// Percentiles run against the *display* bank (clamped to [0, 100])
	// — the same scale ChooseVerdictV2 consumes. The underlying column
	// stores signed bank [-50, 100] (per ENERGY_BANK.md, kept signed
	// so the AI prompt can frame a sustained deficit). Computing
	// percentiles from raw signed values would let p20 slide negative
	// for users with frequent deficit days, making the bank-rest
	// cutoff (`display <= bands.Rest`) unreachable from the display
	// scale: a clamped display value in [0, 100] can never satisfy
	// `<= -5`. Clamp here so band thresholds and the consumer scale
	// agree end-to-end.
	query := fmt.Sprintf(`
		WITH eligible AS (
			SELECT
				date,
				LEAST(GREATEST(bank, 0), 100) AS bank,
				ROW_NUMBER() OVER (
					PARTITION BY date
					ORDER BY ts_bucket DESC, computed_at DESC, formula_version DESC, bank DESC
				) AS rn
			FROM energy_snapshots
			WHERE date >= (CURRENT_DATE - make_interval(days => $1))::text
			  AND NOT ('imputed_sleep' = ANY(flags))
			  AND NOT ('imputed_activity' = ANY(flags))
			  AND NOT ('bootstrap_tail' = ANY(flags))
			  AND formula_version IN (%s)
			  %s
		),
		per_day AS (
			SELECT bank
			FROM eligible
			WHERE rn = 1
		)
		SELECT
			percentile_cont(0.20) WITHIN GROUP (ORDER BY bank),
			percentile_cont(0.50) WITHIN GROUP (ORDER BY bank),
			percentile_cont(0.80) WITHIN GROUP (ORDER BY bank),
			COUNT(*)
		FROM per_day`, strings.Join(placeholders, ", "), cutoffClause)

	err := s.pool.QueryRow(ctx, query, args...).Scan(&p20, &p50, &p80, &n)
	if err != nil {
		return verdictBandSample{}, err
	}
	return verdictBandSample{p20: p20, p50: p50, p80: p80, n: n}, nil
}

func (s *DB) energyBandCalibrationCutoff(ctx context.Context, cfg EnergyConfig) (*time.Time, error) {
	defaultCfg := DefaultEnergyConfig()
	if cfg.EffectiveBeta() == defaultCfg.EffectiveBeta() &&
		cfg.EffectiveAlpha() == defaultCfg.EffectiveAlpha() {
		return nil, nil
	}

	// Exact coefficient comparison is intentional: personal bands are
	// compatible only with snapshots computed under the same distribution-
	// affecting settings. When those settings change without a formula
	// version bump, fall back to snapshots computed after the last change.
	var cutoff *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT MAX(updated_at::timestamptz)
		FROM settings
		WHERE key = ANY($1)`, []string{
		"energy.alpha",
		"energy.alpha_factor",
		"energy.beta",
		"energy.stress_drain_enabled",
	}).Scan(&cutoff)
	if err != nil {
		return nil, err
	}
	return cutoff, nil
}

func compatibleEnergyBandFormulaVersions(cfg EnergyConfig) map[int]bool {
	versions := map[int]bool{
		cfg.FormulaVersion: true,
	}
	defaultCfg := DefaultEnergyConfig()
	// Exact equality is intentional: v1/v2 warmup is allowed only for the
	// documented default-compatible coefficient set, not "close enough"
	// tuning changes that may shift the bank distribution.
	if cfg.FormulaVersion == 2 &&
		!cfg.StressDrainEnabled &&
		cfg.EffectiveBeta() == 0 &&
		cfg.EffectiveAlpha() == defaultCfg.EffectiveAlpha() {
		versions[1] = true
	}
	return versions
}

func verdictBandsFromSample(sample verdictBandSample, mode string, latestDays, compatibleDays int) health.VerdictBands {
	return health.VerdictBands{
		Rest:                  int(*sample.p20),
		Recovery:              int(*sample.p50),
		PushHard:              int(*sample.p80),
		Source:                "personal",
		NDataPoints:           sample.n,
		CalibrationMode:       mode,
		UsedDays:              sample.n,
		LatestFormulaDays:     latestDays,
		CompatibleFormulaDays: compatibleDays,
	}
}

func provisionalVerdictBandsFromSample(sample verdictBandSample, latestDays, compatibleDays int) (health.VerdictBands, bool) {
	bands := verdictBandsFromSample(sample, "provisional_compatible_formula_warmup", latestDays, compatibleDays)
	defaults := health.DefaultV2VerdictBands()
	bands.Rest = maxInt(bands.Rest, defaults.Rest)
	bands.Recovery = maxInt(bands.Recovery, defaults.Recovery)
	bands.PushHard = maxInt(bands.PushHard, defaults.PushHard)
	if bands.Rest > bands.Recovery || bands.Recovery >= bands.PushHard {
		return health.VerdictBands{}, false
	}
	return bands, true
}

func (s verdictBandSample) complete() bool {
	return s.p20 != nil && s.p50 != nil && s.p80 != nil
}

func mapKeys(m map[int]bool) []int {
	keys := make([]int, 0, len(m))
	for k, ok := range m {
		if ok {
			keys = append(keys, k)
		}
	}
	sort.Ints(keys)
	return keys
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
