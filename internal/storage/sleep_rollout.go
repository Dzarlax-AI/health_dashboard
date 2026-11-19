package storage

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"health-receiver/experiments/dbt_metrics"
	"health-receiver/internal/sleepwire"
)

//go:embed sleep_rollout.sql
var sleepRolloutDDL string

const SleepRolloutModeSetting = "sleep_rollout_mode"
const rolloutReadinessBaselineDays = 30

var ErrSleepRolloutDisabled = errors.New("sleep rollout is disabled")
var ErrSleepRolloutConflict = errors.New("sleep rollout revision conflicts")

// SleepRolloutInput has already passed validation at the authenticated boundary.
type SleepRolloutInput struct {
	SourceID, WakeDate string
	Candidate          sleepwire.Candidate
	Raw                []byte
	Legacy             json.RawMessage
}

// sleepRolloutIdentity runs under the same lock as acceptance and processing.
// Its ID comes from the tenant-owned marker, never from packet data.
func sleepRolloutIdentity(ctx context.Context, tx pgx.Tx) (uuid.UUID, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('health/sleep-rollout/' || current_schema(),0))`); err != nil {
		return uuid.Nil, err
	}
	var mode string
	if err := tx.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1 FOR SHARE`, SleepRolloutModeSetting).Scan(&mode); errors.Is(err, pgx.ErrNoRows) || (err == nil && mode != "prepare") {
		return uuid.Nil, ErrSleepRolloutDisabled
	} else if err != nil {
		return uuid.Nil, err
	}
	identity, err := ReadTenantIdentityMarker(ctx, tx, "")
	if err != nil {
		return uuid.Nil, err
	}
	if identity.TenantID == uuid.Nil || identity.SchemaContractVersion != SchemaContractVersion ||
		identity.SchemaContractChecksum != SchemaContractChecksum() {
		return uuid.Nil, errors.New("sleep rollout tenant contract is unverified")
	}
	return identity.TenantID, nil
}

// AcceptSleepRollout commits the immutable raw packet and its work item in one step.
func (s *DB) AcceptSleepRollout(ctx context.Context, input SleepRolloutInput) (string, error) {
	normalized, err := json.Marshal(input.Candidate)
	if err != nil {
		return "", err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(context.Background())
	if _, err := sleepRolloutIdentity(ctx, tx); err != nil {
		return "", err
	}
	var previousMatches bool
	err = tx.QueryRow(ctx, `SELECT candidate=$4::jsonb AND legacy_payload=$5::jsonb
		FROM sleep_rollout_packets WHERE source_id=$1 AND candidate_id=$2 AND revision=$3`,
		input.SourceID, input.Candidate.ID, input.Candidate.Revision, json.RawMessage(normalized), input.Legacy).Scan(&previousMatches)
	if err == nil {
		if !previousMatches {
			return "", ErrSleepRolloutConflict
		}
		return "repeat", nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	var latest int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(revision),0) FROM sleep_rollout_packets
		WHERE source_id=$1 AND candidate_id=$2`, input.SourceID, input.Candidate.ID).Scan(&latest); err != nil {
		return "", err
	}
	if input.Candidate.Revision < latest {
		return "stale", nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO sleep_rollout_packets
		(source_id,candidate_id,revision,wake_date,raw_payload,candidate,legacy_payload)
		VALUES ($1,$2,$3,$4::text::date,$5,$6,$7)`, input.SourceID, input.Candidate.ID, input.Candidate.Revision,
		input.WakeDate, input.Raw, json.RawMessage(normalized), input.Legacy); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return "accepted", nil
}

var rolloutDailySQL = `INSERT INTO sleep_rollout_daily
	(wake_date,generation,candidate,revision,sleep_seconds,deep_seconds,rem_seconds,core_seconds,unspecified_seconds,awake_seconds,stage_conflict)
	SELECT $2::text::date,$3,d.candidate,d.revision,d.sleep_seconds,d.deep_seconds,d.rem_seconds,
		d.core_seconds,d.unspecified_seconds,d.awake_seconds,d.stage_conflict
	FROM (SELECT true) seed LEFT JOIN (` + dbtmetrics.SleepDailySQL("sleep_rollout_candidates", "profile=$1 AND wake_date=$2::text::date") + `) d ON true
	ON CONFLICT (wake_date) DO UPDATE SET generation=EXCLUDED.generation,candidate=EXCLUDED.candidate,
	revision=EXCLUDED.revision,sleep_seconds=EXCLUDED.sleep_seconds,deep_seconds=EXCLUDED.deep_seconds,
	rem_seconds=EXCLUDED.rem_seconds,core_seconds=EXCLUDED.core_seconds,unspecified_seconds=EXCLUDED.unspecified_seconds,
	awake_seconds=EXCLUDED.awake_seconds,stage_conflict=EXCLUDED.stage_conflict`

// RunSleepRolloutNext processes one receipt. A failed date or dependency
// leaves the original receipt pending and rolls back both date placements. A
// wake date still ahead of the tenant calendar stays pending without blocking
// receipts eligible on the current local date.
func (s *DB) RunSleepRolloutNext(ctx context.Context, now time.Time) (bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.Background())
	profile, err := sleepRolloutIdentity(ctx, tx)
	if errors.Is(err, ErrSleepRolloutDisabled) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	today := now.In(unit.reportTZLocation()).Format(time.DateOnly)
	var sequence, revision int64
	var source, id, date string
	var snapshot json.RawMessage
	err = tx.QueryRow(ctx, `SELECT sequence,source_id,candidate_id,revision,wake_date::text,candidate
		FROM sleep_rollout_packets WHERE applied_at IS NULL AND wake_date <= $1::text::date
		ORDER BY sequence LIMIT 1 FOR UPDATE`, today).
		Scan(&sequence, &source, &id, &revision, &date, &snapshot)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	key, err := json.Marshal([2]string{source, id})
	if err != nil {
		return false, err
	}
	var oldDate string
	var oldRevision int64
	err = tx.QueryRow(ctx, `SELECT wake_date::text,revision FROM sleep_rollout_candidates WHERE id=$1`, string(key)).Scan(&oldDate, &oldRevision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if revision > oldRevision {
		var priorities []string
		if err := json.Unmarshal([]byte(unit.GetSetting("sleep_rollout_source_priority", "[]")), &priorities); err != nil {
			return false, errors.New("invalid sleep rollout source priority")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO sleep_rollout_candidates(profile,id,revision,wake_date,priority,snapshot)
			VALUES ($1,$2,$3,$4::text::date,0,$5)
			ON CONFLICT(id) DO UPDATE SET revision=EXCLUDED.revision,wake_date=EXCLUDED.wake_date,
			snapshot=EXCLUDED.snapshot`, profile, string(key), revision, date, snapshot); err != nil {
			return false, err
		}
		affected := []string{date}
		if oldDate != "" && oldDate != date {
			affected = append(affected, oldDate)
		}
		// Candidate IDs are encoded [source, id] tuples. Rank every contender
		// on both affected dates using the same current tenant policy.
		if _, err := tx.Exec(ctx, `UPDATE sleep_rollout_candidates
			SET priority=COALESCE(array_position($3::text[],id::jsonb->>0)-1,cardinality($3::text[]),0)
			WHERE profile=$1 AND wake_date::text=ANY($2::text[])`, profile, affected, priorities); err != nil {
			return false, err
		}
		for _, day := range affected {
			if _, err := tx.Exec(ctx, rolloutDailySQL, profile, day, sequence); err != nil {
				return false, fmt.Errorf("rollout daily: %w", err)
			}
		}
		if err := unit.prepareSleepRolloutResults(ctx, affected, sequence, today); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE sleep_rollout_packets SET applied_at=now() WHERE sequence=$1`, sequence); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (s *DB) prepareSleepRolloutResults(ctx context.Context, dates []string, generation int64, today string) error {
	affected := make(map[string]bool)
	for _, day := range dates {
		start, err := time.Parse(time.DateOnly, day)
		if err != nil {
			return err
		}
		for i := 0; i <= rolloutReadinessBaselineDays; i++ {
			date := start.AddDate(0, 0, i).Format(time.DateOnly)
			if date > today {
				break
			}
			affected[date] = true
		}
	}
	cfg := s.GetEnergyConfig()
	for date := range affected {
		r, err := s.readRolloutReadiness(ctx, date)
		if err != nil {
			return err
		}
		energy, err := s.readRolloutEnergy(ctx, date, cfg)
		if err != nil {
			return err
		}
		rawReadiness, err := json.Marshal(r)
		if err != nil {
			return err
		}
		rawEnergy, err := json.Marshal(energy)
		if err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO sleep_rollout_results(day,generation,readiness,energy)
			VALUES ($1::text::date,$2,$3,$4)
			ON CONFLICT(day) DO UPDATE SET generation=EXCLUDED.generation,readiness=EXCLUDED.readiness,energy=EXCLUDED.energy`,
			date, generation, json.RawMessage(rawReadiness), json.RawMessage(rawEnergy)); err != nil {
			return err
		}
	}
	return nil
}

func (s *DB) readRolloutReadiness(ctx context.Context, date string) (*LocalShadowReadiness, error) {
	rows, err := s.pool.Query(ctx, `SELECT day::text,d.generation,d.candidate,d.revision,
		d.sleep_seconds/3600,d.deep_seconds/3600,d.rem_seconds/3600,d.core_seconds/3600,
		d.unspecified_seconds/3600,d.awake_seconds/3600,d.stage_conflict,h.sleep_total
		FROM (SELECT $1::text::date-i AS day FROM generate_series(0,$2::integer) AS days(i)) days
		LEFT JOIN sleep_rollout_daily d ON d.wake_date=day
		LEFT JOIN daily_scores h ON h.date=day::text ORDER BY day DESC`, date, rolloutReadinessBaselineDays)
	blocks, baseline, err := readSleepBlocks(rows, err, date)
	if err != nil {
		return nil, err
	}
	return s.readinessWithSleep(ctx, date, blocks, baseline), nil
}

func (s *DB) readRolloutEnergy(ctx context.Context, date string, cfg EnergyConfig) (BankResult, error) {
	from := subtractDays(date, energyWindowDays-1)
	inputs, err := s.loadDailyEnergyInputs(ctx, from, date)
	if err != nil {
		return BankResult{}, err
	}
	rows, err := s.pool.Query(ctx, `SELECT wake_date::text,sleep_seconds/3600,deep_seconds/3600,rem_seconds/3600,awake_seconds/3600
		FROM sleep_rollout_daily WHERE wake_date BETWEEN $1::text::date AND $2::text::date`, from, date)
	inputs, err = applySleepEnergyRows(inputs, rows, err)
	if err != nil {
		return BankResult{}, err
	}
	return computeCalendarEnergyBank(inputs, date, cfg), nil
}
