package storage

import (
	"context"

	"health-receiver/internal/health"
)

// energyBandsMinPoints is the minimum number of non-imputed snapshots
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

// energyBandsWindowDays is the rolling window over which percentiles
// are computed. 180 days balances two concerns: long enough to span
// seasonal variation in activity (winter sedentary, summer active),
// short enough that thresholds reflect the user's *current* fitness
// rather than what they could do a year ago.
const energyBandsWindowDays = 180

// ComputeUserVerdictBands returns per-tenant calibrated verdict
// thresholds derived from the user's own energy_snapshots
// distribution. Falls back to DefaultV2VerdictBands when the tenant
// hasn't accumulated enough non-imputed days yet.
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
	var p20, p50, p80 *float64
	var n int
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
	err := s.pool.QueryRow(ctx, `
		WITH eligible AS (
			SELECT LEAST(GREATEST(bank, 0), 100) AS bank
			FROM energy_snapshots
			WHERE date >= (CURRENT_DATE - INTERVAL '180 days')::text
			  AND NOT ('imputed_sleep' = ANY(flags))
			  AND NOT ('imputed_activity' = ANY(flags))
			  AND NOT ('bootstrap_tail' = ANY(flags))
		)
		SELECT
			percentile_cont(0.20) WITHIN GROUP (ORDER BY bank),
			percentile_cont(0.50) WITHIN GROUP (ORDER BY bank),
			percentile_cont(0.80) WITHIN GROUP (ORDER BY bank),
			COUNT(*)
		FROM eligible`).Scan(&p20, &p50, &p80, &n)
	if err != nil {
		return health.VerdictBands{}, err
	}
	if n < energyBandsMinPoints || p20 == nil || p50 == nil || p80 == nil {
		def := health.DefaultV2VerdictBands()
		def.NDataPoints = n
		return def, nil
	}
	return health.VerdictBands{
		Rest:        int(*p20),
		Recovery:    int(*p50),
		PushHard:    int(*p80),
		Source:      "personal",
		NDataPoints: n,
	}, nil
}
