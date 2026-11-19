package sleepshadow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/storage"
)

type RebuildResult struct {
	Profile      string
	SleepDates   int
	DerivedDates int
	Complete     bool
}

var errRebuildInputsChanged = errors.New("rebuild inputs changed before observation publication")

const rebuildBindingConflict = `EXISTS (
	SELECT 1 FROM sleep_shadow.energy_current WHERE profile=$1 AND tenant_schema<>$2
	UNION ALL SELECT 1 FROM sleep_shadow.rebuild_checkpoint WHERE profile=$1 AND tenant_schema<>$2
	UNION ALL SELECT 1 FROM sleep_shadow.pending WHERE profile=$1 AND refresh_schema<>'' AND refresh_schema<>$2
)`

// RebuildAll uses the operator-confirmed schema for each local shadow profile.
// Each date and its cursor commit together. Profiles blocked by pending input
// or a failed date are reported and can be resumed with the same version.
func RebuildAll(ctx context.Context, pool *pgxpool.Pool, bindings map[string]string, version string, now time.Time) ([]RebuildResult, error) {
	started := time.Now()
	if version == "" {
		return nil, errors.New("rebuild requires a version")
	}
	var profiles []string
	err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		profiles, err = validateRebuildBindings(ctx, tx, bindings)
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(profiles) == 0 {
		return nil, nil
	}
	// Install input tracking only after validating every binding. Date
	// transactions own all projection writes.
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, schema); err != nil {
			return err
		}
		return installRebuildInputTracking(ctx, tx, profiles, bindings)
	}); err != nil {
		return nil, err
	}
	s := store{pool: pool}
	results := make([]RebuildResult, 0, len(profiles))
	var failures []error
	for _, profile := range profiles {
		result := RebuildResult{Profile: profile}
		fingerprint, err := rebuildInputsFingerprint(ctx, pool, bindings[profile])
		if err != nil {
			failures = append(failures, fmt.Errorf("profile %q inputs: %w", profile, err))
			results = append(results, result)
			continue
		}
		for ctx.Err() == nil {
			phase, err := s.rebuildStep(ctx, profile, bindings[profile], version, fingerprint, now.Add(time.Since(started)))
			if errors.Is(err, errRebuildInputsChanged) {
				fingerprint, err = rebuildInputsFingerprint(ctx, pool, bindings[profile])
				if err == nil {
					continue
				}
			}
			if err != nil {
				failures = append(failures, fmt.Errorf("profile %q: %w", profile, err))
				break
			}
			switch phase {
			case "sleep":
				result.SleepDates++
			case "derived":
				result.DerivedDates++
			case "published", "done":
				// Recheck both identities under the profile fence. A sleep update
				// can commit after the preceding step released its lock.
				valid, err := s.rebuildStillCurrent(ctx, profile, bindings[profile], version, fingerprint)
				if err != nil {
					failures = append(failures, fmt.Errorf("profile %q inputs: %w", profile, err))
				} else if !valid {
					fingerprint, err = rebuildInputsFingerprint(ctx, pool, bindings[profile])
					if err == nil {
						continue
					}
					failures = append(failures, fmt.Errorf("profile %q inputs: %w", profile, err))
				} else {
					result.Complete = true
				}
			}
			if phase == "done" || phase == "published" {
				break
			}
		}
		results = append(results, result)
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
	}
	return results, errors.Join(failures...)
}

func validateRebuildBindings(ctx context.Context, tx pgx.Tx, bindings map[string]string) ([]string, error) {
	var registry bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('sleep_shadow.profile') IS NOT NULL`).Scan(&registry); err != nil {
		return nil, err
	}
	if !registry && len(bindings) == 0 {
		return nil, nil
	}
	if !registry {
		return nil, errors.New("local shadow profile registry does not exist")
	}
	rows, err := tx.Query(ctx, `SELECT id FROM sleep_shadow.profile ORDER BY id`)
	if err != nil {
		return nil, err
	}
	profiles, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	if len(profiles) != len(bindings) {
		return nil, errors.New("bindings must cover exactly the local shadow profiles")
	}
	seen := make(map[int64]bool, len(profiles))
	for _, profile := range profiles {
		schema := bindings[profile]
		if schema == "" || schema == "sleep_shadow" {
			return nil, fmt.Errorf("profile %q requires one distinct tenant schema", profile)
		}
		var oid int64
		var ready bool
		err := tx.QueryRow(ctx, `SELECT n.oid::bigint, (
				SELECT count(*) = 4 FROM pg_catalog.pg_class c WHERE c.relnamespace=n.oid AND c.relkind IN ('r','p')
				AND c.relname IN ('daily_scores','energy_snapshots','settings','metric_points'))
			FROM pg_catalog.pg_namespace n WHERE n.nspname::text COLLATE "C"=$1`, schema).Scan(&oid, &ready)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("profile %q: tenant schema %q does not exist under that exact name", profile, schema)
		}
		if err != nil {
			return nil, err
		}
		if !ready {
			return nil, fmt.Errorf("profile %q: tenant schema %q is not ready", profile, schema)
		}
		if seen[oid] {
			return nil, fmt.Errorf("profile %q requires one distinct tenant schema", profile)
		}
		seen[oid] = true
		conflict, err := legacyRebuildBindingConflict(ctx, tx, profile, schema)
		if err != nil {
			return nil, err
		}
		if conflict {
			return nil, fmt.Errorf("profile %q: manifest conflicts with a recorded schema binding", profile)
		}
	}
	return profiles, nil
}

// Preflight also supports shadow schemas created before the checkpoint and
// refresh binding columns existed. It never installs those objects.
func legacyRebuildBindingConflict(ctx context.Context, tx pgx.Tx, profile, schema string) (bool, error) {
	for _, source := range []struct{ table, column string }{
		{"energy_current", "tenant_schema"},
		{"rebuild_checkpoint", "tenant_schema"},
		{"pending", "refresh_schema"},
	} {
		relation := pgx.Identifier{"sleep_shadow", source.table}.Sanitize()
		var available bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_attribute
			WHERE attrelid=to_regclass($1) AND attname::text=$2 AND NOT attisdropped)`, relation, source.column).Scan(&available); err != nil {
			return false, err
		}
		if !available {
			continue
		}
		column := pgx.Identifier{source.column}.Sanitize()
		var conflict bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+relation+" WHERE profile=$1 AND "+column+"<>'' AND "+column+"<>$2)", profile, schema).Scan(&conflict); err != nil || conflict {
			return conflict, err
		}
	}
	return false, nil
}

func (s store) rebuildStep(ctx context.Context, profile, schema, version, fingerprint string, now time.Time) (string, error) {
	stepStarted := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var completedPhase string
	err := s.transaction(ctx, profile, func(tx pgx.Tx) error {
		var revision int64
		var pending, conflict bool
		if err := tx.QueryRow(ctx, `SELECT source_revision,
			EXISTS(SELECT 1 FROM sleep_shadow.pending WHERE profile=$1), `+rebuildBindingConflict+`
			FROM sleep_shadow.profile WHERE id=$1`, profile, schema).Scan(&revision, &pending, &conflict); err != nil {
			return err
		}
		if conflict {
			return errors.New("manifest conflicts with a recorded schema binding")
		}
		if pending {
			return errors.New("sleep packets are pending; resolve or replay them before rebuilding")
		}
		today, err := storage.LocalShadowRebuildToday(ctx, tx, schema, now)
		if err != nil {
			return err
		}
		checkpoint, err := loadRebuildCheckpoint(ctx, tx, profile, schema, version, today, fingerprint, revision)
		if err != nil {
			return err
		}
		completedPhase = checkpoint.phase
		if checkpoint.phase == "done" {
			if !checkpoint.observationPending {
				return nil
			}
			if err := publishRebuildObservation(ctx, tx, profile, schema, today, fingerprint, now, stepStarted); err != nil {
				return err
			}
			checkpoint.observationPending = false
			completedPhase = "published"
			return saveRebuildCheckpoint(ctx, tx, profile, schema, version, fingerprint, revision, checkpoint)
		}
		if checkpoint.next == nil {
			return errors.New("rebuild checkpoint is missing its next date")
		}
		date := *checkpoint.next
		switch checkpoint.phase {
		case "sleep":
			err = rebuildSleepDate(ctx, tx, profile, date)
		case "derived":
			err = storage.RebuildLocalShadowDerivedDate(ctx, tx, schema, profile, version, date, now)
		}
		if err != nil {
			return fmt.Errorf("%s %s: %w", checkpoint.phase, date, err)
		}
		if date == checkpoint.through {
			if checkpoint.phase == "sleep" {
				checkpoint.phase, checkpoint.next = "derived", checkpoint.first
			} else {
				checkpoint.phase, checkpoint.next = "done", nil
				checkpoint.observationPending = date == today
			}
		} else {
			day, err := time.Parse(time.DateOnly, date)
			if err != nil {
				return err
			}
			next := day.AddDate(0, 0, 1).Format(time.DateOnly)
			checkpoint.next = &next
		}
		return saveRebuildCheckpoint(ctx, tx, profile, schema, version, fingerprint, revision, checkpoint)
	})
	return completedPhase, err
}

func publishRebuildObservation(ctx context.Context, tx pgx.Tx, profile, schema, today, fingerprint string, now, stepStarted time.Time) error {
	// Match TRUNCATE lock order: relation locks precede the revision row.
	for _, input := range rebuildInputTables {
		table := pgx.Identifier{schema, input.table}.Sanitize()
		if input.optional {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				continue
			}
		}
		if _, err := tx.Exec(ctx, `LOCK TABLE `+table+` IN ACCESS SHARE MODE`); err != nil {
			return err
		}
	}
	// The profile lock fences sleep changes. Tracked input writers must also
	// commit through this revision row, so none can race observation publication.
	var revision int64
	if err := tx.QueryRow(ctx, rebuildInputRevisionSQL+" FOR UPDATE", schema).Scan(&revision); err != nil {
		return err
	}
	if fingerprintRebuildInputs(revision) != fingerprint {
		return errRebuildInputsChanged
	}
	observationTime := now.Add(time.Since(stepStarted))
	publicationDay, err := storage.LocalShadowRebuildToday(ctx, tx, schema, observationTime)
	if err != nil {
		return err
	}
	if publicationDay != today {
		return errRebuildInputsChanged
	}
	if err := storage.PublishLocalShadowRebuildObservation(ctx, tx, schema, profile, today, observationTime); err != nil {
		return err
	}
	after, err := rebuildInputRevision(ctx, tx, schema)
	if err == nil && after != revision {
		return errors.New("rebuild inputs changed inside observation publication")
	}
	return err
}

// The completion decision observes the sleep revision and tenant input revision
// together while holding the same profile fence used by sleep writers.
func (s store) rebuildStillCurrent(ctx context.Context, profile, schema, version, fingerprint string) (bool, error) {
	var valid bool
	err := s.transaction(ctx, profile, func(tx pgx.Tx) error {
		var revision int64
		if err := tx.QueryRow(ctx, rebuildInputRevisionSQL+" FOR SHARE", schema).Scan(&revision); err != nil {
			return err
		}
		latest := fingerprintRebuildInputs(revision)
		if latest != fingerprint {
			return nil
		}
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sleep_shadow.rebuild_checkpoint c
			JOIN sleep_shadow.profile p ON p.id=c.profile
			WHERE c.profile=$1 AND c.tenant_schema=$2 AND c.version=$3 AND c.phase='done'
			AND NOT c.observation_pending AND c.source_revision=p.source_revision AND c.input_fingerprint=$4)
			AND NOT EXISTS (SELECT 1 FROM sleep_shadow.pending WHERE profile=$1)`, profile, schema, version, latest).Scan(&valid)
	})
	return valid, err
}

type rebuildCheckpoint struct {
	first, next        *string
	phase              string
	through            string
	observationPending bool
}

func loadRebuildCheckpoint(ctx context.Context, tx pgx.Tx, profile, schema, version, today, fingerprint string, revision int64) (rebuildCheckpoint, error) {
	var c rebuildCheckpoint
	err := tx.QueryRow(ctx, `SELECT first_date::text,next_date::text,phase,through_date::text,observation_pending
		FROM sleep_shadow.rebuild_checkpoint WHERE profile=$1 AND tenant_schema=$2
		AND version=$3 AND through_date<=$4::text::date AND source_revision=$5 AND input_fingerprint=$6`,
		profile, schema, version, today, revision, fingerprint).Scan(&c.first, &c.next, &c.phase, &c.through, &c.observationPending)
	if err == nil {
		if c.phase != "done" || c.through == today {
			return c, nil
		}
		if c.first != nil {
			// Extend a finished range after midnight. Include its last date
			// so the now-closed day can receive a synthetic EOD snapshot.
			previousLast := c.through
			return rebuildCheckpoint{first: &previousLast, next: &previousLast, phase: "sleep", through: today}, nil
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return c, err
	}
	err = tx.QueryRow(ctx, `SELECT min(d)::text FROM (
		SELECT wake_date AS d FROM sleep_shadow.candidate WHERE profile=$1
		UNION ALL SELECT wake_date FROM sleep_shadow.recalculation WHERE profile=$1
		UNION ALL SELECT wake_date FROM sleep_shadow.daily WHERE profile=$1
		UNION ALL SELECT date::date FROM daily_scores
		UNION ALL SELECT CASE WHEN pg_input_is_valid(left(date,10),'date') AND left(date,10) ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'
			THEN left(date,10)::date END FROM metric_points
	) coverage WHERE d <= $2::text::date`, profile, today).Scan(&c.first)
	if err != nil {
		return c, err
	}
	if err := pruneRebuildOutputs(ctx, tx, profile, schema, today, c.first); err != nil {
		return c, err
	}
	c.phase, c.next, c.through = "sleep", c.first, today
	if c.first == nil {
		c.phase = "done"
		return c, saveRebuildCheckpoint(ctx, tx, profile, schema, version, fingerprint, revision, c)
	}
	return c, nil
}

// Derived results do not extend source coverage. On a new range, remove the
// unsupported dates in the same transaction that advances the checkpoint.
func pruneRebuildOutputs(ctx context.Context, tx pgx.Tx, profile, schema, today string, first *string) error {
	if _, err := tx.Exec(ctx, `WITH removed_readiness AS (
		DELETE FROM sleep_shadow.rebuilt_readiness WHERE profile=$1 AND tenant_schema=$2
		AND ($4::text::date IS NULL OR local_date < $4::text::date OR local_date > $3::text::date)
	)
	DELETE FROM sleep_shadow.energy_current WHERE profile=$1 AND tenant_schema=$2
	AND ($4::text::date IS NULL OR local_date < $4::text::date OR local_date > $3::text::date)`,
		profile, schema, today, first); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM energy_snapshots WHERE 'backfilled'=ANY(flags)
		AND ($2::text IS NULL OR date < $2 OR date >= $1)`, today, first)
	return err
}

func saveRebuildCheckpoint(ctx context.Context, tx pgx.Tx, profile, schema, version, fingerprint string, revision int64, c rebuildCheckpoint) error {
	_, err := tx.Exec(ctx, `INSERT INTO sleep_shadow.rebuild_checkpoint
		(profile,tenant_schema,version,source_revision,through_date,first_date,next_date,phase,input_fingerprint,observation_pending)
		VALUES ($1,$2,$3,$4,$5::text::date,$6::text::date,$7::text::date,$8,$9,$10)
		ON CONFLICT (profile,tenant_schema) DO UPDATE SET version=EXCLUDED.version,
			source_revision=EXCLUDED.source_revision,through_date=EXCLUDED.through_date,
			first_date=EXCLUDED.first_date,next_date=EXCLUDED.next_date,phase=EXCLUDED.phase,
			input_fingerprint=EXCLUDED.input_fingerprint,observation_pending=EXCLUDED.observation_pending`,
		profile, schema, version, revision, c.through, c.first, c.next, c.phase, fingerprint, c.observationPending)
	return err
}

func rebuildSleepDate(ctx context.Context, tx pgx.Tx, profile, date string) error {
	// Move stale placements out of the way before inserting a winner. The
	// displaced dates keep ownership even if their resulting night is empty.
	if _, err := tx.Exec(ctx, `WITH removed AS (
		DELETE FROM sleep_shadow.daily d WHERE d.profile=$1 AND
			(d.wake_date=$2::text::date OR EXISTS (
				SELECT 1 FROM sleep_shadow.candidate c WHERE c.profile=$1
				AND c.wake_date=$2::text::date AND c.id=d.candidate))
		RETURNING wake_date
	) INSERT INTO sleep_shadow.recalculation (profile,wake_date,generation)
		SELECT $1,wake_date,1 FROM removed
		ON CONFLICT (profile,wake_date) DO NOTHING`,
		profile, date); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, recalculate, profile, date)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO sleep_shadow.recalculation (profile,wake_date,generation)
		SELECT $1,$2::text::date,1 WHERE $3 OR EXISTS (
			SELECT 1 FROM sleep_shadow.recalculation WHERE profile=$1 AND wake_date=$2::text::date)
		ON CONFLICT (profile,wake_date) DO UPDATE SET generation=sleep_shadow.recalculation.generation+1`,
		profile, date, tag.RowsAffected() != 0)
	return err
}
