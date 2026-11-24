package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"health-receiver/experiments/dbt_metrics"
	"health-receiver/internal/health"

	"github.com/jackc/pgx/v5"
)

const CompletedNightSleepAlgorithmVersion = "completed-night-sleep-v1"

// NightSleepCoverageCommitment is the versioned adapter acknowledgement that
// a bounded overnight interval was read in one sync generation. It is not a
// claim made by an upstream wearable vendor.
type NightSleepCoverageCommitment struct {
	WakeDate             string
	MetricDate           string
	Source               string
	SourceEpoch          string
	CaptureCompleteness  string
	CoverageGeneration   string
	CoveredIntervalStart time.Time
	CoveredIntervalEnd   time.Time
	ObservedAt           time.Time
	InputHash            string
}

func (s *DB) EnsureCompletedNightSleepTableContext(ctx context.Context) error {
	for _, ddl := range []string{`
		CREATE TABLE IF NOT EXISTS completed_night_sleep (
			wake_date               TEXT PRIMARY KEY,
			duration_hours          DOUBLE PRECISION NOT NULL,
			source                  TEXT NOT NULL,
			source_epoch            TEXT NOT NULL,
			input_hash              TEXT NOT NULL,
			capture_completeness    TEXT NOT NULL,
			duration_assessment     TEXT NOT NULL,
			finalization_state      TEXT NOT NULL,
			claim_eligibility       TEXT NOT NULL,
			coverage_generation     TEXT,
			covered_interval_start  TIMESTAMPTZ,
			covered_interval_end    TIMESTAMPTZ,
			observed_at             TIMESTAMPTZ NOT NULL,
			finalized_at            TIMESTAMPTZ,
			algorithm_version       TEXT NOT NULL,
			 diagnostics             JSONB NOT NULL DEFAULT '{}'::jsonb
		)
	`, `
		CREATE TABLE IF NOT EXISTS night_sleep_coverage_commitments (
			wake_date               TEXT NOT NULL,
			source                  TEXT NOT NULL,
			metric_date             TEXT NOT NULL,
			source_epoch            TEXT NOT NULL,
			capture_completeness    TEXT NOT NULL,
			coverage_generation     TEXT NOT NULL,
			covered_interval_start  TIMESTAMPTZ NOT NULL,
			covered_interval_end    TIMESTAMPTZ NOT NULL,
			observed_at             TIMESTAMPTZ NOT NULL,
			input_hash              TEXT NOT NULL,
			PRIMARY KEY (wake_date, source)
		)
	`} {
		if _, err := s.pool.Exec(ctx, ddl); err != nil {
			return err
		}
	}
	return nil
}

func (s *DB) SaveNightSleepCoverageCommitment(ctx context.Context, commitment NightSleepCoverageCommitment) error {
	if commitment.WakeDate == "" || commitment.MetricDate == "" || commitment.Source == "" || commitment.SourceEpoch == "" || commitment.CoverageGeneration == "" || commitment.InputHash == "" || commitment.ObservedAt.IsZero() || !commitment.CoveredIntervalEnd.After(commitment.CoveredIntervalStart) {
		return fmt.Errorf("invalid night sleep coverage commitment")
	}
	if commitment.CaptureCompleteness != health.NightCaptureComplete && commitment.CaptureCompleteness != health.NightCapturePartial {
		return fmt.Errorf("invalid night sleep coverage completeness %q", commitment.CaptureCompleteness)
	}
	if err := s.validateNightSleepCoverageCommitment(commitment); err != nil {
		return err
	}
	return saveNightSleepCoverage(ctx, s.pool, commitment)
}

func saveNightSleepCoverage(ctx context.Context, writer ContractCatalog, commitment NightSleepCoverageCommitment) error {
	_, err := writer.Exec(ctx, `
		INSERT INTO night_sleep_coverage_commitments(
			wake_date,source,metric_date,source_epoch,capture_completeness,
			coverage_generation,covered_interval_start,covered_interval_end,observed_at,input_hash
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT(wake_date,source) DO UPDATE SET
			metric_date=excluded.metric_date,
			source_epoch=excluded.source_epoch,
			capture_completeness=excluded.capture_completeness,
			coverage_generation=excluded.coverage_generation,
			covered_interval_start=excluded.covered_interval_start,
			covered_interval_end=excluded.covered_interval_end,
			observed_at=excluded.observed_at,
			input_hash=excluded.input_hash
		WHERE night_sleep_coverage_commitments.input_hash IS DISTINCT FROM excluded.input_hash`,
		commitment.WakeDate, commitment.Source, commitment.MetricDate, commitment.SourceEpoch,
		commitment.CaptureCompleteness, commitment.CoverageGeneration, commitment.CoveredIntervalStart,
		commitment.CoveredIntervalEnd, commitment.ObservedAt, commitment.InputHash)
	return err
}

// validateNightSleepCoverageCommitment ties a controlled adapter's metadata to
// the tenant-local wake day. A syntactically valid but misdated interval must
// never promote an unrelated raw point into the canonical B0 evidence set.
func (s *DB) validateNightSleepCoverageCommitment(commitment NightSleepCoverageCommitment) error {
	return validateNightSleepWindow(commitment, s.reportTZLocation())
}

func validateNightSleepWindow(commitment NightSleepCoverageCommitment, loc *time.Location) error {
	wakeDay, err := time.ParseInLocation("2006-01-02", commitment.WakeDate, loc)
	if err != nil {
		return fmt.Errorf("invalid night sleep coverage wake date: %w", err)
	}
	metricAt, err := parseMetricDate(commitment.MetricDate)
	if err != nil {
		return fmt.Errorf("invalid night sleep coverage metric date: %w", err)
	}
	if metricAt.In(loc).Format("2006-01-02") != commitment.WakeDate {
		return fmt.Errorf("night sleep coverage metric date does not match wake date")
	}
	expectedEnd := time.Date(wakeDay.Year(), wakeDay.Month(), wakeDay.Day(), 12, 0, 0, 0, loc)
	expectedStart := expectedEnd.AddDate(0, 0, -1)
	if !commitment.CoveredIntervalStart.Equal(expectedStart) || !commitment.CoveredIntervalEnd.Equal(expectedEnd) {
		return fmt.Errorf("night sleep coverage interval does not match tenant-local noon-to-noon wake window")
	}
	if metricAt.Before(commitment.CoveredIntervalStart) || !metricAt.Before(commitment.CoveredIntervalEnd) {
		return fmt.Errorf("night sleep coverage metric date is outside covered interval")
	}
	return nil
}

// ReconcileCompletedNightSleep selects the highest-priority committed source
// and joins it to the exact raw night_sleep_total point named by the adapter.
// Legacy sleep aggregates and naps are intentionally absent from this query.
type sleepNightCatalog interface {
	ContractCatalog
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func (s *DB) ReconcileCompletedNightSleep(ctx context.Context, wakeDate string, now time.Time) error {
	loc := s.reportTZLocation()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockNightSleepIngest(ctx, tx); err != nil {
		return err
	}
	if err = reconcileCompletedNightSleep(ctx, tx, wakeDate, now, loc); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type nightCandidate struct {
	NightSleepCoverageCommitment
	hours             float64
	quality           string
	durationPlausible bool
}

func (n nightCandidate) full() bool {
	return n.CaptureCompleteness == health.NightCaptureComplete && n.hours > 0 && n.quality == "ok"
}

func reconcileCompletedNightSleep(ctx context.Context, catalog sleepNightCatalog, wakeDate string, now time.Time, loc *time.Location) error {
	assessment := dbtmetrics.CompletedNightDurationAssessmentSQL("COALESCE(m.qty,0)")
	rows, err := catalog.Query(ctx, `
		SELECT c.wake_date,c.source,c.metric_date,c.source_epoch,c.capture_completeness,
		       c.coverage_generation,c.covered_interval_start,c.covered_interval_end,c.observed_at,c.input_hash,
		       COALESCE(m.qty,0), COALESCE(m.quality,'unknown'), (`+assessment+`='plausible')
		  FROM night_sleep_coverage_commitments c
		  LEFT JOIN metric_points m ON m.metric_name='night_sleep_total' AND m.date=c.metric_date AND m.source=c.source
		 WHERE c.wake_date=$1`, wakeDate)
	if err != nil {
		return err
	}
	candidates, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (nightCandidate, error) {
		var n nightCandidate
		err := row.Scan(&n.WakeDate, &n.Source, &n.MetricDate, &n.SourceEpoch, &n.CaptureCompleteness,
			&n.CoverageGeneration, &n.CoveredIntervalStart, &n.CoveredIntervalEnd, &n.ObservedAt, &n.InputHash, &n.hours, &n.quality, &n.durationPlausible)
		return n, err
	})
	if err != nil || len(candidates) == 0 {
		return err
	}
	preferred, err := readPreferredSleepSource(ctx, catalog)
	if err != nil {
		return err
	}
	qualifies := func(n nightCandidate) bool {
		return n.full() && n.durationPlausible
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if preferred != "" {
			if qualifies(left) != qualifies(right) {
				return qualifies(left)
			}
			if qualifies(left) && (left.Source == preferred) != (right.Source == preferred) {
				return left.Source == preferred
			}
		}
		if left.full() != right.full() {
			return left.full()
		}
		if nightSleepSourceRank(left.Source) != nightSleepSourceRank(right.Source) {
			return nightSleepSourceRank(left.Source) < nightSleepSourceRank(right.Source)
		}
		return left.Source < right.Source
	})
	selected := candidates[0]
	capture, state := selected.CaptureCompleteness, "selected"
	if selected.hours <= 0 {
		capture, state = health.NightCaptureUnknown, "missing"
	} else if selected.quality != "ok" {
		capture, state = health.NightCaptureUnknown, "quality_not_ok"
	}
	for _, other := range candidates[1:] {
		// An explicit eligible source has a rank of its own.
		if preferred != "" && qualifies(selected) && selected.Source == preferred {
			break
		}
		if preferred != "" && qualifies(other) != qualifies(selected) {
			continue
		}
		if selected.full() != other.full() || nightSleepSourceRank(other.Source) != nightSleepSourceRank(selected.Source) {
			continue
		}
		if other.SourceEpoch == selected.SourceEpoch && other.hours > 0 && other.quality == "ok" &&
			(other.MetricDate != selected.MetricDate || other.hours != selected.hours) {
			capture, state = health.NightCaptureUnknown, "conflicting"
			break
		}
	}
	hashMaterial := fmt.Sprintf("%s\x1f%s\x1f%s\x1f%.9g\x1f%s", selected.InputHash, selected.MetricDate, selected.Source, selected.hours, selected.quality)
	if state == "conflicting" {
		hashMaterial += "\x1f" + state
	}
	reason := "automatic"
	if preferred != "" {
		reason = "preferred_unavailable"
		if selected.Source == preferred && qualifies(selected) && state == "selected" {
			reason = "preferred"
		}
		hashMaterial += "\x1f" + preferred + "\x1f" + reason
	}
	inputHash := fmt.Sprintf("%x", sha256.Sum256([]byte(hashMaterial)))
	input := health.CanonicalNightInput{
		WakeDate: wakeDate, DurationHours: selected.hours, Source: selected.Source, SourceEpoch: selected.SourceEpoch,
		InputHash: inputHash, CaptureCompleteness: capture, CoverageGeneration: selected.CoverageGeneration,
		CoveredIntervalStart: &selected.CoveredIntervalStart, CoveredIntervalEnd: &selected.CoveredIntervalEnd,
		ObservedAt: selected.ObservedAt, AlgorithmVersion: CompletedNightSleepAlgorithmVersion,
	}
	canonical, err := canonicalizeCompletedNight(ctx, catalog, input, now, loc, nil, nil)
	if err != nil {
		return err
	}
	return saveCompletedNightSleep(ctx, catalog, canonical, map[string]string{"preferred_sleep_source": preferred, "source_selection_reason": reason, "candidate_state": state, "coverage_generation": selected.CoverageGeneration})
}

type completedNightQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// canonicalizeCompletedNight executes the same dbt macro used by the native
// model. Validation remains at the adapter boundary; state and assessments
// come from SQL inside the caller's transaction.
func canonicalizeCompletedNight(ctx context.Context, queryer completedNightQueryer, in health.CanonicalNightInput, now time.Time, loc *time.Location, existingFinalizedAt, finalizedAtFallback *time.Time) (health.CompletedNightSleep, error) {
	if loc == nil {
		return health.CompletedNightSleep{}, fmt.Errorf("night canonicalization requires a tenant timezone")
	}
	if _, err := time.ParseInLocation("2006-01-02", in.WakeDate, loc); err != nil {
		return health.CompletedNightSleep{}, fmt.Errorf("invalid wake date: %w", err)
	}
	if in.Source == "" || in.SourceEpoch == "" || in.InputHash == "" || in.AlgorithmVersion == "" {
		return health.CompletedNightSleep{}, fmt.Errorf("night canonicalization requires source, epoch, input hash, and algorithm version")
	}
	if in.ObservedAt.IsZero() {
		return health.CompletedNightSleep{}, fmt.Errorf("night canonicalization requires observed time")
	}
	if in.CaptureCompleteness == health.NightCaptureComplete && (in.CoverageGeneration == "" || in.CoveredIntervalStart == nil || in.CoveredIntervalEnd == nil || !in.CoveredIntervalEnd.After(*in.CoveredIntervalStart)) {
		return health.CompletedNightSleep{}, fmt.Errorf("complete night requires a valid coverage commitment")
	}
	if in.CaptureCompleteness != "" && in.CaptureCompleteness != health.NightCaptureComplete && in.CaptureCompleteness != health.NightCapturePartial && in.CaptureCompleteness != health.NightCaptureUnknown {
		return health.CompletedNightSleep{}, fmt.Errorf("invalid capture completeness %q", in.CaptureCompleteness)
	}
	start, end := any(nil), any(nil)
	if in.CoveredIntervalStart != nil {
		start = *in.CoveredIntervalStart
	}
	if in.CoveredIntervalEnd != nil {
		end = *in.CoveredIntervalEnd
	}
	sql := dbtmetrics.CompletedNightSleepSQL(`(select $1::text as wake_date,$2::float8 as duration_hours,$3::text as capture_completeness,$4::text as coverage_generation,$5::timestamptz as covered_interval_start,$6::timestamptz as covered_interval_end,$7::timestamptz as decision_at,$8::text as zone,$9::timestamptz as existing_finalized_at,$10::timestamptz as finalized_at_fallback,false as persisted_state,null::text as persisted_capture_completeness,null::text as persisted_duration_assessment)`)
	var capture, assessment, finality, eligibility string
	var finalizedAt *time.Time
	err := queryer.QueryRow(ctx, `select capture_state,duration_assessment,finalization_state,claim_eligibility,finalized_at from (`+sql+`) canonical`, in.WakeDate, in.DurationHours, in.CaptureCompleteness, in.CoverageGeneration, start, end, now, loc.String(), existingFinalizedAt, finalizedAtFallback).Scan(&capture, &assessment, &finality, &eligibility, &finalizedAt)
	if err != nil {
		return health.CompletedNightSleep{}, err
	}
	return health.CompletedNightSleep{WakeDate: in.WakeDate, DurationHours: in.DurationHours, Source: in.Source, SourceEpoch: in.SourceEpoch, InputHash: in.InputHash, CaptureCompleteness: capture, DurationAssessment: assessment, FinalizationState: finality, ClaimEligibility: eligibility, ObservedAt: in.ObservedAt, FinalizedAt: finalizedAt, AlgorithmVersion: in.AlgorithmVersion, CoverageGeneration: in.CoverageGeneration, CoveredIntervalStart: in.CoveredIntervalStart, CoveredIntervalEnd: in.CoveredIntervalEnd}, nil
}

func nightSleepSourceRank(source string) int {
	switch {
	case strings.Contains(source, "Ultra") || strings.Contains(source, "Apple Watch"):
		return 0
	case strings.Contains(source, "iPhone"):
		return 1
	case strings.Contains(source, "RingConn"):
		return 2
	default:
		return 3
	}
}

// SaveCompletedNightSleep is an idempotent canonical write. An exact replay
// does not alter observed/finalized facts; a new input hash replaces the row,
// reopening it through the canonicalizer's state rather than silently keeping
// an obsolete final claim.
func (s *DB) SaveCompletedNightSleep(ctx context.Context, night health.CompletedNightSleep, diagnostics any) error {
	return saveCompletedNightSleep(ctx, s.pool, night, diagnostics)
}

func saveCompletedNightSleep(ctx context.Context, writer ContractCatalog, night health.CompletedNightSleep, diagnostics any) error {
	if night.WakeDate == "" || night.InputHash == "" || night.AlgorithmVersion == "" {
		return fmt.Errorf("invalid completed night identity")
	}
	payload, err := json.Marshal(diagnostics)
	if err != nil {
		return fmt.Errorf("marshal completed night diagnostics: %w", err)
	}
	_, err = writer.Exec(ctx, `
		INSERT INTO completed_night_sleep(
			wake_date,duration_hours,source,source_epoch,input_hash,capture_completeness,
			duration_assessment,finalization_state,claim_eligibility,coverage_generation,
			covered_interval_start,covered_interval_end,observed_at,finalized_at,
			algorithm_version,diagnostics
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''),$11,$12,$13,$14,$15,$16)
		ON CONFLICT(wake_date) DO UPDATE SET
			duration_hours=excluded.duration_hours,
			source=excluded.source,
			source_epoch=excluded.source_epoch,
			input_hash=excluded.input_hash,
			capture_completeness=excluded.capture_completeness,
			duration_assessment=excluded.duration_assessment,
			finalization_state=excluded.finalization_state,
			claim_eligibility=excluded.claim_eligibility,
			coverage_generation=excluded.coverage_generation,
			covered_interval_start=excluded.covered_interval_start,
			covered_interval_end=excluded.covered_interval_end,
			observed_at=excluded.observed_at,
			finalized_at=excluded.finalized_at,
			algorithm_version=excluded.algorithm_version,
			diagnostics=COALESCE(NULLIF(completed_night_sleep.diagnostics,'null'::jsonb),'{}'::jsonb) || COALESCE(NULLIF(excluded.diagnostics,'null'::jsonb),'{}'::jsonb)
		WHERE completed_night_sleep.input_hash IS DISTINCT FROM excluded.input_hash
			OR completed_night_sleep.algorithm_version IS DISTINCT FROM excluded.algorithm_version`,
		night.WakeDate, night.DurationHours, night.Source, night.SourceEpoch, night.InputHash,
		night.CaptureCompleteness, night.DurationAssessment, night.FinalizationState, night.ClaimEligibility,
		night.CoverageGeneration, night.CoveredIntervalStart, night.CoveredIntervalEnd, night.ObservedAt,
		night.FinalizedAt, night.AlgorithmVersion, json.RawMessage(payload))
	return err
}

type completedNightSleepQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func (s *DB) ListCompletedNightSleep(ctx context.Context, fromDate, throughDate string) ([]health.CompletedNightSleep, error) {
	return listCompletedNightSleep(ctx, s.pool, fromDate, throughDate)
}

func listCompletedNightSleep(ctx context.Context, queryer completedNightSleepQueryer, fromDate, throughDate string) ([]health.CompletedNightSleep, error) {
	rows, err := queryer.Query(ctx, `
		SELECT wake_date,duration_hours,source,source_epoch,input_hash,capture_completeness,
		       duration_assessment,finalization_state,claim_eligibility,
		       COALESCE(coverage_generation,''),covered_interval_start,covered_interval_end,
		       observed_at,finalized_at,algorithm_version
		  FROM completed_night_sleep
		 WHERE wake_date >= $1 AND wake_date <= $2
		 ORDER BY wake_date`, fromDate, throughDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nights := make([]health.CompletedNightSleep, 0)
	for rows.Next() {
		var night health.CompletedNightSleep
		if err := rows.Scan(&night.WakeDate, &night.DurationHours, &night.Source, &night.SourceEpoch,
			&night.InputHash, &night.CaptureCompleteness, &night.DurationAssessment, &night.FinalizationState,
			&night.ClaimEligibility, &night.CoverageGeneration, &night.CoveredIntervalStart,
			&night.CoveredIntervalEnd, &night.ObservedAt, &night.FinalizedAt, &night.AlgorithmVersion); err != nil {
			return nil, err
		}
		nights = append(nights, night)
	}
	return nights, rows.Err()
}

// FinalizeCompletedNightSleep marks eligible canonical rows final at the
// tenant-local 18:00 boundary. It never invents completeness from legacy data.
func (s *DB) FinalizeCompletedNightSleep(ctx context.Context, now time.Time) error {
	loc := s.reportTZLocation()
	today := now.In(loc).Format("2006-01-02")
	if now.In(loc).Hour() < 18 {
		today = now.In(loc).AddDate(0, 0, -1).Format("2006-01-02")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockNightSleepIngest(ctx, tx); err != nil {
		return err
	}
	input := `(select wake_date,duration_hours,capture_completeness,coverage_generation,covered_interval_start,covered_interval_end,$2::timestamptz as decision_at,$3::text as zone,finalized_at as existing_finalized_at,$2::timestamptz as finalized_at_fallback,true as persisted_state,capture_completeness as persisted_capture_completeness,duration_assessment as persisted_duration_assessment from locked)`
	canonical := dbtmetrics.CompletedNightSleepSQL(input)
	if _, err = tx.Exec(ctx, `
		WITH locked AS MATERIALIZED (
			SELECT wake_date,duration_hours,capture_completeness,duration_assessment,COALESCE(coverage_generation,'') AS coverage_generation,
			       covered_interval_start,covered_interval_end,finalized_at,input_hash,algorithm_version
			  FROM completed_night_sleep
			 WHERE wake_date <= $1 AND finalization_state=$4
			 ORDER BY wake_date FOR UPDATE
		), canonical AS MATERIALIZED (`+canonical+`)
		UPDATE completed_night_sleep target
		   SET finalization_state=canonical.finalization_state,
		       claim_eligibility=canonical.claim_eligibility,
		       finalized_at=canonical.finalized_at
		  FROM canonical
		 WHERE target.wake_date=canonical.wake_date
		   AND target.finalization_state=$4`, today, now, loc.String(), health.NightFinalProvisional); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReconcileRecentCompletedNightSleep replays only adapter commitments in the
// bounded B0 evidence horizon. It never synthesizes records from legacy
// aggregates, so an installation without the controlled adapter stays in the
// factual answer ladder instead of acquiring a false sleep history.
func (s *DB) ReconcileRecentCompletedNightSleep(ctx context.Context, now time.Time) error {
	loc := s.reportTZLocation()
	through := now.In(loc).Format("2006-01-02")
	from := now.In(loc).AddDate(0, 0, -107).Format("2006-01-02")
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT wake_date
		  FROM night_sleep_coverage_commitments
		 WHERE wake_date >= $1 AND wake_date <= $2
		 ORDER BY wake_date`, from, through)
	if err != nil {
		return err
	}
	defer rows.Close()
	wakeDates := make([]string, 0)
	for rows.Next() {
		var wakeDate string
		if err := rows.Scan(&wakeDate); err != nil {
			return err
		}
		wakeDates = append(wakeDates, wakeDate)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, wakeDate := range wakeDates {
		if err := s.ReconcileCompletedNightSleep(ctx, wakeDate, now); err != nil {
			return fmt.Errorf("reconcile %s: %w", wakeDate, err)
		}
	}
	return s.FinalizeCompletedNightSleep(ctx, now)
}

// EvaluateRecentSleepBelowReference loads only the bounded B0 evidence window;
// it does not scan raw points or treat the dashboard sleep card as a fallback.
func (s *DB) EvaluateRecentSleepBelowReference(ctx context.Context, wakeDate string, now time.Time) (health.RecentSleepBelowReference, error) {
	loc := s.reportTZLocation()
	date, err := time.ParseInLocation("2006-01-02", wakeDate, loc)
	if err != nil {
		return health.RecentSleepBelowReference{State: health.RecentSleepClaimUnknown, Reason: "invalid_wake_date"}, nil
	}
	records, err := s.ListCompletedNightSleep(ctx, date.AddDate(0, 0, -100).Format("2006-01-02"), wakeDate)
	if err != nil {
		return health.RecentSleepBelowReference{}, err
	}
	return health.EvaluateRecentSleepBelowReference(records, wakeDate, now, loc), nil
}
