package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

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
	hours   float64
	quality string
}

func (n nightCandidate) full() bool {
	return n.CaptureCompleteness == health.NightCaptureComplete && n.hours > 0 && n.quality == "ok"
}

func reconcileCompletedNightSleep(ctx context.Context, catalog sleepNightCatalog, wakeDate string, now time.Time, loc *time.Location) error {
	rows, err := catalog.Query(ctx, `
		SELECT c.wake_date,c.source,c.metric_date,c.source_epoch,c.capture_completeness,
		       c.coverage_generation,c.covered_interval_start,c.covered_interval_end,c.observed_at,c.input_hash,
		       COALESCE(m.qty,0), COALESCE(m.quality,'unknown')
		  FROM night_sleep_coverage_commitments c
		  LEFT JOIN metric_points m ON m.metric_name='night_sleep_total' AND m.date=c.metric_date AND m.source=c.source
		 WHERE c.wake_date=$1`, wakeDate)
	if err != nil {
		return err
	}
	candidates, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (nightCandidate, error) {
		var n nightCandidate
		err := row.Scan(&n.WakeDate, &n.Source, &n.MetricDate, &n.SourceEpoch, &n.CaptureCompleteness,
			&n.CoverageGeneration, &n.CoveredIntervalStart, &n.CoveredIntervalEnd, &n.ObservedAt, &n.InputHash, &n.hours, &n.quality)
		return n, err
	})
	if err != nil || len(candidates) == 0 {
		return err
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
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
		if selected.full() != other.full() || nightSleepSourceRank(other.Source) != nightSleepSourceRank(selected.Source) {
			break
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
	inputHash := fmt.Sprintf("%x", sha256.Sum256([]byte(hashMaterial)))
	canonical, err := health.CanonicalizeCompletedNight(health.CanonicalNightInput{
		WakeDate: wakeDate, DurationHours: selected.hours, Source: selected.Source, SourceEpoch: selected.SourceEpoch,
		InputHash: inputHash, CaptureCompleteness: capture, CoverageGeneration: selected.CoverageGeneration,
		CoveredIntervalStart: &selected.CoveredIntervalStart, CoveredIntervalEnd: &selected.CoveredIntervalEnd,
		ObservedAt: selected.ObservedAt, AlgorithmVersion: CompletedNightSleepAlgorithmVersion,
	}, now, loc)
	if err != nil {
		return err
	}
	return saveCompletedNightSleep(ctx, catalog, canonical, map[string]string{"candidate_state": state, "coverage_generation": selected.CoverageGeneration})
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
	if _, err = tx.Exec(ctx, `
		UPDATE completed_night_sleep
		   SET finalization_state=$1,
		       claim_eligibility=CASE WHEN capture_completeness=$2 AND duration_assessment=$3 THEN $4 ELSE $5 END,
		       finalized_at=COALESCE(finalized_at,$6)
		 WHERE wake_date <= $7 AND finalization_state=$8`,
		health.NightFinalFinal, health.NightCaptureComplete, health.NightDurationPlausible,
		health.NightClaimEligible, health.NightClaimIneligible, now, today, health.NightFinalProvisional); err != nil {
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
