// Package storage — readiness redesign tables.
//
// Phase 0 infrastructure for the readiness redesign described in
// READINESS_REDESIGN_PLAN.md. Four new tables:
//
//   - target_snapshots   — daily targets per (date, sub_score, target_kind)
//   - feature_snapshots  — daily feature payload per (date, sub_score)
//   - naive_baselines    — predictions from naive baselines per
//                          (date, sub_score, target_kind, baseline_kind)
//   - source_epochs      — small catalogue of ingest/physiology epochs
//
// All four are idempotently created on every startup via
// EnsureReadinessRedesignTables, alongside the other Ensure*Tables
// helpers in this package. The migration is safe to re-run; every
// statement is gated by IF NOT EXISTS.
//
// `date` is TEXT (YYYY-MM-DD) computed in Go under the tenant's
// REPORT_TZ — same convention as `daily_scores.date` and
// `energy_snapshots.date`. A Postgres GENERATED column would force a
// hardcoded TZ in DDL, which is wrong for multi-tenant.
//
// Enums are stored as TEXT and validated in Go (Is*Valid). No CHECK
// constraints: adding a new enum value should not require a DB
// migration.
//
// `source_epoch` on each row is captured at write time. If a new epoch
// boundary is added retroactively, previously-written rows keep their
// recorded `source_epoch` — a separate backfill re-stamps them. Reading
// `source_epochs` on every query would be correct but too slow for the
// hot path.

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

// --- Enums (Phase 0 vocabulary) -----------------------------------------

// SubScore identifiers — matches the five-member family in
// READINESS_REDESIGN_PLAN.md §2.
const (
	SubScoreRecoveryStability = "recovery_stability"
	SubScorePassiveEfficiency = "passive_efficiency"
	SubScoreAcuteRisk         = "acute_risk"
	SubScoreChronicLoad       = "chronic_load"
	SubScoreAthleticReadiness = "athletic_readiness"
)

// TargetKind identifiers — the shape of a target_snapshots row's value.
// Multiple shapes can coexist per (date, sub_score); see §4.2 and §9.5.
const (
	TargetKindRolling3d              = "rolling_3d"                // 3-day rolling target (primary for Recovery/Passive)
	TargetKindRolling3dCandidate2of3 = "rolling_3d_candidate_2of3" // Recovery evidence target: mean over eligible nights when at least 2 of t+1..t+3 are eligible
	TargetKindDailyPoint             = "daily_point"               // single-day value (secondary)
	TargetKindEventT1T3              = "event_t1_t3"               // OR-event in t+1..t+3 (Acute Risk primary): any day in window with HRV drop ≥1.5σ OR RHR spike ≥1.5σ
	TargetKindEventStrictT1T3        = "event_strict_t1_t3"        // AND-event in t+1..t+3 (Acute Risk secondary): some day in window with HRV drop AND RHR spike same day
	TargetKindWoResidual             = "wo_residual"               // per-workout HR residual (Athletic, dormant)
	TargetKindChronicLabel           = "chronic_label"             // sustained-deterioration binary label (Chronic Load primary): ≥5 of 14 forward days breach Recovery 3d-roll EWMA45 by >1σ
	TargetKindChronicAcuteDensity    = "chronic_acute_density"     // analysis label (Chronic Load secondary): ≥3 Acute Risk OR-events in t+1..t+14 forward window
)

// BaselineKind identifiers — naive predictors written alongside targets
// so models in Phase 1 have a floor to beat.
const (
	BaselineKindPersistenceYesterday = "persistence_yesterday"
	BaselineKindRolling7dMean        = "rolling_7d_mean"
	BaselineKindRolling30dMean       = "rolling_30d_mean"
	BaselineKindEWMA45d              = "ewma_45d"
	BaselineKindSlow180d             = "slow_180d"
	BaselineKindEventBaseRate        = "event_base_rate"
	BaselineKindRecencyDecay         = "recency_decay"
)

// EligibilityReason values — TEXT in DB, validated in Go. The enum is
// open: writers can introduce new reasons as long as they appear here.
// See §3.1, §4.2, §4.2.1 of the plan.
const (
	EligibilityOK                    = "ok"
	EligibilityOKAwakeStructuralZero = "ok_awake_structural_zero"
	EligibilityMissingAwakeUnknown   = "missing_awake_unknown"
	EligibilitySleepTotalOutOfRange  = "sleep_total_out_of_range"
	// EligibilitySleepDataMissing splits the old "out_of_range" bucket
	// for nights with no source row at all (Total==NULL). Introduced in
	// recovery_stability formula_version 2 so re-backfilled rows can be
	// distinguished from rows written under the v1 over-broad reason.
	EligibilitySleepDataMissing    = "sleep_data_missing"
	EligibilityCoarseOnlySource    = "coarse_only_source"
	EligibilityNoWalkingSegments   = "no_walking_segments"
	EligibilityNoWalkingHR         = "no_walking_hr"
	EligibilityWalkingHROutOfRange = "walking_hr_out_of_range"
	// EligibilityEventWindowDataMissing fires when a forward-looking
	// event-classifier target (Acute Risk) cannot honestly write a
	// negative label because at least one day in the t+1..t+3 window
	// has no observable signal. Otherwise a sensor gap would be
	// silently coded as "no breach".
	EligibilityEventWindowDataMissing        = "event_window_data_missing"
	EligibilityValueOutOfRange               = "value_out_of_range"
	EligibilityHRVSparse                     = "hrv_sparse"
	EligibilityBaselineWarmup                = "baseline_warmup"
	EligibilityImporterGap                   = "importer_gap"
	EligibilityWalkingOnly                   = "walking_only"
	EligibilityWASORequiresSegments          = "waso_requires_segments"
	EligibilityFragmentationRequiresSegments = "fragmentation_requires_segments"
	EligibilityDataAnomaly2024               = "data_anomaly_2024"
	EligibilityNoStructuredWorkouts          = "no_structured_workouts"
)

// ChipCalibrationStatus values — populated by the auto-calibration
// writer when it tries to derive a binary-chip cutoff per (sub_score,
// target_kind, source_epoch). Richer than a single "insufficient_data"
// bucket so an operator can tell "too few rows" from "too few
// positives" without spelunking; the UI collapses any non-`active`
// status into a single "calibrating" state.
const (
	ChipCalibrationStatusActive               = "active"
	ChipCalibrationStatusInsufficientEligible = "insufficient_eligible"
	ChipCalibrationStatusInsufficientPositive = "insufficient_positives"
	ChipCalibrationStatusNoCurrentEpoch       = "no_current_epoch"
)

// ChipCalibrationMethodPercentileP80 is the only method shipped so
// far. Stored as a column so future methods (e.g. constant override,
// adaptive percentile) don't need a schema migration.
const ChipCalibrationMethodPercentileP80 = "percentile_p80"

// BaselineReason values — chip-facing explanation when a
// `naive_baselines.predicted_value` is NULL. Authoritative for the
// per-sub-score chip's `unknown` state in §6.1 of the plan (the chip
// does NOT fall back to target_snapshots.eligibility_reason; the two
// writers have different eligibility conditions).
//
// The enum is open: writers can introduce new reasons as long as they
// appear here. Empty string is reserved for "predicted_value present"
// rows — readers MUST treat reason as meaningful only when the value
// is NULL.
const (
	// BaselineReasonWarmup — the trailing window has no eligible
	// observations yet, but it lies fully inside the current
	// source_epoch. Will eventually clear as data accumulates.
	BaselineReasonWarmup = "baseline_warmup"
	// BaselineReasonSourceEpochBoundary — the trailing window starts
	// before the current `source_epoch.start_date`, so the baseline
	// computation has been clipped to (possibly) zero observations.
	// Different from warmup because operator intervention (epoch
	// catalogue) shaped this, not just time-since-onboarding.
	BaselineReasonSourceEpochBoundary = "baseline_source_epoch_boundary"
)

const ChipReasonSourceEpochChange = "source_epoch_change"

// SourceEpochKind separates ingest/source epochs from user-side
// physiology regime changes.
const (
	SourceEpochKindIngest     = "source_epoch"
	SourceEpochKindPhysiology = "physiology_epoch"
)

// DetectedBy records how an epoch boundary entered the catalogue.
const (
	DetectedByManual            = "manual"
	DetectedByDistributionShift = "distribution_shift"
)

// SentinelSourceEpoch is returned by ResolveSourceEpoch when no epoch
// covers a date. Should not happen in practice — the bootstrap row in
// EnsureReadinessRedesignTables covers 2014-01-01..NULL, so any
// realistic ingest date resolves to at least `initial`.
const SentinelSourceEpoch = "unknown"

// InitialSourceEpoch is the bootstrap row planted by
// EnsureReadinessRedesignTables so writers don't fail on day zero.
const InitialSourceEpoch = "initial"

var validSubScores = map[string]struct{}{
	SubScoreRecoveryStability: {}, SubScorePassiveEfficiency: {},
	SubScoreAcuteRisk: {}, SubScoreChronicLoad: {},
	SubScoreAthleticReadiness: {},
}

var validTargetKinds = map[string]struct{}{
	TargetKindRolling3d: {}, TargetKindRolling3dCandidate2of3: {}, TargetKindDailyPoint: {},
	TargetKindEventT1T3: {}, TargetKindEventStrictT1T3: {},
	TargetKindWoResidual: {}, TargetKindChronicLabel: {},
	TargetKindChronicAcuteDensity: {},
}

var validBaselineKinds = map[string]struct{}{
	BaselineKindPersistenceYesterday: {}, BaselineKindRolling7dMean: {},
	BaselineKindRolling30dMean: {}, BaselineKindEWMA45d: {},
	BaselineKindSlow180d: {}, BaselineKindEventBaseRate: {},
	BaselineKindRecencyDecay: {},
}

// validChipCalibrationStatuses gates SaveChipCalibration. Same pattern
// as validBaselineKinds — DB column is plain TEXT; new statuses don't
// require a schema migration.
var validChipCalibrationStatuses = map[string]struct{}{
	ChipCalibrationStatusActive:               {},
	ChipCalibrationStatusInsufficientEligible: {},
	ChipCalibrationStatusInsufficientPositive: {},
	ChipCalibrationStatusNoCurrentEpoch:       {},
}

// validChipCalibrationMethods — same shape. Lone method right now;
// listed so future writers can land additional methods without
// regenerating the enum check downstream.
var validChipCalibrationMethods = map[string]struct{}{
	ChipCalibrationMethodPercentileP80: {},
}

// validBaselineReasons gates SaveNaiveBaseline so the chip can rely on
// `reason` being one of a known enum value when `predicted_value IS
// NULL`. Same pattern as validBaselineKinds — DB column is plain TEXT,
// new enum values don't need a schema migration.
var validBaselineReasons = map[string]struct{}{
	BaselineReasonWarmup:              {},
	BaselineReasonSourceEpochBoundary: {},
}

// IsSubScoreValid returns true for the five sub-scores defined in §2.
func IsSubScoreValid(s string) bool { _, ok := validSubScores[s]; return ok }

// IsTargetKindValid returns true for the recognised target_kind values.
func IsTargetKindValid(s string) bool { _, ok := validTargetKinds[s]; return ok }

// IsBaselineKindValid returns true for the recognised baseline_kind values.
func IsBaselineKindValid(s string) bool { _, ok := validBaselineKinds[s]; return ok }

// IsBaselineReasonValid returns true for the recognised baseline_reason
// enum values used on NULL `predicted_value` rows.
func IsBaselineReasonValid(s string) bool { _, ok := validBaselineReasons[s]; return ok }

// IsChipCalibrationStatusValid returns true for the recognised
// chip_calibrations.status enum values.
func IsChipCalibrationStatusValid(s string) bool {
	_, ok := validChipCalibrationStatuses[s]
	return ok
}

// IsChipCalibrationMethodValid returns true for the recognised
// chip_calibrations.method enum values.
func IsChipCalibrationMethodValid(s string) bool {
	_, ok := validChipCalibrationMethods[s]
	return ok
}

// --- Migration ----------------------------------------------------------

// EnsureReadinessRedesignTables creates the four redesign tables, their
// indexes, and seeds the `initial` source_epoch row. Idempotent — every
// statement is guarded by IF NOT EXISTS and the bootstrap upsert uses
// ON CONFLICT DO NOTHING. Safe to call on every startup, on every
// tenant.
//
// Errors are logged but do not abort startup. Callers downstream
// (Phase 0 writers) defensively check existence on every write.
func (s *DB) EnsureReadinessRedesignTables() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.EnsureReadinessRedesignTablesContext(ctx); err != nil {
		log.Printf("EnsureReadinessRedesignTables: %v", err)
	}
}

func (s *DB) EnsureReadinessRedesignTablesContext(ctx context.Context) error {
	stmts := []string{
		// source_epochs — the small catalogue of ingest + physiology epochs.
		`CREATE TABLE IF NOT EXISTS source_epochs (
			epoch_id     TEXT PRIMARY KEY,
			start_date   TEXT NOT NULL,
			end_date     TEXT,
			kind         TEXT NOT NULL,
			description  TEXT NOT NULL DEFAULT '',
			detected_by  TEXT NOT NULL,
			confirmed    BOOLEAN NOT NULL DEFAULT FALSE,
			created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_source_epochs_kind_start
			ON source_epochs (kind, start_date)`,
		`CREATE INDEX IF NOT EXISTS idx_source_epochs_active
			ON source_epochs (kind, start_date DESC)
			WHERE end_date IS NULL`,

		// target_snapshots — primary + secondary target writes per (date, sub_score, target_kind).
		`CREATE TABLE IF NOT EXISTS target_snapshots (
			date                TEXT NOT NULL,
			sub_score           TEXT NOT NULL,
			target_kind         TEXT NOT NULL,
			target_value        REAL,
			eligible            BOOLEAN NOT NULL,
			eligibility_reason  TEXT NOT NULL,
			data_coverage       JSONB,
			source_epoch        TEXT NOT NULL,
			formula_version     INTEGER NOT NULL,
			computed_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (date, sub_score, target_kind)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_target_snapshots_sub_kind_date
			ON target_snapshots (sub_score, target_kind, date DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_target_snapshots_source_epoch
			ON target_snapshots (source_epoch)`,

		// feature_snapshots — one row per (date, sub_score) holding the JSONB feature payload.
		`CREATE TABLE IF NOT EXISTS feature_snapshots (
			date             TEXT NOT NULL,
			sub_score        TEXT NOT NULL,
			features         JSONB NOT NULL,
			source_epoch     TEXT NOT NULL,
			feature_version  INTEGER NOT NULL,
			computed_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (date, sub_score)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_feature_snapshots_sub_date
			ON feature_snapshots (sub_score, date DESC)`,

		// naive_baselines — predictions from naive baselines per (date, sub_score, target_kind, baseline_kind).
		`CREATE TABLE IF NOT EXISTS naive_baselines (
			date              TEXT NOT NULL,
			sub_score         TEXT NOT NULL,
			target_kind       TEXT NOT NULL,
			baseline_kind     TEXT NOT NULL,
			predicted_value   REAL,
			source_epoch      TEXT NOT NULL,
			formula_version   INTEGER NOT NULL,
			computed_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (date, sub_score, target_kind, baseline_kind)
		)`,
		// `reason` lands after the initial schema (Phase 2 track 3 of
		// the operationalisation plan). Idempotent ALTER so re-running
		// EnsureReadinessRedesignTables on a fresh tenant just gets the
		// column up-front. Pre-existing rows from before this PR keep
		// `reason` NULL — which is the same semantic as "value
		// present, no explanation needed".
		`ALTER TABLE naive_baselines ADD COLUMN IF NOT EXISTS reason TEXT`,
		`CREATE INDEX IF NOT EXISTS idx_naive_baselines_sub_kind_base_date
			ON naive_baselines (sub_score, target_kind, baseline_kind, date DESC)`,

		// chip_calibrations — Phase 2 §6.1: cutoff that maps a binary
		// chip's predicted_value to elevated vs ok, per tenant per
		// source_epoch. Recomputed by RecomputeChipCalibrations on a
		// rolling 180-day window; one row per (sub_score, target_kind,
		// source_epoch). Audit fields (p80, base_rate) are persisted
		// alongside the final cutoff so operators can tell which guard
		// fired without re-running the writer.
		`CREATE TABLE IF NOT EXISTS chip_calibrations (
			sub_score                TEXT NOT NULL,
			target_kind              TEXT NOT NULL,
			source_epoch             TEXT NOT NULL,
			status                   TEXT NOT NULL,
			method                   TEXT NOT NULL,
			cutoff                   REAL,
			p80                      REAL,
			base_rate                REAL,
			calibration_window_days  INTEGER NOT NULL,
			n_eligible               INTEGER NOT NULL,
			n_positives              INTEGER NOT NULL,
			computed_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (sub_score, target_kind, source_epoch)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_chip_calibrations_sub_kind
			ON chip_calibrations (sub_score, target_kind, computed_at DESC)`,
	}
	for _, q := range stmts {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			return err
		}
	}

	// Bootstrap initial source_epoch so writers can resolve any historical
	// date without falling through to SentinelSourceEpoch. Idempotent.
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO source_epochs
			(epoch_id, start_date, end_date, kind, description, detected_by, confirmed)
		VALUES
			($1, $2, NULL, $3, $4, $5, TRUE)
		ON CONFLICT (epoch_id) DO NOTHING
	`,
		InitialSourceEpoch,
		"2014-01-01",
		SourceEpochKindIngest,
		"pre-redesign baseline; covers all historical data prior to the first detected epoch boundary",
		DetectedByManual,
	); err != nil {
		return err
	}
	return nil
}

// --- Storage API --------------------------------------------------------

// TargetSnapshot is the input to SaveTargetSnapshot.
type TargetSnapshot struct {
	Date              string
	SubScore          string
	TargetKind        string
	TargetValue       *float64 // nil when ineligible
	Eligible          bool
	EligibilityReason string
	DataCoverage      []byte // raw JSON; wrap as json.RawMessage at write time
	SourceEpoch       string
	FormulaVersion    int
}

// SaveTargetSnapshot upserts a single target row. Idempotent on the
// composite PK (date, sub_score, target_kind). Validates sub_score and
// target_kind at the Go layer.
func (s *DB) SaveTargetSnapshot(t TargetSnapshot) error {
	if !IsSubScoreValid(t.SubScore) {
		return fmt.Errorf("SaveTargetSnapshot: invalid sub_score %q", t.SubScore)
	}
	if !IsTargetKindValid(t.TargetKind) {
		return fmt.Errorf("SaveTargetSnapshot: invalid target_kind %q", t.TargetKind)
	}
	if t.EligibilityReason == "" {
		return fmt.Errorf("SaveTargetSnapshot: eligibility_reason must be non-empty (use %q for happy path)", EligibilityOK)
	}
	if t.SourceEpoch == "" {
		return fmt.Errorf("SaveTargetSnapshot: source_epoch must be non-empty")
	}

	ctx, cancel := queryCtx()
	defer cancel()

	var coverage any
	if len(t.DataCoverage) > 0 {
		coverage = json.RawMessage(t.DataCoverage)
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO target_snapshots
			(date, sub_score, target_kind, target_value, eligible, eligibility_reason,
			 data_coverage, source_epoch, formula_version, computed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
		ON CONFLICT (date, sub_score, target_kind) DO UPDATE SET
			target_value = excluded.target_value,
			eligible = excluded.eligible,
			eligibility_reason = excluded.eligibility_reason,
			data_coverage = excluded.data_coverage,
			source_epoch = excluded.source_epoch,
			formula_version = excluded.formula_version,
			computed_at = NOW()
	`,
		t.Date, t.SubScore, t.TargetKind, t.TargetValue,
		t.Eligible, t.EligibilityReason, coverage,
		t.SourceEpoch, t.FormulaVersion,
	)
	return err
}

// FeatureSnapshot is the input to SaveFeatureSnapshot.
type FeatureSnapshot struct {
	Date           string
	SubScore       string
	Features       []byte // raw JSON; must be non-empty
	SourceEpoch    string
	FeatureVersion int
}

// SaveFeatureSnapshot upserts the one canonical feature payload per
// (date, sub_score). Bumping feature_version overwrites the row — no
// parallel versions in Phase 0 (see plan §9 closed decisions).
func (s *DB) SaveFeatureSnapshot(f FeatureSnapshot) error {
	if !IsSubScoreValid(f.SubScore) {
		return fmt.Errorf("SaveFeatureSnapshot: invalid sub_score %q", f.SubScore)
	}
	if len(f.Features) == 0 {
		return fmt.Errorf("SaveFeatureSnapshot: features JSON must be non-empty")
	}
	if f.SourceEpoch == "" {
		return fmt.Errorf("SaveFeatureSnapshot: source_epoch must be non-empty")
	}

	ctx, cancel := queryCtx()
	defer cancel()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO feature_snapshots
			(date, sub_score, features, source_epoch, feature_version, computed_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (date, sub_score) DO UPDATE SET
			features = excluded.features,
			source_epoch = excluded.source_epoch,
			feature_version = excluded.feature_version,
			computed_at = NOW()
	`,
		f.Date, f.SubScore, json.RawMessage(f.Features),
		f.SourceEpoch, f.FeatureVersion,
	)
	return err
}

// NaiveBaseline is the input to SaveNaiveBaseline.
type NaiveBaseline struct {
	Date           string
	SubScore       string
	TargetKind     string
	BaselineKind   string
	PredictedValue *float64 // nil for ineligible / not-applicable
	// Reason explains a NULL PredictedValue for chip rendering. Must
	// be one of the BaselineReason* constants when PredictedValue is
	// nil, and empty string when PredictedValue is non-nil. The two
	// are joint state; readers MUST NOT interpret reason without
	// first checking the value.
	Reason         string
	SourceEpoch    string
	FormulaVersion int
}

// SaveNaiveBaseline upserts a naive baseline prediction. Idempotent on
// the composite PK (date, sub_score, target_kind, baseline_kind).
func (s *DB) SaveNaiveBaseline(b NaiveBaseline) error {
	if !IsSubScoreValid(b.SubScore) {
		return fmt.Errorf("SaveNaiveBaseline: invalid sub_score %q", b.SubScore)
	}
	if !IsTargetKindValid(b.TargetKind) {
		return fmt.Errorf("SaveNaiveBaseline: invalid target_kind %q", b.TargetKind)
	}
	if !IsBaselineKindValid(b.BaselineKind) {
		return fmt.Errorf("SaveNaiveBaseline: invalid baseline_kind %q", b.BaselineKind)
	}
	if b.SourceEpoch == "" {
		return fmt.Errorf("SaveNaiveBaseline: source_epoch must be non-empty")
	}
	// Joint-state guard. The chip-facing contract (§6.1) is that
	// `reason` is meaningful **iff** `predicted_value IS NULL`. Anything
	// else (both set, both empty, unknown reason value) would either
	// contradict the row itself or leave the chip without a usable
	// `unknown` explanation — both produce hard-to-debug UI bugs once
	// downstream consumers ship.
	switch {
	case b.PredictedValue != nil:
		if b.Reason != "" {
			return fmt.Errorf("SaveNaiveBaseline: reason %q set on non-nil predicted_value", b.Reason)
		}
	default: // predicted_value IS NULL
		if b.Reason == "" {
			return fmt.Errorf("SaveNaiveBaseline: reason must be set when predicted_value is nil")
		}
		if !IsBaselineReasonValid(b.Reason) {
			return fmt.Errorf("SaveNaiveBaseline: invalid reason %q", b.Reason)
		}
	}

	// reason is stored as NULL when empty so historical rows from
	// before this column existed remain semantically identical to
	// freshly-written value-present rows.
	var reason any
	if b.Reason != "" {
		reason = b.Reason
	}

	ctx, cancel := queryCtx()
	defer cancel()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO naive_baselines
			(date, sub_score, target_kind, baseline_kind, predicted_value,
			 reason, source_epoch, formula_version, computed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
		ON CONFLICT (date, sub_score, target_kind, baseline_kind) DO UPDATE SET
			predicted_value = excluded.predicted_value,
			reason = excluded.reason,
			source_epoch = excluded.source_epoch,
			formula_version = excluded.formula_version,
			computed_at = NOW()
	`,
		b.Date, b.SubScore, b.TargetKind, b.BaselineKind,
		b.PredictedValue, reason, b.SourceEpoch, b.FormulaVersion,
	)
	return err
}

// OperationalContractRow is one (date, sub_score) cell as it would
// render on the chip — used by the admin operational-contract preview
// endpoint to validate §6.1 of the plan before any UI chip ships.
//
// Each row joins the deployable `naive_baselines` row (chip value +
// authoritative reason on NULL) with its sibling `target_snapshots`
// row (secondary diagnostic reason). Either side may be NULL when the
// writer has not reached this date yet, which is the §6.1 "pending"
// state — the handler renders that explicitly so an operator can spot
// gaps.
type OperationalContractRow struct {
	Date                    string
	SubScore                string
	TargetKind              string
	BaselineKind            string
	PredictedValue          *float64
	BaselineReason          *string
	SourceEpoch             *string
	TargetEligible          *bool
	TargetEligibilityReason *string
	CurrentSourceEpoch      *string
	SourceEpochChanged      bool
	// Cutoff + CalibrationStatus carry the per-tenant chip threshold
	// for binary chips (Acute, Chronic). Joined from
	// `chip_calibrations` on (sub_score, target_kind, source_epoch);
	// both are NULL on continuous chips (Recovery, Passive — no
	// calibration concept) and on dates whose source_epoch hasn't
	// been calibrated yet. Readers decide chip state from the
	// (PredictedValue, Cutoff, CalibrationStatus) triple — see
	// chipCellStateFromRow in the admin handler.
	Cutoff            *float64
	CalibrationStatus *string
}

// chipConfigs lists the (sub_score, target_kind, baseline_kind)
// triples that drive the chip — see plan §6.1 "Primary target_kind +
// baseline_kind per sub-score" table. The order is the chip render
// order on the dashboard (Recovery, Passive, Chronic, Acute) so
// downstream consumers can group by date and trust the slot ordering.
var chipConfigs = []struct {
	SubScore     string
	TargetKind   string
	BaselineKind string
}{
	{SubScoreRecoveryStability, TargetKindRolling3d, BaselineKindEWMA45d},
	{SubScorePassiveEfficiency, TargetKindRolling3d, BaselineKindEWMA45d},
	{SubScoreChronicLoad, TargetKindChronicLabel, BaselineKindEventBaseRate},
	{SubScoreAcuteRisk, TargetKindEventT1T3, BaselineKindEventBaseRate},
}

// LoadOperationalContractRows returns one row per (date, chip_config)
// for the inclusive date range, joined across `naive_baselines` and
// `target_snapshots`. Missing rows on either side surface as NULL
// fields — the consumer (admin page) renders that as `pending` or
// `unknown` per §6.1 rules.
//
// Sort order: date DESC, then chip render order (Recovery → Passive
// → Chronic → Acute). That matches both the admin table layout and
// the dashboard chip slot order.
func (s *DB) LoadOperationalContractRows(from, to string) ([]OperationalContractRow, error) {
	ctx, cancel := queryCtx()
	defer cancel()

	// One SELECT per (sub_score, target_kind, baseline_kind) triple
	// keeps the query simple (no values-CTE plumbing or
	// generate_series). The whole result set is bounded by
	// `len(chipConfigs) * days` — at the admin default of 14 days
	// that's 56 rows, well under any concern. Each LEFT JOIN binds
	// to a single chip configuration so the result rows can be
	// distinguished without grouping.
	// Date subquery yields every date in the range where ANY chip
	// configuration has at least one row in either table. Per-chip
	// rows for that date are then LEFT-JOINed in — including
	// configurations that have neither side populated, so the operator
	// sees a `pending` cell when one writer lags behind another on the
	// same calendar day. The earlier WHERE filter discarded those
	// pending rows and made the gap invisible (Codex review on PR #109).
	// LEFT JOIN onto chip_calibrations brings the per-tenant cutoff
	// into the same row the chip would render — keyed on
	// (sub_score, target_kind, source_epoch). Continuous chips
	// (Recovery, Passive) don't have calibration rows and end up
	// with NULL cutoff/status. The handler distinguishes "no
	// calibration concept" (continuous chip) from "not yet
	// calibrated" (binary chip with NULL row) by checking sub_score.
	const stmt = `
		SELECT
			d.date,
			$1::text AS sub_score,
			$2::text AS target_kind,
			$3::text AS baseline_kind,
			n.predicted_value,
			n.reason AS baseline_reason,
			COALESCE(n.source_epoch, t.source_epoch) AS source_epoch,
			t.eligible,
			t.eligibility_reason,
			cc.cutoff,
			cc.status
		FROM (
			SELECT DISTINCT date FROM naive_baselines
			 WHERE date BETWEEN $4 AND $5
			UNION
			SELECT DISTINCT date FROM target_snapshots
			 WHERE date BETWEEN $4 AND $5
		) d
		LEFT JOIN naive_baselines n
			ON n.date = d.date AND n.sub_score = $1
		   AND n.target_kind = $2 AND n.baseline_kind = $3
		LEFT JOIN target_snapshots t
			ON t.date = d.date AND t.sub_score = $1 AND t.target_kind = $2
		LEFT JOIN chip_calibrations cc
			ON cc.sub_score = $1 AND cc.target_kind = $2
		   AND cc.source_epoch = COALESCE(n.source_epoch, t.source_epoch)
		ORDER BY d.date DESC
	`

	rowsByDate := make(map[string][]OperationalContractRow)
	currentEpochByDate := make(map[string]string)
	for _, c := range chipConfigs {
		pgRows, err := s.pool.Query(ctx, stmt, c.SubScore, c.TargetKind, c.BaselineKind, from, to)
		if err != nil {
			return nil, fmt.Errorf("LoadOperationalContractRows: query %s/%s/%s: %w",
				c.SubScore, c.TargetKind, c.BaselineKind, err)
		}
		for pgRows.Next() {
			var r OperationalContractRow
			var predicted *float32
			var cutoff *float32
			if err := pgRows.Scan(
				&r.Date, &r.SubScore, &r.TargetKind, &r.BaselineKind,
				&predicted, &r.BaselineReason, &r.SourceEpoch,
				&r.TargetEligible, &r.TargetEligibilityReason,
				&cutoff, &r.CalibrationStatus,
			); err != nil {
				pgRows.Close()
				return nil, fmt.Errorf("LoadOperationalContractRows: scan: %w", err)
			}
			if predicted != nil {
				v := float64(*predicted)
				r.PredictedValue = &v
			}
			if cutoff != nil {
				v := float64(*cutoff)
				r.Cutoff = &v
			}
			currentEpoch, ok := currentEpochByDate[r.Date]
			if !ok {
				currentEpoch, err = s.ResolveSourceEpoch(r.Date)
				if err != nil {
					pgRows.Close()
					return nil, fmt.Errorf("LoadOperationalContractRows: resolve source_epoch for %s: %w", r.Date, err)
				}
				currentEpochByDate[r.Date] = currentEpoch
			}
			r.CurrentSourceEpoch = &currentEpoch
			if r.SourceEpoch != nil && *r.SourceEpoch != "" && *r.SourceEpoch != currentEpoch {
				r.SourceEpochChanged = true
			}
			rowsByDate[r.Date] = append(rowsByDate[r.Date], r)
		}
		if err := pgRows.Err(); err != nil {
			pgRows.Close()
			return nil, fmt.Errorf("LoadOperationalContractRows: rows: %w", err)
		}
		pgRows.Close()
	}

	// Flatten with the documented sort order: date DESC, then chip
	// render order from chipConfigs.
	dates := make([]string, 0, len(rowsByDate))
	for d := range rowsByDate {
		dates = append(dates, d)
	}
	// Descending lexicographic equals descending chronological for
	// YYYY-MM-DD strings, so a plain reverse sort works without
	// parsing.
	for i := 0; i < len(dates); i++ {
		for j := i + 1; j < len(dates); j++ {
			if dates[j] > dates[i] {
				dates[i], dates[j] = dates[j], dates[i]
			}
		}
	}
	chipOrder := make(map[string]int, len(chipConfigs))
	for i, c := range chipConfigs {
		chipOrder[c.SubScore] = i
	}
	out := make([]OperationalContractRow, 0, len(rowsByDate)*len(chipConfigs))
	for _, d := range dates {
		group := rowsByDate[d]
		// Stable sort by chip order; small N so insertion sort is fine.
		for i := 1; i < len(group); i++ {
			for j := i; j > 0 && chipOrder[group[j].SubScore] < chipOrder[group[j-1].SubScore]; j-- {
				group[j], group[j-1] = group[j-1], group[j]
			}
		}
		out = append(out, group...)
	}
	return out, nil
}

// ChipCalibration is one row in chip_calibrations — the auto-derived
// cutoff that maps a binary chip's `predicted_value` to elevated vs
// ok, for a given (sub_score, target_kind, source_epoch).
//
// Audit fields (`P80`, `BaseRate`) record what the percentile rule
// and the base-rate floor produced before the final `Cutoff` was
// chosen (`max(p80, base_rate)`), so an operator reviewing the table
// can tell which guard fired without re-running the writer.
//
// `Cutoff`, `P80`, `BaseRate` follow a strict joint contract enforced
// by SaveChipCalibration:
//   - Status == `active`             → all three MUST be non-nil
//   - Status == any insufficient_*   → all three MUST be nil
//
// Readers MUST check Status before consuming the numeric fields.
type ChipCalibration struct {
	SubScore              string
	TargetKind            string
	SourceEpoch           string
	Status                string   // ChipCalibrationStatus*
	Method                string   // ChipCalibrationMethod*
	Cutoff                *float64 // NULL on insufficient_*
	P80                   *float64 // raw percentile result, NULL on insufficient_*
	BaseRate              *float64 // observed positive rate, NULL on no_current_epoch
	CalibrationWindowDays int
	NEligible             int
	NPositives            int
}

// SaveChipCalibration upserts a single row keyed on
// (sub_score, target_kind, source_epoch). Idempotent — a recompute
// pass overwrites the previous row in place.
//
// Joint-state guards: Status must be a known enum; cutoff/p80/
// base_rate MUST be nil when Status is one of the insufficient_*
// values (the row has no calibration to consume); cutoff MUST be
// populated when Status is `active` (otherwise the chip read path
// would render `calibrating` despite a "we're done" status).
func (s *DB) SaveChipCalibration(c ChipCalibration) error {
	if !IsSubScoreValid(c.SubScore) {
		return fmt.Errorf("SaveChipCalibration: invalid sub_score %q", c.SubScore)
	}
	if !IsTargetKindValid(c.TargetKind) {
		return fmt.Errorf("SaveChipCalibration: invalid target_kind %q", c.TargetKind)
	}
	if c.SourceEpoch == "" {
		return fmt.Errorf("SaveChipCalibration: source_epoch must be non-empty")
	}
	if !IsChipCalibrationStatusValid(c.Status) {
		return fmt.Errorf("SaveChipCalibration: invalid status %q", c.Status)
	}
	if !IsChipCalibrationMethodValid(c.Method) {
		return fmt.Errorf("SaveChipCalibration: invalid method %q", c.Method)
	}
	if c.Status == ChipCalibrationStatusActive {
		// All three numeric fields must be populated. Cutoff is the
		// deployable threshold the chip reads; p80 and base_rate are
		// the audit trail (which guard picked the cutoff). Allowing
		// active rows without the audit fields breaks the contract
		// the admin surface advertises ("see which guard fired
		// without re-running the writer").
		if c.Cutoff == nil {
			return fmt.Errorf("SaveChipCalibration: active status requires non-nil cutoff")
		}
		if c.P80 == nil {
			return fmt.Errorf("SaveChipCalibration: active status requires non-nil p80 (audit field)")
		}
		if c.BaseRate == nil {
			return fmt.Errorf("SaveChipCalibration: active status requires non-nil base_rate (audit field)")
		}
	}
	if c.Status != ChipCalibrationStatusActive {
		// All three derived numeric fields must be nil on
		// insufficient-data statuses. The struct comment spells out
		// this contract; enforcing it at the boundary stops a future
		// writer bug from persisting stale `p80`/`base_rate` from a
		// previous run and showing misleading audit fields in the
		// admin surface.
		if c.Cutoff != nil {
			return fmt.Errorf("SaveChipCalibration: cutoff set on non-active status %q", c.Status)
		}
		if c.P80 != nil {
			return fmt.Errorf("SaveChipCalibration: p80 set on non-active status %q", c.Status)
		}
		if c.BaseRate != nil {
			return fmt.Errorf("SaveChipCalibration: base_rate set on non-active status %q", c.Status)
		}
	}

	ctx, cancel := queryCtx()
	defer cancel()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO chip_calibrations
			(sub_score, target_kind, source_epoch, status, method,
			 cutoff, p80, base_rate, calibration_window_days,
			 n_eligible, n_positives, computed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW())
		ON CONFLICT (sub_score, target_kind, source_epoch) DO UPDATE SET
			status = excluded.status,
			method = excluded.method,
			cutoff = excluded.cutoff,
			p80 = excluded.p80,
			base_rate = excluded.base_rate,
			calibration_window_days = excluded.calibration_window_days,
			n_eligible = excluded.n_eligible,
			n_positives = excluded.n_positives,
			computed_at = NOW()
	`,
		c.SubScore, c.TargetKind, c.SourceEpoch, c.Status, c.Method,
		c.Cutoff, c.P80, c.BaseRate, c.CalibrationWindowDays,
		c.NEligible, c.NPositives,
	)
	return err
}

// LoadChipCalibration returns the single row for the given key, or
// (nil, nil) when no row exists. Used by the chip read path on
// every preview render.
func (s *DB) LoadChipCalibration(subScore, targetKind, sourceEpoch string) (*ChipCalibration, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	var c ChipCalibration
	var cutoff, p80, baseRate *float32
	err := s.pool.QueryRow(ctx, `
		SELECT sub_score, target_kind, source_epoch, status, method,
		       cutoff, p80, base_rate, calibration_window_days,
		       n_eligible, n_positives
		  FROM chip_calibrations
		 WHERE sub_score = $1 AND target_kind = $2 AND source_epoch = $3
	`, subScore, targetKind, sourceEpoch).Scan(
		&c.SubScore, &c.TargetKind, &c.SourceEpoch, &c.Status, &c.Method,
		&cutoff, &p80, &baseRate, &c.CalibrationWindowDays,
		&c.NEligible, &c.NPositives,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("LoadChipCalibration: %w", err)
	}
	if cutoff != nil {
		v := float64(*cutoff)
		c.Cutoff = &v
	}
	if p80 != nil {
		v := float64(*p80)
		c.P80 = &v
	}
	if baseRate != nil {
		v := float64(*baseRate)
		c.BaseRate = &v
	}
	return &c, nil
}

// ListChipCalibrations returns every row in the table, newest
// `computed_at` first. Used by the admin endpoint that shows the
// current calibration state across all configs without recomputing.
func (s *DB) ListChipCalibrations() ([]ChipCalibration, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT sub_score, target_kind, source_epoch, status, method,
		       cutoff, p80, base_rate, calibration_window_days,
		       n_eligible, n_positives
		  FROM chip_calibrations
		 ORDER BY computed_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("ListChipCalibrations: %w", err)
	}
	defer rows.Close()
	var out []ChipCalibration
	for rows.Next() {
		var c ChipCalibration
		var cutoff, p80, baseRate *float32
		if scanErr := rows.Scan(
			&c.SubScore, &c.TargetKind, &c.SourceEpoch, &c.Status, &c.Method,
			&cutoff, &p80, &baseRate, &c.CalibrationWindowDays,
			&c.NEligible, &c.NPositives,
		); scanErr != nil {
			return nil, fmt.Errorf("ListChipCalibrations scan: %w", scanErr)
		}
		if cutoff != nil {
			v := float64(*cutoff)
			c.Cutoff = &v
		}
		if p80 != nil {
			v := float64(*p80)
			c.P80 = &v
		}
		if baseRate != nil {
			v := float64(*baseRate)
			c.BaseRate = &v
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SourceEpoch is one row from the source_epochs catalogue. Returned by
// resolution and listing helpers below.
type SourceEpoch struct {
	EpochID     string
	StartDate   string
	EndDate     *string
	Kind        string
	Description string
	DetectedBy  string
	Confirmed   bool
}

// ResolveSourceEpoch picks the source_epoch that covers `date`. Returns
// SentinelSourceEpoch (`"unknown"`) when nothing matches — which should
// not happen in production because the bootstrap `initial` epoch covers
// 2014-01-01..NULL. Only confirmed rows of kind=source_epoch are
// considered (unconfirmed shifts wait for manual confirmation before
// influencing baselines).
//
// Returns the most recent epoch by start_date when multiple match —
// epochs are non-overlapping by convention, enforced in code at the
// catalogue management layer.
func (s *DB) ResolveSourceEpoch(date string) (string, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	var epoch string
	err := s.pool.QueryRow(ctx, `
		SELECT epoch_id
		  FROM source_epochs
		 WHERE kind = $1
		   AND confirmed = TRUE
		   AND start_date <= $2
		   AND (end_date IS NULL OR end_date >= $2)
		 ORDER BY start_date DESC
		 LIMIT 1
	`, SourceEpochKindIngest, date).Scan(&epoch)
	if err != nil {
		// pgx returns ErrNoRows when there's no match; surface as sentinel.
		return SentinelSourceEpoch, nil
	}
	return epoch, nil
}

// UpsertSourceEpoch inserts or updates a row in source_epochs. Intended
// for catalogue management (manual epoch additions, distribution-shift
// detector). Idempotent on epoch_id. Caller is responsible for the
// non-overlap invariant.
func (s *DB) UpsertSourceEpoch(e SourceEpoch) error {
	if e.EpochID == "" {
		return fmt.Errorf("UpsertSourceEpoch: epoch_id must be non-empty")
	}
	if e.Kind != SourceEpochKindIngest && e.Kind != SourceEpochKindPhysiology {
		return fmt.Errorf("UpsertSourceEpoch: invalid kind %q", e.Kind)
	}
	if e.DetectedBy != DetectedByManual && e.DetectedBy != DetectedByDistributionShift {
		return fmt.Errorf("UpsertSourceEpoch: invalid detected_by %q", e.DetectedBy)
	}

	ctx, cancel := queryCtx()
	defer cancel()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO source_epochs
			(epoch_id, start_date, end_date, kind, description, detected_by, confirmed, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
		ON CONFLICT (epoch_id) DO UPDATE SET
			start_date = excluded.start_date,
			end_date = excluded.end_date,
			kind = excluded.kind,
			description = excluded.description,
			detected_by = excluded.detected_by,
			confirmed = excluded.confirmed,
			updated_at = NOW()
	`,
		e.EpochID, e.StartDate, e.EndDate, e.Kind,
		e.Description, e.DetectedBy, e.Confirmed,
	)
	return err
}

// VerifyReadinessRedesignSchema checks that the four redesign tables
// exist and the bootstrap `initial` source_epoch is in place. Intended
// for callers that need to surface provisioning failures explicitly —
// tenant creation, admin status endpoints. EnsureReadinessRedesignTables
// stays log-and-continue for startup; this method is the strict twin.
//
// Returns nil when the schema is healthy. Returns a wrapped error
// listing which tables/rows are missing when it isn't.
func (s *DB) VerifyReadinessRedesignSchema() error {
	ctx, cancel := queryCtx()
	defer cancel()

	required := []string{
		"source_epochs", "target_snapshots", "feature_snapshots", "naive_baselines",
		"chip_calibrations",
	}
	var missing []string
	for _, t := range required {
		var present bool
		err := s.pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables
				 WHERE table_schema = current_schema()
				   AND table_name = $1
			)
		`, t).Scan(&present)
		if err != nil {
			return fmt.Errorf("VerifyReadinessRedesignSchema: probe %s: %w", t, err)
		}
		if !present {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("VerifyReadinessRedesignSchema: missing tables: %v", missing)
	}

	var hasInitial bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM source_epochs WHERE epoch_id = $1)
	`, InitialSourceEpoch).Scan(&hasInitial); err != nil {
		return fmt.Errorf("VerifyReadinessRedesignSchema: probe initial epoch: %w", err)
	}
	if !hasInitial {
		return fmt.Errorf("VerifyReadinessRedesignSchema: bootstrap %q epoch missing from source_epochs", InitialSourceEpoch)
	}

	// Per-column checks for migrations applied via ALTER (not part of
	// the initial CREATE TABLE). EnsureReadinessRedesignTables logs and
	// continues on ALTER failures, so without these assertions a
	// missing or drifted column would only surface as a
	// SaveNaiveBaseline 500 at write time.
	//
	// We assert the full contract for each column — data type and
	// nullability — because SaveNaiveBaseline depends on reason being
	// nullable TEXT (NOT NULL would block every value-present row;
	// non-TEXT would break the reason-enum write path). information_
	// _schema.columns reports `text` as the canonical type name for
	// Postgres TEXT (other variants like character varying would
	// surface differently and we'd want to know).
	requiredColumns := []struct {
		table, column, wantType string
		wantNullable            bool
	}{
		{"naive_baselines", "reason", "text", true},
		// chip_calibrations audit fields — cutoff/p80/base_rate are
		// nullable because insufficient-data statuses leave them
		// unpopulated; the read path joins on these and would silently
		// degrade if a future ALTER drifted them to NOT NULL.
		{"chip_calibrations", "cutoff", "real", true},
		{"chip_calibrations", "p80", "real", true},
		{"chip_calibrations", "base_rate", "real", true},
		{"chip_calibrations", "status", "text", false},
		{"chip_calibrations", "method", "text", false},
	}
	for _, rc := range requiredColumns {
		var dataType, isNullable string
		err := s.pool.QueryRow(ctx, `
			SELECT data_type, is_nullable
			  FROM information_schema.columns
			 WHERE table_schema = current_schema()
			   AND table_name = $1
			   AND column_name = $2
		`, rc.table, rc.column).Scan(&dataType, &isNullable)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("VerifyReadinessRedesignSchema: column %s.%s is missing", rc.table, rc.column)
		}
		if err != nil {
			return fmt.Errorf("VerifyReadinessRedesignSchema: probe %s.%s: %w", rc.table, rc.column, err)
		}
		if dataType != rc.wantType {
			return fmt.Errorf("VerifyReadinessRedesignSchema: column %s.%s data_type = %q, want %q",
				rc.table, rc.column, dataType, rc.wantType)
		}
		nullable := isNullable == "YES"
		if nullable != rc.wantNullable {
			return fmt.Errorf("VerifyReadinessRedesignSchema: column %s.%s is_nullable = %q, want nullable=%v",
				rc.table, rc.column, isNullable, rc.wantNullable)
		}
	}
	return nil
}

// ListSourceEpochs returns all rows in the catalogue, newest start_date
// first. Used by admin tooling and by the dist-shift detector to find
// pre-existing boundaries before proposing new ones.
func (s *DB) ListSourceEpochs() ([]SourceEpoch, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT epoch_id, start_date, end_date, kind, description, detected_by, confirmed
		  FROM source_epochs
		 ORDER BY start_date DESC, epoch_id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SourceEpoch
	for rows.Next() {
		var e SourceEpoch
		if err := rows.Scan(&e.EpochID, &e.StartDate, &e.EndDate, &e.Kind,
			&e.Description, &e.DetectedBy, &e.Confirmed); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
