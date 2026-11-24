package storage

import (
	"context"
	"fmt"
	"time"

	dbtmetrics "health-receiver/experiments/dbt_metrics"
	"health-receiver/internal/health"
)

// ComputeUserVerdictBands returns per-tenant calibrated verdict
// thresholds derived from the user's own energy_snapshots
// distribution. Falls back to DefaultV2VerdictBands when the tenant
// hasn't accumulated enough distinct eligible local dates yet.
//
// Inputs filter:
//   - snapshots dated within the 180-day lookback
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
	return s.ComputeUserVerdictBandsThrough(ctx, time.Now().In(s.reportTZLocation()).Format("2006-01-02"))
}

// ComputeUserVerdictBandsThrough calibrates verdict bands from data that was
// dated no later than throughDate in the currently stored history. An
// as-issued explanation comes from its decision-time capture or sent report.
func (s *DB) ComputeUserVerdictBandsThrough(ctx context.Context, throughDate string) (health.VerdictBands, error) {
	if _, err := time.Parse("2006-01-02", throughDate); err != nil {
		return health.VerdictBands{}, fmt.Errorf("invalid energy band cutoff date %q: %w", throughDate, err)
	}
	cfg := s.GetEnergyConfig()
	calibrationCutoff, err := s.energyBandCalibrationCutoff(ctx, cfg)
	if err != nil {
		return health.VerdictBands{}, err
	}
	return withDecisionJournal(ctx, s, func(db *DB, journal bool) (health.VerdictBands, error) {
		var bands health.VerdictBands
		query := energyBandReadSQL
		if journal {
			query += `, decision_packets as (
                select 'energy_bands'::text as metric,$1::text as decision_key,1 as formula_version,
                 jsonb_build_object('through_day',d.through_day,'snapshot_formula',d.formula_version,
                   'alpha',d.alpha_effective,'beta',d.beta_effective,'stress',d.stress_enabled,'settings_changed',d.settings_changed,
                   'samples',(select coalesce(jsonb_agg(jsonb_build_object('sample',sample,'day',day,'bank',bank)
                     order by sample,day),'[]'::jsonb) from samples where rn=1)) as inputs,
                 jsonb_build_object('mode',d.mode,'latest_days',d.latest_days,'compatible_days',d.compatible_days,
                   'used_days',d.used_days,'thresholds',d.thresholds) as result from bands d
            )` + decisionJournalInsertSQL
		}
		err := db.pool.QueryRow(ctx, query+energyBandSelectSQL, throughDate, cfg.FormulaVersion,
			cfg.EffectiveAlpha(), cfg.EffectiveBeta(), cfg.StressDrainEnabled, calibrationCutoff,
		).Scan(&bands.CalibrationMode, &bands.LatestFormulaDays, &bands.CompatibleFormulaDays,
			&bands.UsedDays, &bands.Rest, &bands.Recovery, &bands.PushHard, &bands.Source)
		bands.NDataPoints = bands.UsedDays
		return bands, err
	})
}

var energyBandReadSQL = `with decisions as (
	select ''::text as profile, ''::text as capture_id, $1::text::date as through_day,
	       $2::int as formula_version, $3::float8 as alpha_effective, $4::float8 as beta_effective,
	       $5::bool as stress_enabled, $6::timestamptz as settings_changed
), history as (
	select ''::text as profile, ''::text as capture_id, date as day, ts_bucket, computed_at,
	       formula_version, bank, flags from energy_snapshots
), ` + dbtmetrics.EnergyVerdictBandsSQL("history")

const energyBandSelectSQL = `
select mode,latest_days,compatible_days,used_days,trunc(thresholds[1])::int,
       trunc(thresholds[2])::int,trunc(thresholds[3])::int,
       case when mode='default_warmup' then 'default' else 'personal' end
from bands`

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
