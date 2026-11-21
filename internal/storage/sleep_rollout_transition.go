package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"health-receiver/internal/sleepstudy"
	"health-receiver/internal/sleepwire"
)

func transitionSleepProjection(ctx context.Context, tx pgx.Tx) (*sleepGoProjection, error) {
	publication, err := readSleepRolloutPublication(ctx, tx)
	if err != nil {
		return nil, err
	}
	if publication != nil && publication.Projection != nil {
		return publication.Projection, nil
	}
	if !sleepGoProjectionProcessEnabled() {
		return nil, nil
	}
	return loadPreparedSleepGoProjection(ctx, tx)
}

// Only the verified window is fenced. Later receipts remain queued and
// ordinary metrics are read from their usual Go inputs on both routes.
func auditRevisionBoundSleepRollout(ctx context.Context, tx pgx.Tx, report SleepRolloutAudit, tenant uuid.UUID, projection *sleepGoProjection) (SleepRolloutAudit, error) {
	if report.From != projection.From || report.Through != projection.Through {
		return report, errors.New("audit range must match the configured sleep projection")
	}
	report.projection = projection
	report.ProjectionReady = true
	config, err := json.Marshal(projection)
	if err != nil {
		return report, err
	}
	digest := sha256.New()
	fmt.Fprintf(digest, "sleep-projection-v1\n%s\n%s\n%s\n", tenant, SchemaContractChecksum(), config)
	rows, err := tx.Query(ctx, `SELECT days.wake_day::date::text,d.generation,r.generation,d.xmin::text,r.xmin::text,d.episode_selection, COALESCE((`+readySleepGoProjectionSQL+`),false)
		FROM generate_series($1::text::date,$2::text::date,interval '1 day') AS days(wake_day)
		LEFT JOIN sleep_rollout_daily d ON d.wake_date=days.wake_day::date AND `+ownedSleepGoProjectionSQL+`
		LEFT JOIN sleep_rollout_results r ON r.day=d.wake_date ORDER BY days.wake_day`, report.From, report.Through, projection.Sources)
	if err != nil {
		return report, err
	}
	defer rows.Close()
	for rows.Next() {
		var day SleepRolloutAuditDay
		var generation, resultGeneration *int64
		var dailyVersion, resultVersion *string
		var raw []byte
		var eligible bool
		if err := rows.Scan(&day.Date, &generation, &resultGeneration, &dailyVersion, &resultVersion, &raw, &eligible); err != nil {
			return report, err
		}
		fmt.Fprintln(digest, day.Date)
		report.ExpectedDates++
		switch {
		case generation == nil:
			day.Sleep, day.Result = "unowned", "ordinary_path"
			report.UnownedDates++
		default:
			if err := json.Unmarshal(raw, &day.Selection); err != nil {
				return report, err
			}
			day.Sleep, day.Generation = day.Selection.State, *generation
			fmt.Fprintln(digest, *generation, *dailyVersion)
			report.projectionBoundary = max(report.projectionBoundary, *generation)
			if resultGeneration == nil || *resultGeneration < *generation {
				report.ProjectionReady = false
				day.Result = "missing_generation"
				report.MissingResults++
			} else {
				fmt.Fprintln(digest, *resultGeneration, *resultVersion)
				if eligible {
					day.Result = "verified_generation"
					report.VerifiedProjectionDates++
				} else {
					day.Result = "retained_unknown"
				}
			}
		}
		report.Dates = append(report.Dates, day)
	}
	if err := rows.Err(); err != nil {
		return report, err
	}
	rows.Close()
	// Use the worker's episode dates for both revisions. A packet's final
	// wake date alone can miss an earlier episode in the same snapshot.
	pending, err := tx.Query(ctx, `SELECT p.sequence,p.wake_date::text,p.candidate,c.wake_date::text,c.snapshot
		FROM sleep_rollout_packets p
		LEFT JOIN sleep_rollout_candidates c ON c.id::jsonb=jsonb_build_array(p.source_id,p.candidate_id)
		WHERE p.applied_at IS NULL ORDER BY p.sequence`)
	if err != nil {
		return report, err
	}
	defer pending.Close()
	for pending.Next() {
		var sequence int64
		var wake string
		var previousWake *string
		var current, previous []byte
		if err := pending.Scan(&sequence, &wake, &current, &previousWake, &previous); err != nil {
			return report, err
		}
		affectsWindow, err := pendingSleepProjectionTouches(report.From, report.Through, wake, previousWake, current, previous)
		if err != nil {
			return report, err
		}
		if affectsWindow {
			fmt.Fprintln(digest, "pending", sequence)
			report.PendingCandidates++
		}
	}
	if err := pending.Err(); err != nil {
		return report, err
	}
	report.Evidence = fmt.Sprintf("%x", digest.Sum(nil))
	return report, nil
}

func pendingSleepProjectionTouches(from, through, wake string, previousWake *string, current, previous []byte) (bool, error) {
	touches := func(first, last string) bool {
		return first <= through && subtractDays(last, -rolloutReadinessBaselineDays) >= from
	}
	if touches(wake, wake) || (previousWake != nil && touches(*previousWake, *previousWake)) {
		return true, nil
	}
	dates := make(map[string]bool)
	for _, raw := range [][]byte{current, previous} {
		if raw == nil {
			continue
		}
		var candidate sleepwire.Candidate
		if err := json.Unmarshal(raw, &candidate); err != nil {
			return false, err
		}
		if err := addAffectedEpisodeDates(dates, candidate); err != nil {
			return false, err
		}
	}
	neighborDays := rolloutAccountingNeighborDays + rolloutFragmentNeighborDays
	for date := range dates {
		if touches(subtractDays(date, neighborDays), subtractDays(date, -sleepstudy.HistoryDays-neighborDays)) {
			return true, nil
		}
	}
	return false, nil
}

func switchRevisionBoundSleepRollout(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, from, through, evidence string, active bool) (SleepRolloutPublication, error) {
	var publication SleepRolloutPublication
	// Fence settings edits with the mode change. Candidate writers already
	// share the rollout advisory lock held by the caller.
	if _, err := tx.Exec(ctx, `LOCK TABLE settings IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return publication, err
	}
	current, err := readSleepRolloutPublication(ctx, tx)
	if err != nil {
		return publication, err
	}
	if active {
		if current != nil && current.Active {
			return publication, errors.New("sleep rollout is already published")
		}
		if _, err := sleepRolloutProfile(ctx, tx); err != nil {
			return publication, err
		}
	} else if current == nil || current.Projection == nil || !current.Active || current.Tenant != tenant ||
		current.From != from || current.Through != through {
		return publication, errors.New("rollback must target the current revision-bound publication")
	}
	projection, err := transitionSleepProjection(ctx, tx)
	if err != nil {
		return publication, err
	}
	if projection == nil || projection.From != from || projection.Through != through {
		return publication, ErrSleepRolloutEvidenceChanged
	}
	report, err := auditRevisionBoundSleepRollout(ctx, tx, SleepRolloutAudit{From: from, Through: through}, tenant, projection)
	if err != nil {
		return publication, err
	}
	if report.Evidence != evidence {
		return publication, ErrSleepRolloutEvidenceChanged
	}
	if active && (!report.ProjectionReady || report.VerifiedProjectionDates == 0) {
		return publication, errors.New("sleep projection has no verified dates or has an incomplete result")
	}
	mode := "prepare"
	publication = SleepRolloutPublication{Tenant: tenant, Active: active, From: from, Through: through,
		Projection: projection, Evidence: evidence}
	if active {
		mode = "serve"
		publication.Packet = report.projectionBoundary
	} else {
		publication.Packet = current.Packet
	}
	return saveSleepRolloutPublication(ctx, tx, mode, publication)
}
