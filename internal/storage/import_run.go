package storage

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

const appleHealthXMLOrigin = "apple_health_xml"

// ImportOptions describes one logical external import.
type ImportOptions struct {
	Origin     string
	SourceName string
	SnapshotAt time.Time
}

// ImportCounters records parser, staging, and promote outcomes for one import.
type ImportCounters struct {
	ImportRunID      int64
	HealthRecordID   int64
	ParsedPoints     int64
	InsertedPoints   int64
	UpdatedPoints    int64
	DeletedPoints    int64
	SkippedPoints    int64
	ParsedWorkouts   int64
	UpsertedWorkouts int64
	DeletedWorkouts  int64
	MinDate          string
	MaxDate          string
}

const abandonedRunningImportAge = 24 * time.Hour

// ImportSession owns one XML import's durable metadata and persistent staging rows.
// Call Commit after all parser callbacks succeed, or Abort on any parse/stage error.
type ImportSession struct {
	db             *DB
	runID          int64
	healthRecordID int64
	snapshotAt     time.Time
	counters       ImportCounters
}

// BeginAppleHealthXMLImport creates durable import metadata. Staged rows live
// in persistent tables keyed by import_run_id so parsing does not hold one
// long database transaction open.
func (s *DB) BeginAppleHealthXMLImport(opts ImportOptions) (*ImportSession, error) {
	if opts.Origin == "" {
		opts.Origin = appleHealthXMLOrigin
	}
	if opts.SourceName == "" {
		opts.SourceName = "apple-health-xml"
	}
	if opts.SnapshotAt.IsZero() {
		opts.SnapshotAt = time.Now()
	}

	ctx, cancel := longCtx()
	defer cancel()

	if err := s.ensureAppleHealthImportSchema(ctx); err != nil {
		return nil, fmt.Errorf("ensure import schema: %w", err)
	}
	if err := s.cleanupAbandonedImportStages(ctx, abandonedRunningImportAge); err != nil {
		log.Printf("cleanup abandoned import staging before new import: %v", err)
	}

	var runID int64
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO import_runs (origin, source_name, snapshot_at, status)
		VALUES ($1, $2, $3, 'running')
		RETURNING id`,
		opts.Origin, opts.SourceName, opts.SnapshotAt,
	).Scan(&runID); err != nil {
		return nil, fmt.Errorf("create import_run: %w", err)
	}

	var recordID int64
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO health_records (automation_name, content_type, payload)
		VALUES ($1, 'apple-health-import', $2)
		RETURNING id`,
		opts.SourceName, fmt.Sprintf("import_run_id=%d", runID),
	).Scan(&recordID); err != nil {
		_ = s.markImportFailed(runID, fmt.Sprintf("create health_record: %v", err))
		return nil, fmt.Errorf("create import health_record: %w", err)
	}

	session := &ImportSession{
		db:             s,
		runID:          runID,
		healthRecordID: recordID,
		snapshotAt:     opts.SnapshotAt,
		counters: ImportCounters{
			ImportRunID:    runID,
			HealthRecordID: recordID,
		},
	}
	return session, nil
}

func (s *DB) ensureAppleHealthImportSchema(ctx context.Context) error {
	tables := []string{
		importRunsTableDDL,
		importRunCoverageTableDDL,
		importStagePointsTableDDL,
		importStageWorkoutsTableDDL,
	}
	for _, ddl := range tables {
		if _, err := s.pool.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("create import table: %w", err)
		}
	}
	for _, index := range importStageIndexMigrations() {
		if _, err := s.pool.Exec(ctx, index.ddl); err != nil {
			return fmt.Errorf("create import index %s: %w", index.name, err)
		}
	}
	return nil
}

func (s *ImportSession) AddPoints(points []MetricPoint) error {
	if len(points) == 0 {
		return nil
	}
	ctx, cancel := longCtx()
	defer cancel()

	rows := make([][]any, 0, len(points))
	for _, p := range points {
		localDate := p.Date
		if len(localDate) > 10 {
			localDate = localDate[:10]
		}
		if localDate == "" {
			continue
		}
		rows = append(rows, []any{s.runID, p.MetricName, p.Units, p.Date, p.Qty, p.Source, localDate})
		s.trackDate(localDate)
	}
	if len(rows) == 0 {
		return nil
	}
	n, err := s.db.pool.CopyFrom(ctx,
		pgx.Identifier{"import_stage_points"},
		[]string{"import_run_id", "metric_name", "units", "date", "qty", "source", "local_date"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("copy import points: %w", err)
	}
	s.counters.ParsedPoints += n
	return nil
}

func (s *ImportSession) AddWorkouts(workouts []Workout) error {
	if len(workouts) == 0 {
		return nil
	}
	ctx, cancel := longCtx()
	defer cancel()

	rows := make([][]any, 0, len(workouts))
	for _, w := range workouts {
		rows = append(rows, []any{
			s.runID, w.ExternalID, w.Name, w.StartTime, w.EndTime, w.DurationSec,
			w.IsIndoor, nullableString(w.Location),
			w.AvgHRBPM, w.MaxHRBPM, w.EnergyKcal, w.Intensity,
			w.DistanceKm, w.AvgSpeedKmh, w.MaxSpeedKmh, w.ElevationUpM,
			w.StepCountTotal, w.StepCadenceSpm, w.TemperatureC, w.HumidityPct,
			w.HRZ1Sec, w.HRZ2Sec, w.HRZ3Sec, w.HRZ4Sec, w.HRZ5Sec,
		})
		s.trackDate(w.StartTime.Format("2006-01-02"))
	}
	n, err := s.db.pool.CopyFrom(ctx,
		pgx.Identifier{"import_stage_workouts"},
		[]string{
			"import_run_id", "external_id", "name", "start_time", "end_time", "duration_sec",
			"is_indoor", "location", "avg_hr_bpm", "max_hr_bpm", "energy_kcal", "intensity",
			"distance_km", "avg_speed_kmh", "max_speed_kmh", "elevation_up_m",
			"step_count_total", "step_cadence_spm", "temperature_c", "humidity_pct",
			"hr_z1_sec", "hr_z2_sec", "hr_z3_sec", "hr_z4_sec", "hr_z5_sec",
		},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("copy import workouts: %w", err)
	}
	s.counters.ParsedWorkouts += n
	return nil
}

func (s *ImportSession) Commit() (ImportCounters, error) {
	ctx, cancel := longCtx()
	defer cancel()

	tx, err := s.db.pool.Begin(ctx)
	if err != nil {
		_ = s.db.markImportFailedWithCounters(s.runID, fmt.Sprintf("begin promote transaction: %v", err), s.counters)
		return s.counters, err
	}

	if err := s.promoteCoverage(ctx, tx); err != nil {
		return s.failCommit(tx, err)
	}
	if err := s.promotePoints(ctx, tx); err != nil {
		return s.failCommit(tx, err)
	}
	if err := s.promoteWorkouts(ctx, tx); err != nil {
		return s.failCommit(tx, err)
	}
	if err := s.markCommitted(ctx, tx); err != nil {
		return s.failCommit(tx, err)
	}
	if err := s.deleteStageRows(ctx, tx); err != nil {
		return s.failCommit(tx, err)
	}
	if err := tx.Commit(ctx); err != nil {
		// The outcome can be ambiguous after Commit returns an error. Leave the
		// import_run row and staging rows as forensic data instead of marking a
		// possibly committed transaction failed.
		return s.counters, err
	}
	return s.counters, nil
}

func (s *ImportSession) failCommit(tx pgx.Tx, cause error) (ImportCounters, error) {
	if err := tx.Rollback(context.Background()); err != nil {
		log.Printf("rollback import %d promote transaction: %v", s.runID, err)
	}
	if err := s.db.markImportFailedWithCounters(s.runID, cause.Error(), s.counters); err != nil {
		log.Printf("mark import %d failed: %v", s.runID, err)
	}
	if err := s.db.cleanupImportStageRows(s.runID); err != nil {
		log.Printf("cleanup stage rows for import %d: %v", s.runID, err)
	}
	return s.counters, cause
}

func (s *ImportSession) Abort(cause error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	_ = s.db.markImportFailedWithCounters(s.runID, msg, s.counters)
	_ = s.db.cleanupImportStageRows(s.runID)
}

func (s *ImportSession) promoteCoverage(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO import_run_coverage (import_run_id, metric_name, source, local_date)
		SELECT DISTINCT $1::bigint, metric_name, COALESCE(source, ''), local_date
		  FROM import_stage_points
		 WHERE import_run_id = $1
		ON CONFLICT DO NOTHING`, s.runID)
	if err != nil {
		return fmt.Errorf("promote import coverage: %w", err)
	}
	return nil
}

func (s *ImportSession) promotePoints(ctx context.Context, tx pgx.Tx) error {
	var distinctPoints, existingExact, protectedExact int64
	if err := tx.QueryRow(ctx, `
		WITH incoming AS (
			SELECT DISTINCT ON (metric_name, date, source)
			       metric_name, units, date, qty, COALESCE(source, '') AS source, local_date
			  FROM import_stage_points
			 WHERE import_run_id = $2
			 ORDER BY metric_name, date, source, staged_seq DESC
		)
		SELECT COUNT(*),
		       COUNT(mp.id),
		       COUNT(mp.id) FILTER (WHERE mp.received_at > $1)
		  FROM incoming i
		  LEFT JOIN metric_points mp
		    ON mp.metric_name = i.metric_name
		   AND mp.date = i.date
		   AND COALESCE(mp.source, '') = i.source`, s.snapshotAt, s.runID).Scan(&distinctPoints, &existingExact, &protectedExact); err != nil {
		return fmt.Errorf("count staged points: %w", err)
	}

	deleted, err := tx.Exec(ctx, `
		DELETE FROM metric_points mp
		USING import_run_coverage c
		WHERE c.import_run_id = $1
		  AND mp.metric_name = c.metric_name
		  AND COALESCE(mp.source, '') = c.source
		  AND SUBSTRING(mp.date, 1, 10) = c.local_date
		  AND mp.received_at <= $2
		  AND NOT EXISTS (
		      SELECT 1
		        FROM import_stage_points st
		       WHERE st.metric_name = mp.metric_name
		         AND st.date = mp.date
		         AND COALESCE(st.source, '') = COALESCE(mp.source, '')
		         AND st.import_run_id = $1
		  )`, s.runID, s.snapshotAt)
	if err != nil {
		return fmt.Errorf("delete stale covered points: %w", err)
	}

	_, err = tx.Exec(ctx, `
		WITH incoming AS (
			SELECT DISTINCT ON (metric_name, date, source)
			       metric_name, units, date, qty, COALESCE(source, '') AS source
			  FROM import_stage_points
			 WHERE import_run_id = $3
			 ORDER BY metric_name, date, source, staged_seq DESC
		)
		INSERT INTO metric_points
			(health_record_id, metric_name, units, date, qty, source, origin, import_run_id)
		SELECT $1, metric_name, units, date, qty, source, $2, $3
		  FROM incoming
		ON CONFLICT (metric_name, date, source) DO UPDATE SET
			received_at = NOW(),
			health_record_id = excluded.health_record_id,
			units = excluded.units,
			qty = excluded.qty,
			origin = excluded.origin,
			import_run_id = excluded.import_run_id
		WHERE metric_points.received_at <= $4`,
		s.healthRecordID, appleHealthXMLOrigin, s.runID, s.snapshotAt)
	if err != nil {
		return fmt.Errorf("upsert staged points: %w", err)
	}

	s.counters.DeletedPoints = deleted.RowsAffected()
	s.counters.UpdatedPoints = existingExact - protectedExact
	s.counters.InsertedPoints = distinctPoints - existingExact
	s.counters.SkippedPoints = s.counters.ParsedPoints - distinctPoints + protectedExact
	return nil
}

func (s *ImportSession) promoteWorkouts(ctx context.Context, tx pgx.Tx) error {
	deleted, err := tx.Exec(ctx, `
		DELETE FROM workouts w
		USING (
			SELECT DISTINCT name, start_time, end_time
			  FROM import_stage_workouts
			 WHERE external_id LIKE 'applexml:%'
			   AND import_run_id = $2
		) st
		WHERE w.origin = $1
		  AND w.external_id LIKE 'applexml:%'
		  AND w.name = st.name
		  AND w.start_time = st.start_time
		  AND w.end_time = st.end_time
		  AND NOT EXISTS (
		      SELECT 1 FROM import_stage_workouts exact
		       WHERE exact.external_id = w.external_id
		         AND exact.import_run_id = $2
		  )`, appleHealthXMLOrigin, s.runID)
	if err != nil {
		return fmt.Errorf("delete stale synthetic workouts: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		WITH incoming AS (
			SELECT DISTINCT ON (external_id)
			       external_id, name, start_time, end_time, duration_sec,
			       is_indoor, location, avg_hr_bpm, max_hr_bpm, energy_kcal, intensity,
			       distance_km, avg_speed_kmh, max_speed_kmh, elevation_up_m,
			       step_count_total, step_cadence_spm, temperature_c, humidity_pct,
			       hr_z1_sec, hr_z2_sec, hr_z3_sec, hr_z4_sec, hr_z5_sec
			  FROM import_stage_workouts
			 WHERE import_run_id = $3
			 ORDER BY external_id, staged_seq DESC
		)
		INSERT INTO workouts (
			health_record_id, external_id, name, start_time, end_time, duration_sec,
			is_indoor, location, avg_hr_bpm, max_hr_bpm, energy_kcal, intensity,
			distance_km, avg_speed_kmh, max_speed_kmh, elevation_up_m,
			step_count_total, step_cadence_spm, temperature_c, humidity_pct,
			hr_z1_sec, hr_z2_sec, hr_z3_sec, hr_z4_sec, hr_z5_sec,
			origin, import_run_id
		)
		SELECT
			$1, external_id, name, start_time, end_time, duration_sec,
			is_indoor, location, avg_hr_bpm, max_hr_bpm, energy_kcal, intensity,
			distance_km, avg_speed_kmh, max_speed_kmh, elevation_up_m,
			step_count_total, step_cadence_spm, temperature_c, humidity_pct,
			hr_z1_sec, hr_z2_sec, hr_z3_sec, hr_z4_sec, hr_z5_sec,
			$2, $3
		  FROM incoming
		ON CONFLICT (external_id) DO UPDATE SET
			received_at = NOW(),
			health_record_id = excluded.health_record_id,
			name = excluded.name,
			start_time = excluded.start_time,
			end_time = excluded.end_time,
			duration_sec = excluded.duration_sec,
			is_indoor = excluded.is_indoor,
			location = excluded.location,
			avg_hr_bpm = excluded.avg_hr_bpm,
			max_hr_bpm = excluded.max_hr_bpm,
			energy_kcal = excluded.energy_kcal,
			intensity = excluded.intensity,
			distance_km = excluded.distance_km,
			avg_speed_kmh = excluded.avg_speed_kmh,
			max_speed_kmh = excluded.max_speed_kmh,
			elevation_up_m = excluded.elevation_up_m,
			step_count_total = excluded.step_count_total,
			step_cadence_spm = excluded.step_cadence_spm,
			temperature_c = excluded.temperature_c,
			humidity_pct = excluded.humidity_pct,
			hr_z1_sec = excluded.hr_z1_sec,
			hr_z2_sec = excluded.hr_z2_sec,
			hr_z3_sec = excluded.hr_z3_sec,
			hr_z4_sec = excluded.hr_z4_sec,
			hr_z5_sec = excluded.hr_z5_sec,
			origin = excluded.origin,
			import_run_id = excluded.import_run_id`,
		s.healthRecordID, appleHealthXMLOrigin, s.runID)
	if err != nil {
		return fmt.Errorf("upsert staged workouts: %w", err)
	}
	s.counters.DeletedWorkouts = deleted.RowsAffected()
	s.counters.UpsertedWorkouts = tag.RowsAffected()
	return nil
}

func (s *ImportSession) markCommitted(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
		UPDATE import_runs
		   SET finished_at = NOW(),
		       status = 'committed',
		       min_date = $2,
		       max_date = $3,
		       parsed_points = $4,
		       inserted_points = $5,
		       updated_points = $6,
		       deleted_points = $7,
		       skipped_points = $8,
		       parsed_workouts = $9,
		       upserted_workouts = $10,
		       deleted_workouts = $11,
		       error = NULL
		 WHERE id = $1`,
		s.runID, nullableString(s.counters.MinDate), nullableString(s.counters.MaxDate),
		s.counters.ParsedPoints, s.counters.InsertedPoints, s.counters.UpdatedPoints,
		s.counters.DeletedPoints, s.counters.SkippedPoints, s.counters.ParsedWorkouts,
		s.counters.UpsertedWorkouts, s.counters.DeletedWorkouts)
	if err != nil {
		return fmt.Errorf("mark import committed: %w", err)
	}
	return nil
}

func (s *ImportSession) deleteStageRows(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `DELETE FROM import_stage_points WHERE import_run_id = $1`, s.runID); err != nil {
		return fmt.Errorf("delete staged points: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM import_stage_workouts WHERE import_run_id = $1`, s.runID); err != nil {
		return fmt.Errorf("delete staged workouts: %w", err)
	}
	return nil
}

func (s *DB) markImportFailed(runID int64, msg string) error {
	return s.markImportFailedWithCounters(runID, msg, ImportCounters{})
}

func (s *DB) markImportFailedWithCounters(runID int64, msg string, counters ImportCounters) error {
	ctx, cancel := queryCtx()
	defer cancel()
	if len(msg) > 4000 {
		msg = msg[:4000]
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE import_runs
		   SET finished_at = NOW(),
		       status = 'failed',
		       min_date = COALESCE($2, min_date),
		       max_date = COALESCE($3, max_date),
		       parsed_points = GREATEST(parsed_points, $4),
		       inserted_points = GREATEST(inserted_points, $5),
		       updated_points = GREATEST(updated_points, $6),
		       deleted_points = GREATEST(deleted_points, $7),
		       skipped_points = GREATEST(skipped_points, $8),
		       parsed_workouts = GREATEST(parsed_workouts, $9),
		       upserted_workouts = GREATEST(upserted_workouts, $10),
		       deleted_workouts = GREATEST(deleted_workouts, $11),
		       error = $12
		 WHERE id = $1`,
		runID, nullableString(counters.MinDate), nullableString(counters.MaxDate),
		counters.ParsedPoints, counters.InsertedPoints, counters.UpdatedPoints,
		counters.DeletedPoints, counters.SkippedPoints, counters.ParsedWorkouts,
		counters.UpsertedWorkouts, counters.DeletedWorkouts, msg)
	return err
}

func (s *DB) cleanupImportStageRows(runID int64) error {
	ctx, cancel := queryCtx()
	defer cancel()
	if _, err := s.pool.Exec(ctx, `DELETE FROM import_stage_points WHERE import_run_id = $1`, runID); err != nil {
		return fmt.Errorf("delete staged points for import %d: %w", runID, err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM import_stage_workouts WHERE import_run_id = $1`, runID); err != nil {
		return fmt.Errorf("delete staged workouts for import %d: %w", runID, err)
	}
	return nil
}

// CleanupAbandonedImportStages removes persistent staging rows left behind by
// completed, failed, or clearly abandoned imports. It deliberately leaves recent
// running imports alone because the CLI can run independently of the web UI.
func (s *DB) CleanupAbandonedImportStages(runningOlderThan time.Duration) error {
	ctx, cancel := longCtx()
	defer cancel()
	return s.cleanupAbandonedImportStages(ctx, runningOlderThan)
}

func (s *DB) cleanupAbandonedImportStages(ctx context.Context, runningOlderThan time.Duration) error {
	if runningOlderThan <= 0 {
		runningOlderThan = abandonedRunningImportAge
	}
	cutoff := time.Now().Add(-runningOlderThan)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin import staging cleanup: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		UPDATE import_runs
		   SET status = 'failed',
		       finished_at = COALESCE(finished_at, NOW()),
		       error = COALESCE(NULLIF(error, ''), 'import abandoned before completion')
		 WHERE status = 'running'
		   AND started_at < $1`, cutoff); err != nil {
		return fmt.Errorf("mark abandoned imports failed: %w", err)
	}

	targetRuns := `
		SELECT id
		  FROM import_runs
		 WHERE status IN ('committed', 'failed')
		    OR (status = 'running' AND started_at < $1)`
	if _, err := tx.Exec(ctx, `DELETE FROM import_stage_points WHERE import_run_id IN (`+targetRuns+`)`, cutoff); err != nil {
		return fmt.Errorf("cleanup staged points: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM import_stage_workouts WHERE import_run_id IN (`+targetRuns+`)`, cutoff); err != nil {
		return fmt.Errorf("cleanup staged workouts: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit import staging cleanup: %w", err)
	}
	return nil
}

func (s *ImportSession) trackDate(date string) {
	if date == "" {
		return
	}
	if s.counters.MinDate == "" || date < s.counters.MinDate {
		s.counters.MinDate = date
	}
	if date > s.counters.MaxDate {
		s.counters.MaxDate = date
	}
}
