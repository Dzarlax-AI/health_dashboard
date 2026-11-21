package storage

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/sleepwire"
)

// A retraction supersedes precisely the latest source revision, including
// one that is durable but still waiting for its ordinary prerequisite.
func validateSleepRolloutRevision(input SleepRolloutInput, latest int64, latestDate string, raw []byte) error {
	current := input.Candidate
	var previous sleepwire.Candidate
	if latest != 0 {
		if err := json.Unmarshal(raw, &previous); err != nil {
			return err
		}
	}
	proof := current.Coverage
	if previous.Coverage != nil && (proof == nil || !proof.ObservedAt.After(previous.Coverage.ObservedAt)) {
		return ErrSleepRolloutConflict
	}
	if proof != nil && len(previous.Stages) != 0 && !coversPreviousSleepWindow(*proof, previous, latestDate) {
		return ErrSleepRolloutConflict
	}
	if current.Withdrawal == nil {
		return nil
	}
	if proof == nil || previous.Withdrawal != nil || current.Zone != previous.Zone ||
		current.Withdrawal.WakeDate != latestDate || current.Withdrawal.PreviousRevision != latest ||
		input.WakeDate != latestDate || len(previous.Stages) == 0 {
		return ErrSleepRolloutConflict
	}
	return nil
}

func coversPreviousSleepWindow(proof sleepwire.Coverage, previous sleepwire.Candidate, previousDate string) bool {
	location, err := time.LoadLocation(previous.Zone)
	if err != nil {
		return false
	}
	overlap := rolloutAccountingWindow(previousDate, location)
	if !proof.Contains(overlap.Start, overlap.End) {
		return false
	}
	for _, stage := range previous.Stages {
		if !proof.Contains(stage.Start, stage.End) {
			return false
		}
	}
	return true
}

func checkSleepRolloutReceipt(ctx context.Context, tx pgx.Tx, input SleepRolloutInput) (bool, error) {
	current := input.Candidate
	var observedAt time.Time
	if current.Coverage != nil {
		observedAt = current.Coverage.ObservedAt
	} else if current.Observation != nil {
		observedAt = current.Observation.ObservedAt
	}
	if !observedAt.IsZero() {
		var received time.Time
		if err := tx.QueryRow(ctx, "SELECT transaction_timestamp()").Scan(&received); err != nil {
			return false, err
		}
		if observedAt.After(received.Add(5 * time.Minute)) {
			return false, ErrSleepRolloutConflict
		}
	}

	if len(current.Stages) != 0 {
		ids := make([]string, 0, len(current.Stages))
		for _, stage := range current.Stages {
			ids = append(ids, stage.ID)
		}
		var fenced bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sleep_rollout_packets p
			WHERE p.source_id=$1 AND (p.candidate #> '{observation,deleted_stage_ids}') ?| $2::text[])`,
			input.SourceID, ids).Scan(&fenced); err != nil {
			return false, err
		}
		if fenced {
			return true, nil
		}
	}

	observation := current.Observation != nil
	if !observation && current.Coverage == nil {
		var priorObservation bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sleep_rollout_packets
			WHERE source_id=$1 AND candidate_id=$2 AND candidate ? 'observation')`,
			input.SourceID, current.ID).Scan(&priorObservation); err != nil {
			return false, err
		}
		if priorObservation {
			return false, ErrSleepRolloutConflict
		}
	}
	var latest int64
	var latestDate string
	var latestRaw []byte
	err := tx.QueryRow(ctx, `SELECT revision,wake_date::text,candidate FROM sleep_rollout_packets
		WHERE source_id=$1 AND candidate_id=$2 AND (candidate ? 'observation')=$3
		ORDER BY revision DESC LIMIT 1`, input.SourceID, current.ID, observation).Scan(&latest, &latestDate, &latestRaw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if current.Revision < latest {
		return true, nil
	}

	if observation {
		var confirmed int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(revision),0) FROM sleep_rollout_packets
			WHERE source_id=$1 AND candidate_id=$2 AND NOT candidate ? 'observation'`,
			input.SourceID, current.ID).Scan(&confirmed); err != nil {
			return false, err
		}
		if current.Revision <= confirmed {
			return true, nil
		}
		return false, validateEarlySleepRevision(input, latestDate, latestRaw)
	}

	return false, validateSleepRolloutRevision(input, latest, latestDate, latestRaw)
}

func validateEarlySleepRevision(input SleepRolloutInput, previousDate string, raw []byte) error {
	current := input.Candidate
	if raw == nil {
		if len(current.Observation.DeletedStageIDs) > 0 {
			return ErrSleepRolloutConflict
		}
		return nil
	}
	var previous sleepwire.Candidate
	if err := json.Unmarshal(raw, &previous); err != nil {
		return err
	}
	if current.Zone != previous.Zone || !current.Observation.ObservedAt.After(previous.Observation.ObservedAt) {
		return ErrSleepRolloutConflict
	}
	if len(current.Observation.DeletedStageIDs) != 0 {
		if input.WakeDate != previousDate || len(previous.Observation.DeletedStageIDs) != 0 {
			return ErrSleepRolloutConflict
		}
		proven := make([]string, 0, len(previous.Stages))
		for _, stage := range previous.Stages {
			if stage.Kind != "awake" && stage.Kind != "in_bed" {
				proven = append(proven, stage.ID)
			}
		}
		slices.Sort(proven)
		if !slices.Equal(proven, current.Observation.DeletedStageIDs) {
			return ErrSleepRolloutConflict
		}
		return nil
	}
	if len(previous.Observation.DeletedStageIDs) != 0 {
		return ErrSleepRolloutConflict
	}
	for _, stage := range previous.Stages {
		if stage.Kind != "awake" && stage.Kind != "in_bed" && !slices.Contains(current.Stages, stage) {
			return ErrSleepRolloutConflict
		}
	}
	return nil
}
