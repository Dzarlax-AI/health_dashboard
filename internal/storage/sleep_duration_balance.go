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

// SleepDurationBalanceAlgorithmVersion identifies the transparent accounting
// implementation. It is deliberately separate from any future model of sleep
// need, readiness, or recommendation.
const SleepDurationBalanceAlgorithmVersion = "sleep-duration-balance-v1"

// SleepPeriodCoverageCommitment attests that the controlled adapter read one
// complete tenant-local noon-to-noon window. A complete window may contain no
// sleep episodes; absent coverage remains unknown rather than zero sleep.
type SleepPeriodCoverageCommitment struct {
	WakeDate             string
	SourceEpoch          string
	CaptureCompleteness  string
	CoverageGeneration   string
	CoveredIntervalStart time.Time
	CoveredIntervalEnd   time.Time
	ObservedAt           time.Time
	InputHash            string
}

// CompletedSleepEpisodeCommitment is a selected, non-overlapping asleep
// interval belonging to one fully covered balance period. It intentionally
// differs from completed_night_sleep: naps are first-class inputs here.
type CompletedSleepEpisodeCommitment struct {
	EpisodeID          string
	WakeDate           string
	Start              time.Time
	End                time.Time
	Source             string
	SourceEpoch        string
	InputHash          string
	CoverageGeneration string
	CaptureState       string
	DurationAssessment string
	ObservedAt         time.Time
}

func (s *DB) EnsureSleepDurationBalanceTablesContext(ctx context.Context) error {
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS sleep_period_coverage (
			wake_date               TEXT PRIMARY KEY,
			source_epoch            TEXT NOT NULL,
			capture_completeness    TEXT NOT NULL,
			coverage_generation     TEXT NOT NULL,
			covered_interval_start  TIMESTAMPTZ NOT NULL,
			covered_interval_end    TIMESTAMPTZ NOT NULL,
			observed_at             TIMESTAMPTZ NOT NULL,
			input_hash              TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS completed_sleep_episode (
			episode_id              TEXT PRIMARY KEY,
			wake_date               TEXT NOT NULL,
			start_at                TIMESTAMPTZ NOT NULL,
			end_at                  TIMESTAMPTZ NOT NULL,
			source                  TEXT NOT NULL,
			source_epoch            TEXT NOT NULL,
			input_hash              TEXT NOT NULL,
			coverage_generation     TEXT NOT NULL,
			capture_state           TEXT NOT NULL,
			duration_assessment     TEXT NOT NULL,
			observed_at             TIMESTAMPTZ NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_completed_sleep_episode_wake_start
			ON completed_sleep_episode(wake_date, start_at)`,
		`CREATE TABLE IF NOT EXISTS sleep_goal (
			effective_date          TEXT PRIMARY KEY,
			goal_hours              DOUBLE PRECISION NOT NULL,
			version                 TEXT NOT NULL,
			created_at              TIMESTAMPTZ NOT NULL,
			updated_at              TIMESTAMPTZ NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sleep_duration_balance_snapshot (
			wake_date               TEXT PRIMARY KEY,
			window_start_date       TEXT NOT NULL,
			calculated_through      TIMESTAMPTZ NOT NULL,
			state                   TEXT NOT NULL,
			confidence              TEXT NOT NULL,
			incomplete_reason       TEXT NOT NULL DEFAULT '',
			last_complete_date      TEXT NOT NULL DEFAULT '',
			balance_hours           DOUBLE PRECISION,
			periods                 JSONB NOT NULL,
			input_hash              TEXT NOT NULL,
			algorithm_version       TEXT NOT NULL,
			calculated_at           TIMESTAMPTZ NOT NULL
		)`,
	} {
		if _, err := s.pool.Exec(ctx, ddl); err != nil {
			return err
		}
	}
	return nil
}

// ApplySleepPeriodSnapshot atomically replaces one covered period's selected
// episodes. A replay with the same exact input hash is a no-op; a correction
// never leaves mixed generations behind.
func (s *DB) ApplySleepPeriodSnapshot(ctx context.Context, coverage SleepPeriodCoverageCommitment, episodes []CompletedSleepEpisodeCommitment) (bool, error) {
	if err := validateSleepPeriodCoverageCommitment(coverage); err != nil {
		return false, err
	}
	if err := validateCompletedSleepEpisodeCommitments(coverage, episodes); err != nil {
		return false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	var existingHash string
	err = tx.QueryRow(ctx, `SELECT input_hash FROM sleep_period_coverage WHERE wake_date=$1 FOR UPDATE`, coverage.WakeDate).Scan(&existingHash)
	if err != nil && err != pgx.ErrNoRows {
		return false, err
	}
	if existingHash == coverage.InputHash {
		if err := tx.Commit(ctx); err != nil {
			return false, err
		}
		return false, nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO sleep_period_coverage(
			wake_date,source_epoch,capture_completeness,coverage_generation,
			covered_interval_start,covered_interval_end,observed_at,input_hash
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT(wake_date) DO UPDATE SET
			source_epoch=excluded.source_epoch,
			capture_completeness=excluded.capture_completeness,
			coverage_generation=excluded.coverage_generation,
			covered_interval_start=excluded.covered_interval_start,
			covered_interval_end=excluded.covered_interval_end,
			observed_at=excluded.observed_at,
			input_hash=excluded.input_hash`,
		coverage.WakeDate, coverage.SourceEpoch, coverage.CaptureCompleteness, coverage.CoverageGeneration,
		coverage.CoveredIntervalStart, coverage.CoveredIntervalEnd, coverage.ObservedAt, coverage.InputHash,
	); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM completed_sleep_episode WHERE wake_date=$1`, coverage.WakeDate); err != nil {
		return false, err
	}
	for _, episode := range episodes {
		if _, err := tx.Exec(ctx, `
			INSERT INTO completed_sleep_episode(
				episode_id,wake_date,start_at,end_at,source,source_epoch,input_hash,
				coverage_generation,capture_state,duration_assessment,observed_at
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			episode.EpisodeID, episode.WakeDate, episode.Start, episode.End, episode.Source,
			episode.SourceEpoch, episode.InputHash, episode.CoverageGeneration, episode.CaptureState,
			episode.DurationAssessment, episode.ObservedAt,
		); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (s *DB) SaveSleepGoal(ctx context.Context, goal health.SleepGoal, now time.Time) error {
	if err := validateSleepGoalsForStorage([]health.SleepGoal{goal}); err != nil {
		return err
	}
	if now.IsZero() {
		return fmt.Errorf("sleep goal requires an update timestamp")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sleep_goal(effective_date,goal_hours,version,created_at,updated_at)
		VALUES($1,$2,$3,$4,$4)
		ON CONFLICT(effective_date) DO UPDATE SET
			goal_hours=excluded.goal_hours,
			version=excluded.version,
			updated_at=excluded.updated_at
		WHERE sleep_goal.goal_hours IS DISTINCT FROM excluded.goal_hours
			OR sleep_goal.version IS DISTINCT FROM excluded.version`,
		goal.EffectiveDate, goal.Hours, goal.Version, now.UTC())
	return err
}

func (s *DB) GetEffectiveSleepGoal(ctx context.Context, date string) (*health.SleepGoal, error) {
	var goal health.SleepGoal
	err := s.pool.QueryRow(ctx, `
		SELECT effective_date,goal_hours,version
		  FROM sleep_goal WHERE effective_date <= $1
		 ORDER BY effective_date DESC LIMIT 1`, date).Scan(&goal.EffectiveDate, &goal.Hours, &goal.Version)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &goal, nil
}

func (s *DB) ReconcileSleepDurationBalance(ctx context.Context, wakeDate string, now time.Time) (health.SleepDurationBalance, error) {
	loc := s.reportTZLocation()
	endDay, err := time.ParseInLocation("2006-01-02", wakeDate, loc)
	if err != nil {
		return health.SleepDurationBalance{}, fmt.Errorf("invalid balance wake date: %w", err)
	}
	windowStart := endDay.AddDate(0, 0, -(health.SleepDurationBalanceWindowDays - 1)).Format("2006-01-02")
	goals, err := s.listSleepGoals(ctx, wakeDate)
	if err != nil {
		return health.SleepDurationBalance{}, err
	}
	coverage, err := s.listSleepPeriodCoverage(ctx, windowStart, wakeDate)
	if err != nil {
		return health.SleepDurationBalance{}, err
	}
	episodes, err := s.listCompletedSleepEpisodes(ctx, windowStart, wakeDate, loc)
	if err != nil {
		return health.SleepDurationBalance{}, err
	}
	result, err := health.CalculateSleepDurationBalance(wakeDate, goals, coverage, episodes, loc)
	if err != nil {
		return health.SleepDurationBalance{}, err
	}
	if err := s.saveSleepDurationBalanceSnapshot(ctx, result, goals, coverage, episodes, now); err != nil {
		return health.SleepDurationBalance{}, err
	}
	return result, nil
}

func (s *DB) listSleepGoals(ctx context.Context, through string) ([]health.SleepGoal, error) {
	rows, err := s.pool.Query(ctx, `SELECT effective_date,goal_hours,version FROM sleep_goal WHERE effective_date <= $1 ORDER BY effective_date`, through)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var goals []health.SleepGoal
	for rows.Next() {
		var goal health.SleepGoal
		if err := rows.Scan(&goal.EffectiveDate, &goal.Hours, &goal.Version); err != nil {
			return nil, err
		}
		goals = append(goals, goal)
	}
	return goals, rows.Err()
}

func (s *DB) listSleepPeriodCoverage(ctx context.Context, from, through string) ([]health.SleepBalanceCoverage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT wake_date,capture_completeness,coverage_generation
		  FROM sleep_period_coverage
		 WHERE wake_date >= $1 AND wake_date <= $2
		 ORDER BY wake_date`, from, through)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var coverage []health.SleepBalanceCoverage
	for rows.Next() {
		var value health.SleepBalanceCoverage
		if err := rows.Scan(&value.WakeDate, &value.CaptureState, &value.CoverageGeneration); err != nil {
			return nil, err
		}
		coverage = append(coverage, value)
	}
	return coverage, rows.Err()
}

func (s *DB) listCompletedSleepEpisodes(ctx context.Context, from, through string, loc *time.Location) ([]health.CompletedSleepEpisode, error) {
	startDay, err := time.ParseInLocation("2006-01-02", from, loc)
	if err != nil {
		return nil, err
	}
	endDay, err := time.ParseInLocation("2006-01-02", through, loc)
	if err != nil {
		return nil, err
	}
	windowStart := time.Date(startDay.Year(), startDay.Month(), startDay.Day()-1, 12, 0, 0, 0, loc)
	windowEnd := time.Date(endDay.Year(), endDay.Month(), endDay.Day(), 12, 0, 0, 0, loc)
	rows, err := s.pool.Query(ctx, `
		SELECT episode_id,start_at,end_at,source,source_epoch,input_hash,coverage_generation,capture_state,duration_assessment
		  FROM completed_sleep_episode
		 WHERE end_at > $1 AND start_at < $2
		 ORDER BY start_at,episode_id`, windowStart, windowEnd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var episodes []health.CompletedSleepEpisode
	for rows.Next() {
		var episode health.CompletedSleepEpisode
		if err := rows.Scan(&episode.ID, &episode.Start, &episode.End, &episode.Source, &episode.SourceEpoch,
			&episode.InputHash, &episode.CoverageGeneration, &episode.CaptureState, &episode.DurationAssessment); err != nil {
			return nil, err
		}
		episodes = append(episodes, episode)
	}
	return episodes, rows.Err()
}

func (s *DB) saveSleepDurationBalanceSnapshot(ctx context.Context, result health.SleepDurationBalance, goals []health.SleepGoal, coverage []health.SleepBalanceCoverage, episodes []health.CompletedSleepEpisode, now time.Time) error {
	if now.IsZero() {
		return fmt.Errorf("sleep duration balance requires a calculation timestamp")
	}
	inputHash, err := sleepDurationBalanceInputHash(goals, coverage, episodes)
	if err != nil {
		return err
	}
	periods, err := json.Marshal(result.Periods)
	if err != nil {
		return fmt.Errorf("marshal sleep duration balance periods: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO sleep_duration_balance_snapshot(
			wake_date,window_start_date,calculated_through,state,confidence,incomplete_reason,
			last_complete_date,balance_hours,periods,input_hash,algorithm_version,calculated_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT(wake_date) DO UPDATE SET
			window_start_date=excluded.window_start_date,
			calculated_through=excluded.calculated_through,
			state=excluded.state,
			confidence=excluded.confidence,
			incomplete_reason=excluded.incomplete_reason,
			last_complete_date=excluded.last_complete_date,
			balance_hours=excluded.balance_hours,
			periods=excluded.periods,
			input_hash=excluded.input_hash,
			algorithm_version=excluded.algorithm_version,
			calculated_at=excluded.calculated_at
		WHERE sleep_duration_balance_snapshot.input_hash IS DISTINCT FROM excluded.input_hash
			OR sleep_duration_balance_snapshot.algorithm_version IS DISTINCT FROM excluded.algorithm_version`,
		result.WakeDate, result.WindowStartDate, result.CalculatedThrough, result.State, result.Confidence,
		result.IncompleteReason, result.LastCompleteDate, result.BalanceHours, json.RawMessage(periods), inputHash,
		SleepDurationBalanceAlgorithmVersion, now.UTC())
	return err
}

func validateSleepPeriodCoverageCommitment(coverage SleepPeriodCoverageCommitment) error {
	if coverage.WakeDate == "" || coverage.SourceEpoch == "" || coverage.CoverageGeneration == "" || coverage.InputHash == "" || coverage.ObservedAt.IsZero() || !coverage.CoveredIntervalEnd.After(coverage.CoveredIntervalStart) {
		return fmt.Errorf("invalid sleep period coverage")
	}
	if coverage.CaptureCompleteness != health.SleepBalanceCoverageComplete {
		return fmt.Errorf("sleep period coverage must be complete")
	}
	return nil
}

func validateCompletedSleepEpisodeCommitments(coverage SleepPeriodCoverageCommitment, episodes []CompletedSleepEpisodeCommitment) error {
	ordered := append([]CompletedSleepEpisodeCommitment(nil), episodes...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Start.Equal(ordered[j].Start) {
			return ordered[i].EpisodeID < ordered[j].EpisodeID
		}
		return ordered[i].Start.Before(ordered[j].Start)
	})
	for index, episode := range ordered {
		if episode.EpisodeID == "" || episode.WakeDate != coverage.WakeDate || episode.Source == "" || episode.SourceEpoch != coverage.SourceEpoch || episode.InputHash == "" || episode.CoverageGeneration != coverage.CoverageGeneration || episode.CaptureState != health.SleepBalanceCoverageComplete || episode.ObservedAt.IsZero() || !episode.End.After(episode.Start) {
			return fmt.Errorf("invalid completed sleep episode")
		}
		if episode.Start.Before(coverage.CoveredIntervalStart) || episode.End.After(coverage.CoveredIntervalEnd) {
			return fmt.Errorf("completed sleep episode lies outside its covered period")
		}
		if episode.DurationAssessment != health.NightDurationPlausible && episode.DurationAssessment != health.NightDurationOutlier {
			return fmt.Errorf("invalid completed sleep episode duration assessment")
		}
		if index > 0 && episode.Start.Before(ordered[index-1].End) {
			return fmt.Errorf("completed sleep episodes overlap")
		}
	}
	return nil
}

func validateSleepGoalsForStorage(goals []health.SleepGoal) error {
	seen := make(map[string]struct{}, len(goals))
	for _, goal := range goals {
		if err := health.ValidateSleepGoal(goal); err != nil {
			return err
		}
		if _, duplicate := seen[goal.EffectiveDate]; duplicate {
			return fmt.Errorf("duplicate sleep goal effective date %q", goal.EffectiveDate)
		}
		seen[goal.EffectiveDate] = struct{}{}
	}
	return nil
}

func sleepDurationBalanceInputHash(goals []health.SleepGoal, coverage []health.SleepBalanceCoverage, episodes []health.CompletedSleepEpisode) (string, error) {
	orderedGoals := append([]health.SleepGoal(nil), goals...)
	orderedCoverage := append([]health.SleepBalanceCoverage(nil), coverage...)
	orderedEpisodes := append([]health.CompletedSleepEpisode(nil), episodes...)
	sort.Slice(orderedGoals, func(i, j int) bool { return orderedGoals[i].EffectiveDate < orderedGoals[j].EffectiveDate })
	sort.Slice(orderedCoverage, func(i, j int) bool { return orderedCoverage[i].WakeDate < orderedCoverage[j].WakeDate })
	sort.Slice(orderedEpisodes, func(i, j int) bool { return orderedEpisodes[i].ID < orderedEpisodes[j].ID })
	payload, err := json.Marshal(struct {
		Goals    []health.SleepGoal
		Coverage []health.SleepBalanceCoverage
		Episodes []health.CompletedSleepEpisode
		Version  string
	}{orderedGoals, orderedCoverage, orderedEpisodes, SleepDurationBalanceAlgorithmVersion})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

// ReconcileSleepDurationBalancesAfter recalculates every potentially affected
// fourteen-day window after one period or goal correction. It deliberately
// stops at throughDate supplied by the caller; no fabricated future state.
func (s *DB) ReconcileSleepDurationBalancesAfter(ctx context.Context, fromDate, throughDate string, now time.Time) error {
	loc := s.reportTZLocation()
	start, err := time.ParseInLocation("2006-01-02", fromDate, loc)
	if err != nil {
		return err
	}
	through, err := time.ParseInLocation("2006-01-02", throughDate, loc)
	if err != nil {
		return err
	}
	for day := start; !day.After(through); day = day.AddDate(0, 0, 1) {
		if _, err := s.ReconcileSleepDurationBalance(ctx, day.Format("2006-01-02"), now); err != nil {
			return err
		}
	}
	return nil
}

// SleepDurationBalanceSnapshot is the persisted, client-safe representation.
// It contains no hidden physiological inference: nil BalanceHours means the
// required manual goal or coverage window is not yet available.
type SleepDurationBalanceSnapshot = health.SleepDurationBalance

func (s *DB) GetSleepDurationBalance(ctx context.Context, wakeDate string) (*SleepDurationBalanceSnapshot, error) {
	var result health.SleepDurationBalance
	var periods []byte
	err := s.pool.QueryRow(ctx, `
		SELECT wake_date,window_start_date,calculated_through,state,confidence,incomplete_reason,
		       last_complete_date,balance_hours,periods
		  FROM sleep_duration_balance_snapshot WHERE wake_date=$1`, wakeDate).
		Scan(&result.WakeDate, &result.WindowStartDate, &result.CalculatedThrough, &result.State, &result.Confidence,
			&result.IncompleteReason, &result.LastCompleteDate, &result.BalanceHours, &periods)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(periods, &result.Periods); err != nil {
		return nil, fmt.Errorf("decode sleep duration balance periods: %w", err)
	}
	return &result, nil
}

func (s *DB) LatestSleepPeriodCoverageDate(ctx context.Context) (string, error) {
	var value string
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(wake_date),'') FROM sleep_period_coverage`).Scan(&value)
	return strings.TrimSpace(value), err
}
