package storage

import (
	"encoding/json"
	"time"

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
