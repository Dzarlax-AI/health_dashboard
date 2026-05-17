// Read-only summaries powering the readiness-redesign onboarding
// wizard (plan §6.2 runbook → admin UI). Every function in this
// file is **read-only** and idempotent — the wizard recomputes a
// step's state on every render, so an operator can close the page
// and come back; the wizard is stateless and re-derives "done /
// pending / warning" purely from the database. No cookies, no
// session progress flags.
//
// Mutating actions for the wizard's two write steps (Step 4 backfill
// and Step 6 recompute) reuse the existing endpoints; this file
// only adds the read-side helpers needed to decide whether each
// step is satisfied yet.

package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"health-receiver/internal/health"
)

// OnboardingSubScoreRowCounts is one row per sub_score in the
// target_snapshots / naive_baselines / feature_snapshots tables.
// Used by Step 1 (tenant check) and Step 5 (verify) to confirm a
// backfill produced output and by Step 3 (coverage preview) to
// gate whether enough history exists to run calibration.
type OnboardingSubScoreRowCounts struct {
	SubScore           string  `json:"sub_score"`
	TargetSnapshots    int     `json:"target_snapshots"`
	EligibleTargets    int     `json:"eligible_targets"`
	NaiveBaselines     int     `json:"naive_baselines"`
	BaselinesWithValue int     `json:"baselines_with_value"`
	FeatureSnapshots   int     `json:"feature_snapshots"`
	LatestDate         *string `json:"latest_date,omitempty"`
}

// OnboardingTenantStatus is the Step 1 / Step 5 payload — pure
// "did the backfill actually run, and is the schema intact" check.
// The wizard renders the row counts as a small table and surfaces
// `schema_healthy=false` as a hard block.
type OnboardingTenantStatus struct {
	Schema             string                        `json:"schema"`
	SchemaHealthy      bool                          `json:"schema_healthy"`
	SchemaError        string                        `json:"schema_error,omitempty"`
	SubScoreCounts     []OnboardingSubScoreRowCounts `json:"sub_score_counts"`
	UnknownEpochRows   int                           `json:"unknown_epoch_rows"`
	ActiveEpochID      string                        `json:"active_epoch_id"`
	ActiveEpochStart   string                        `json:"active_epoch_start"`
}

// ChronicThresholdEcho is the per-row audit pulled from a sampled
// chronic target's `data_coverage` JSON. The chronic writer echoes
// the thresholds it actually used onto every row it produces (since
// §6.2). Comparing this echo to the effective `ChronicLoadConfig`
// is the load-bearing verification step — a silent miscalibration
// "operator overrode settings but the writer used defaults" only
// surfaces here.
type ChronicThresholdEcho struct {
	SampledDate         string `json:"sampled_date"`
	SampledTargetKind   string `json:"sampled_target_kind"`
	BreachThreshold     int    `json:"breach_threshold"`
	AcuteDensityThresh  int    `json:"acute_density_threshold"`
}

// MatchesConfig reports whether the threshold echo agrees with the
// effective ChronicLoadConfig. Wizard Step 5 surfaces a mismatch as
// a hard warning.
func (e *ChronicThresholdEcho) MatchesConfig(cfg health.ChronicLoadConfig) bool {
	if e == nil {
		return false
	}
	return e.BreachThreshold == cfg.MinBreachDays && e.AcuteDensityThresh == cfg.MinAcuteDensity
}

// OnboardingCoverageSummary is the Step 3 payload — minimum slice
// of data the operator needs to decide whether `chronic_load`
// thresholds should be retuned before backfilling. Acute OR base
// rate is the load-bearing number per the v2 retune (PR #97) and
// the §6.2 runbook.
type OnboardingCoverageSummary struct {
	Schema           string  `json:"schema"`
	AcuteEligibleN   int     `json:"acute_eligible_n"`
	AcuteORBaseRate  *float64 `json:"acute_or_base_rate,omitempty"`
	ChronicEligibleN int     `json:"chronic_eligible_n"`
}

// onboardingSubScores enumerates the sub_scores whose row counts the
// wizard renders. Order matches the chip render order so Step 1
// reads like the dashboard pivot row.
var onboardingSubScores = []string{
	SubScoreRecoveryStability,
	SubScorePassiveEfficiency,
	SubScoreChronicLoad,
	SubScoreAcuteRisk,
}

// LoadOnboardingTenantStatus computes Step 1 / Step 5 state. Returns
// row counts per sub_score across the three Phase 0 tables plus the
// schema-health probe and active source_epoch. Read-only.
//
// All counts are derived in a single SQL pass per table so the
// wizard's polling cost is bounded — three queries total, regardless
// of how many sub_scores grow over time.
func (s *DB) LoadOnboardingTenantStatus(asOfDate string) (*OnboardingTenantStatus, error) {
	if _, err := time.Parse(isoDate, asOfDate); err != nil {
		return nil, fmt.Errorf("LoadOnboardingTenantStatus: parse asOfDate %q: %w", asOfDate, err)
	}
	out := &OnboardingTenantStatus{SchemaHealthy: true}
	if err := s.VerifyReadinessRedesignSchema(); err != nil {
		// Schema is unhealthy or missing — every downstream query
		// (row counts, sentinel-epoch probe) reads tables that may
		// not exist, so we'd flip a soft "schema_healthy=false"
		// payload into a 500. Return the partial status now and let
		// Step 1 render the schema_error block as a hard block.
		out.SchemaHealthy = false
		out.SchemaError = err.Error()
		return out, nil
	}

	// Resolve today's epoch — Step 1 surfaces "no active epoch" as a
	// hard warning because every downstream step needs one.
	epoch, err := s.ResolveSourceEpoch(asOfDate)
	if err != nil {
		return nil, fmt.Errorf("LoadOnboardingTenantStatus: resolve epoch: %w", err)
	}
	out.ActiveEpochID = epoch
	if epoch != "" && epoch != SentinelSourceEpoch {
		out.ActiveEpochStart = s.lookupEpochStart(epoch)
	}

	counts, err := s.loadOnboardingRowCounts()
	if err != nil {
		return nil, err
	}
	out.SubScoreCounts = counts

	// Count rows tagged with the sentinel epoch — anything > 0 is a
	// real bug (resolver fell through to unknown) and the wizard
	// must flag it before the operator goes further.
	ctx, cancel := queryCtx()
	defer cancel()
	if err := s.pool.QueryRow(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM target_snapshots WHERE source_epoch = $1) +
		  (SELECT COUNT(*) FROM naive_baselines WHERE source_epoch = $1) +
		  (SELECT COUNT(*) FROM feature_snapshots WHERE source_epoch = $1)
	`, SentinelSourceEpoch).Scan(&out.UnknownEpochRows); err != nil {
		return nil, fmt.Errorf("LoadOnboardingTenantStatus: probe unknown_epoch: %w", err)
	}
	return out, nil
}

func (s *DB) loadOnboardingRowCounts() ([]OnboardingSubScoreRowCounts, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	out := make([]OnboardingSubScoreRowCounts, 0, len(onboardingSubScores))
	for _, sub := range onboardingSubScores {
		var c OnboardingSubScoreRowCounts
		c.SubScore = sub
		var latest *string
		err := s.pool.QueryRow(ctx, `
			SELECT
			  (SELECT COUNT(*) FROM target_snapshots WHERE sub_score = $1),
			  (SELECT COUNT(*) FROM target_snapshots WHERE sub_score = $1 AND eligible = TRUE),
			  (SELECT COUNT(*) FROM naive_baselines WHERE sub_score = $1),
			  (SELECT COUNT(*) FROM naive_baselines WHERE sub_score = $1 AND predicted_value IS NOT NULL),
			  (SELECT COUNT(*) FROM feature_snapshots WHERE sub_score = $1),
			  (SELECT MAX(date) FROM target_snapshots WHERE sub_score = $1)
		`, sub).Scan(&c.TargetSnapshots, &c.EligibleTargets, &c.NaiveBaselines, &c.BaselinesWithValue, &c.FeatureSnapshots, &latest)
		if err != nil {
			return nil, fmt.Errorf("loadOnboardingRowCounts %s: %w", sub, err)
		}
		c.LatestDate = latest
		out = append(out, c)
	}
	return out, nil
}

// LoadChronicThresholdEcho samples the most recent eligible chronic
// target row **within the given source_epoch** and parses its
// `data_coverage` JSON to read the thresholds the writer used.
// Returns nil (no error) when no chronic rows exist yet in this
// epoch — Step 5 handles that as "verify thresholds after running
// Step 4".
//
// Epoch scoping is load-bearing: without it, a fresh onboarding
// where Step 4 has not yet written chronic rows in the current epoch
// would silently surface a row from an older epoch and either falsely
// confirm or falsely contradict the current effective config. The
// wizard's threshold comparison is only meaningful for the epoch the
// operator is onboarding right now.
//
// Sampling one row is enough: every chronic row in the same writer
// pass carries the same threshold values (the writer reads them
// once and threads them through), so any single eligible row in the
// current epoch tells the operator what the last backfill wrote for
// that epoch.
func (s *DB) LoadChronicThresholdEcho(sourceEpoch string) (*ChronicThresholdEcho, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	var date, targetKind string
	var coverageJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT date, target_kind, data_coverage::text
		  FROM target_snapshots
		 WHERE sub_score = $1
		   AND eligible = TRUE
		   AND data_coverage IS NOT NULL
		   AND source_epoch = $2
		 ORDER BY date DESC
		 LIMIT 1
	`, SubScoreChronicLoad, sourceEpoch).Scan(&date, &targetKind, &coverageJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("LoadChronicThresholdEcho: %w", err)
	}
	var dc struct {
		BreachThreshold        int `json:"breach_threshold"`
		AcuteDensityThreshold  int `json:"acute_density_threshold"`
	}
	if err := json.Unmarshal(coverageJSON, &dc); err != nil {
		return nil, fmt.Errorf("LoadChronicThresholdEcho: parse data_coverage on %s: %w", date, err)
	}
	return &ChronicThresholdEcho{
		SampledDate:        date,
		SampledTargetKind:  targetKind,
		BreachThreshold:    dc.BreachThreshold,
		AcuteDensityThresh: dc.AcuteDensityThreshold,
	}, nil
}

// LoadOnboardingCoverageSummary computes Step 3 state — just the
// numbers that drive the chronic-config retune decision. Specifically
// the Acute OR base rate (the calibration knob from PR #97).
//
// Acute base rate is scoped to the active source_epoch since the
// chronic_acute_density threshold is calibrated per epoch. Computing
// it across the whole table would mix epochs and produce a misleading
// suggestion for the wizard's tooltip.
func (s *DB) LoadOnboardingCoverageSummary(asOfDate string) (*OnboardingCoverageSummary, error) {
	if _, err := time.Parse(isoDate, asOfDate); err != nil {
		return nil, fmt.Errorf("LoadOnboardingCoverageSummary: parse asOfDate %q: %w", asOfDate, err)
	}
	epoch, err := s.ResolveSourceEpoch(asOfDate)
	if err != nil {
		return nil, fmt.Errorf("LoadOnboardingCoverageSummary: resolve epoch: %w", err)
	}
	out := &OnboardingCoverageSummary{}

	ctx, cancel := queryCtx()
	defer cancel()

	// Acute eligible count + OR positive rate.
	var acuteN, acutePos int
	if err := s.pool.QueryRow(ctx, `
		SELECT
		  COUNT(*),
		  COUNT(*) FILTER (WHERE target_value >= 0.5)
		  FROM target_snapshots
		 WHERE sub_score = $1
		   AND target_kind = $2
		   AND eligible = TRUE
		   AND target_value IS NOT NULL
		   AND source_epoch = $3
	`, SubScoreAcuteRisk, TargetKindEventT1T3, epoch).Scan(&acuteN, &acutePos); err != nil {
		return nil, fmt.Errorf("LoadOnboardingCoverageSummary: acute base rate: %w", err)
	}
	out.AcuteEligibleN = acuteN
	if acuteN > 0 {
		r := float64(acutePos) / float64(acuteN)
		out.AcuteORBaseRate = &r
	}

	// Chronic eligible count — gates whether running chip recompute
	// makes sense yet.
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM target_snapshots
		 WHERE sub_score = $1
		   AND target_kind = $2
		   AND eligible = TRUE
		   AND source_epoch = $3
	`, SubScoreChronicLoad, TargetKindChronicLabel, epoch).Scan(&out.ChronicEligibleN); err != nil {
		return nil, fmt.Errorf("LoadOnboardingCoverageSummary: chronic count: %w", err)
	}
	return out, nil
}

