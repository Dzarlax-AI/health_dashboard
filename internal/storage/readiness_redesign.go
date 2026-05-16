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
	"fmt"
	"log"
	"time"
)

// --- Enums (Phase 0 vocabulary) -----------------------------------------

// SubScore identifiers — matches the five-member family in
// READINESS_REDESIGN_PLAN.md §2.
const (
	SubScoreRecoveryStability  = "recovery_stability"
	SubScorePassiveEfficiency  = "passive_efficiency"
	SubScoreAcuteRisk          = "acute_risk"
	SubScoreChronicLoad        = "chronic_load"
	SubScoreAthleticReadiness  = "athletic_readiness"
)

// TargetKind identifiers — the shape of a target_snapshots row's value.
// Multiple shapes can coexist per (date, sub_score); see §4.2 and §9.5.
const (
	TargetKindRolling3d    = "rolling_3d"     // 3-day rolling target (primary for Recovery/Passive)
	TargetKindDailyPoint   = "daily_point"    // single-day value (secondary)
	TargetKindEventT1T3       = "event_t1_t3"        // OR-event in t+1..t+3 (Acute Risk primary): any day in window with HRV drop ≥1.5σ OR RHR spike ≥1.5σ
	TargetKindEventStrictT1T3     = "event_strict_t1_t3"     // AND-event in t+1..t+3 (Acute Risk secondary): some day in window with HRV drop AND RHR spike same day
	TargetKindWoResidual          = "wo_residual"            // per-workout HR residual (Athletic, dormant)
	TargetKindChronicLabel        = "chronic_label"          // sustained-deterioration binary label (Chronic Load primary): ≥5 of 14 forward days breach Recovery 3d-roll EWMA45 by >1σ
	TargetKindChronicAcuteDensity = "chronic_acute_density"  // analysis label (Chronic Load secondary): ≥3 Acute Risk OR-events in t+1..t+14 forward window
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
	EligibilityOK                           = "ok"
	EligibilityOKAwakeStructuralZero        = "ok_awake_structural_zero"
	EligibilityMissingAwakeUnknown          = "missing_awake_unknown"
	EligibilitySleepTotalOutOfRange         = "sleep_total_out_of_range"
	// EligibilitySleepDataMissing splits the old "out_of_range" bucket
	// for nights with no source row at all (Total==NULL). Introduced in
	// recovery_stability formula_version 2 so re-backfilled rows can be
	// distinguished from rows written under the v1 over-broad reason.
	EligibilitySleepDataMissing             = "sleep_data_missing"
	EligibilityCoarseOnlySource             = "coarse_only_source"
	EligibilityNoWalkingSegments            = "no_walking_segments"
	EligibilityNoWalkingHR                  = "no_walking_hr"
	EligibilityWalkingHROutOfRange          = "walking_hr_out_of_range"
	// EligibilityEventWindowDataMissing fires when a forward-looking
	// event-classifier target (Acute Risk) cannot honestly write a
	// negative label because at least one day in the t+1..t+3 window
	// has no observable signal. Otherwise a sensor gap would be
	// silently coded as "no breach".
	EligibilityEventWindowDataMissing       = "event_window_data_missing"
	EligibilityValueOutOfRange              = "value_out_of_range"
	EligibilityHRVSparse                    = "hrv_sparse"
	EligibilityBaselineWarmup               = "baseline_warmup"
	EligibilityImporterGap                  = "importer_gap"
	EligibilityWalkingOnly                  = "walking_only"
	EligibilityWASORequiresSegments         = "waso_requires_segments"
	EligibilityFragmentationRequiresSegments = "fragmentation_requires_segments"
	EligibilityDataAnomaly2024              = "data_anomaly_2024"
	EligibilityNoStructuredWorkouts         = "no_structured_workouts"
)

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
	TargetKindRolling3d: {}, TargetKindDailyPoint: {},
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

// IsSubScoreValid returns true for the five sub-scores defined in §2.
func IsSubScoreValid(s string) bool { _, ok := validSubScores[s]; return ok }

// IsTargetKindValid returns true for the recognised target_kind values.
func IsTargetKindValid(s string) bool { _, ok := validTargetKinds[s]; return ok }

// IsBaselineKindValid returns true for the recognised baseline_kind values.
func IsBaselineKindValid(s string) bool { _, ok := validBaselineKinds[s]; return ok }

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
	}
	for _, q := range stmts {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			log.Printf("EnsureReadinessRedesignTables: %v", err)
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
		log.Printf("EnsureReadinessRedesignTables seed initial epoch: %v", err)
	}
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
	// Joint-state guard: reason is meaningful only when value is NULL.
	// Production callers shouldn't trip this, but it catches future
	// regressions where a writer accidentally fills in both fields.
	if b.PredictedValue != nil && b.Reason != "" {
		return fmt.Errorf("SaveNaiveBaseline: reason %q set on non-nil predicted_value", b.Reason)
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
