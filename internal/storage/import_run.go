package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	appleHealthXMLOrigin                   = "apple_health_xml"
	appleHealthImportContentType           = "apple-health-import"
	normalizedAppleHealthImportContentType = "apple-health-import; catalog=1"
)

// ImportOptions describes one logical external import.
type ImportOptions struct {
	Origin     string
	SourceName string
	SnapshotAt time.Time
	// UnverifiedCoverage stages exact observations without deleting omitted rows.
	UnverifiedCoverage bool
	// CatalogNormalized attests that the shared XML decoder produced the staged values.
	CatalogNormalized bool
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
	db                 *DB
	runID              int64
	healthRecordID     int64
	snapshotAt         *time.Time
	unverifiedCoverage bool
	catalogNormalized  bool
	mayInvalidatePoint bool
	hasStepPoints      bool
	leaseToken         uuid.UUID
	counters           ImportCounters
	// afterLeaseLocked is used by DB-backed concurrency tests to pause after
	// Commit has acquired the import-run row lock. Production sessions leave it nil.
	afterLeaseLocked func()
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
	var snapshot *time.Time
	if !opts.SnapshotAt.IsZero() {
		snapshot = &opts.SnapshotAt
	}
	// import_runs retains a non-null timestamp for old readers. The sentinel
	// represents a missing export clock, independent of the receipt timestamp.
	runSnapshot := opts.SnapshotAt
	if snapshot == nil {
		runSnapshot = time.Unix(0, 0).UTC()
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
	leaseToken := uuid.New()
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO import_runs (origin, source_name, snapshot_at, status, heartbeat_at, lease_token)
		VALUES ($1, $2, $3, 'running', NOW(), $4)
		RETURNING id`,
		opts.Origin, opts.SourceName, runSnapshot, leaseToken,
	).Scan(&runID); err != nil {
		return nil, fmt.Errorf("acquire active import lease: %w", err)
	}

	contentType := appleHealthImportContentType
	if opts.CatalogNormalized {
		contentType = normalizedAppleHealthImportContentType
	}
	var recordID int64
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO health_records (automation_name, content_type, payload)
		VALUES ($1, $2, $3)
		RETURNING id`,
		opts.SourceName, contentType, fmt.Sprintf("import_run_id=%d", runID),
	).Scan(&recordID); err != nil {
		_ = s.markImportFailed(runID, fmt.Sprintf("create health_record: %v", err))
		return nil, fmt.Errorf("create import health_record: %w", err)
	}

	session := &ImportSession{
		db:                 s,
		runID:              runID,
		healthRecordID:     recordID,
		snapshotAt:         snapshot,
		unverifiedCoverage: opts.UnverifiedCoverage || snapshot == nil,
		catalogNormalized:  opts.CatalogNormalized,
		leaseToken:         leaseToken,
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
	for _, ddl := range []string{
		`ALTER TABLE import_runs ADD COLUMN IF NOT EXISTS heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`,
		`ALTER TABLE import_runs ADD COLUMN IF NOT EXISTS lease_token UUID`,
	} {
		if _, err := s.pool.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("migrate import lease schema: %w", err)
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
	if err := s.touchLease(ctx); err != nil {
		return err
	}

	rows := make([][]any, 0, len(points))
	mayInvalidatePoint := false
	for _, p := range points {
		p = normalizeStoredMetricPoint(p)
		if usesSeparatedArchiveSamples(p.MetricName) {
			p.MetricName = archiveSamplePrefix + p.MetricName
		}
		localDate := p.Date
		if len(localDate) > 10 {
			localDate = localDate[:10]
		}
		if localDate == "" {
			continue
		}
		s.hasStepPoints = s.hasStepPoints || p.MetricName == "step_count"
		rows = append(rows, []any{s.runID, p.MetricName, p.Units, p.Date, p.Qty, p.Source, localDate})
		mayInvalidatePoint = mayInvalidatePoint || ((p.MetricName == "heart_rate_variability" || p.MetricName == "step_count") && float32(p.Qty) <= 0)
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
	s.mayInvalidatePoint = s.mayInvalidatePoint || mayInvalidatePoint
	return nil
}

func (s *ImportSession) AddWorkouts(workouts []Workout) error {
	if len(workouts) == 0 {
		return nil
	}
	ctx, cancel := longCtx()
	defer cancel()
	if err := s.touchLease(ctx); err != nil {
		return err
	}

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

func (s *ImportSession) touchLease(ctx context.Context) error {
	tag, err := s.db.pool.Exec(ctx, `UPDATE import_runs SET heartbeat_at=NOW() WHERE id=$1 AND status='running' AND lease_token=$2`, s.runID, s.leaseToken)
	if err != nil {
		return fmt.Errorf("refresh active import lease: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("active import lease is no longer owned")
	}
	return nil
}

func (s *ImportSession) Commit() (ImportCounters, error) {
	s.db.cacheMu.Lock()
	defer s.db.cacheMu.Unlock()

	ctx, cancel := longCtx()
	defer cancel()

	tx, err := s.db.pool.Begin(ctx)
	if err != nil {
		s.markFailedAndCleanup(fmt.Sprintf("begin promote transaction: %v", err))
		return s.counters, err
	}
	var leaseOwned bool
	if err := tx.QueryRow(ctx, `SELECT true FROM import_runs WHERE id=$1 AND status='running' AND lease_token=$2 FOR UPDATE`, s.runID, s.leaseToken).Scan(&leaseOwned); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = errors.New("active import lease is no longer owned")
		}
		return s.failCommit(tx, err)
	}
	if s.afterLeaseLocked != nil {
		s.afterLeaseLocked()
	}

	if err := s.promoteCoverage(ctx, tx); err != nil {
		return s.failCommit(tx, err)
	}
	coverageDates, err := importCoverageDates(ctx, tx, s.runID)
	if err != nil {
		return s.failCommit(tx, err)
	}
	// Keep the established journal-before-source lock order while deriving
	// the repair subset from the mutations that actually succeed below.
	cacheState, err := lockCacheMaintenanceRow(ctx, tx)
	if err != nil {
		return s.failCommit(tx, fmt.Errorf("lock imported cache dates: %w", err))
	}
	hrvRepairDates, err := s.promotePoints(ctx, tx)
	if err != nil {
		return s.failCommit(tx, err)
	}
	if err := s.db.recordCacheDirtyWithHRVRepairsTx(ctx, tx, cacheState, coverageDates, hrvRepairDates); err != nil {
		return s.failCommit(tx, fmt.Errorf("mark imported cache dates dirty: %w", err))
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

func importCoverageDates(ctx context.Context, tx pgx.Tx, runID int64) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT local_date FROM import_run_coverage WHERE import_run_id = $1`, runID)
	if err != nil {
		return nil, fmt.Errorf("read import coverage dates: %w", err)
	}
	defer rows.Close()
	var dates []string
	for rows.Next() {
		var date string
		if err := rows.Scan(&date); err != nil {
			return nil, fmt.Errorf("scan import coverage date: %w", err)
		}
		dates = append(dates, date)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate import coverage dates: %w", err)
	}
	return dates, nil
}

func (s *ImportSession) failCommit(tx pgx.Tx, cause error) (ImportCounters, error) {
	if err := tx.Rollback(context.Background()); err != nil {
		log.Printf("rollback import %d promote transaction: %v", s.runID, err)
	}
	s.markFailedAndCleanup(cause.Error())
	return s.counters, cause
}

func (s *ImportSession) Abort(cause error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	s.markFailedAndCleanup(msg)
}

func (s *ImportSession) markFailed(msg string) (bool, error) {
	return s.db.updateImportFailed(s.runID, msg, s.counters, &s.leaseToken)
}

func (s *ImportSession) markFailedAndCleanup(msg string) {
	owned, err := s.markFailed(msg)
	if err != nil {
		log.Printf("mark import %d failed: %v", s.runID, err)
		return
	}
	if !owned {
		log.Printf("skip import %d failure cleanup: active lease is no longer owned", s.runID)
		return
	}
	if err := s.db.cleanupImportStageRows(s.runID); err != nil {
		log.Printf("cleanup stage rows for import %d: %v", s.runID, err)
	}
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

func (s *ImportSession) promotePoints(ctx context.Context, tx pgx.Tx) ([]string, error) {
	overlapKeys := []string{}
	var archivedStepCells, changedStepKeys []byte
	if s.hasStepPoints {
		var err error
		changedStepKeys, err = s.positiveStepChangesBeforeImport(ctx, tx)
		if err != nil {
			return nil, err
		}
	}
	if s.catalogNormalized {
		overlapKeys = overlappingArchiveSampleMetrics
		var err error
		archivedStepCells, err = s.preserveArchiveHourBoundarySamples(ctx, tx)
		if err != nil {
			return nil, err
		}
	}
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
		       COUNT(*) FILTER (WHERE mp.id IS NOT NULL OR `+newerLegacyMetricExists("i", "$1")+`),
		       COUNT(*) FILTER (WHERE (mp.id IS NOT NULL AND ($1::timestamptz IS NULL OR
		         COALESCE(mp.source_snapshot_at, CASE WHEN mp.origin <> 'apple_health_xml' THEN mp.received_at END, '-infinity') > $1
		         OR `+protectedCurrentHourlyPoint("mp", "$3")+`)) OR `+newerLegacyMetricExists("i", "$1")+`)
		  FROM incoming i
		  LEFT JOIN metric_points mp
		    ON mp.metric_name = i.metric_name
		   AND mp.date = i.date
		   AND COALESCE(mp.source, '') = i.source`, s.snapshotAt, s.runID, overlapKeys).Scan(&distinctPoints, &existingExact, &protectedExact); err != nil {
		return nil, fmt.Errorf("count staged points: %w", err)
	}

	var deletedPoints int64
	var deletedHRVDates []string
	var deletedStepCells, replacedStepCells []byte
	err := tx.QueryRow(ctx, `
		WITH covered AS (
			SELECT metric_name,source,local_date FROM import_run_coverage WHERE import_run_id=$1
			UNION ALL
			SELECT legacy_name,source,local_date FROM (
				SELECT `+legacyMetricSQL("metric_name")+` AS legacy_name,source,local_date
				FROM import_run_coverage WHERE import_run_id=$1
			) aliases WHERE legacy_name IS NOT NULL
			UNION ALL
			SELECT $5||metric_name,source,local_date FROM import_run_coverage
			WHERE import_run_id=$1 AND metric_name=ANY($4::text[])
		), deleted AS (
		DELETE FROM metric_points mp
		USING covered c
		WHERE $3 AND NOT `+protectedCurrentHourlyPoint("mp", "$4")+`
		  AND mp.metric_name = c.metric_name
		  AND COALESCE(mp.source, '') = c.source
		  AND SUBSTRING(mp.date, 1, 10) = c.local_date
		  AND COALESCE(mp.source_snapshot_at, mp.received_at) <= $2
		  AND NOT EXISTS (
		      SELECT 1
		        FROM import_stage_points st
		       WHERE (st.metric_name = mp.metric_name OR
		            (st.metric_name=ANY($4::text[]) AND $5||st.metric_name=mp.metric_name))
		         AND st.date = mp.date
		         AND COALESCE(st.source, '') = COALESCE(mp.source, '')
		         AND st.import_run_id = $1
		  )
		RETURNING CASE WHEN mp.metric_name='heart_rate_variability' AND mp.quality='ok' AND mp.qty>0
			THEN SUBSTRING(mp.date,1,10) END AS hrv_day,
			CASE WHEN `+usableStepPointSQL("mp")+`
			THEN jsonb_build_array(substring(mp.date,1,16),coalesce(mp.source,'')) END AS step_cell
		)
		SELECT count(*), array_agg(DISTINCT hrv_day) FILTER (WHERE hrv_day IS NOT NULL),
			jsonb_agg(DISTINCT step_cell) FILTER (WHERE step_cell IS NOT NULL)
		FROM deleted`, s.runID, s.snapshotAt, !s.unverifiedCoverage, overlapKeys, archiveSamplePrefix).Scan(&deletedPoints, &deletedHRVDates, &deletedStepCells)
	if err != nil {
		return nil, fmt.Errorf("delete stale covered points: %w", err)
	}

	// Collect accepted transitions only when the staging input contains a
	// potentially withdrawing value. Ordinary positive archive imports keep
	// the direct set-based upsert path.
	incomingSQL := `
		WITH incoming AS (
			SELECT DISTINCT ON (metric_name, date, source)
			       metric_name, units, date, qty, COALESCE(source, '') AS source
			  FROM import_stage_points
			 WHERE import_run_id = $3
			 ORDER BY metric_name, date, source, staged_seq DESC
		)`
	upsertSQL := `
		INSERT INTO metric_points
			(health_record_id, metric_name, units, date, qty, source, origin, import_run_id, source_snapshot_at)
		SELECT $1, i.metric_name, i.units, i.date, i.qty, i.source, $2, $3, $4
		  FROM incoming i
		 WHERE NOT ` + newerLegacyMetricExists("i", "$4") + `
		ON CONFLICT (metric_name, date, source) DO UPDATE SET
			received_at = NOW(),
			health_record_id = excluded.health_record_id,
			units = excluded.units,
			qty = excluded.qty,
			origin = excluded.origin,
			import_run_id = excluded.import_run_id,
			source_snapshot_at = excluded.source_snapshot_at
		WHERE COALESCE(metric_points.source_snapshot_at,
		      CASE WHEN metric_points.origin <> 'apple_health_xml' THEN metric_points.received_at END, '-infinity')
		      <= excluded.source_snapshot_at
		  AND NOT ` + protectedCurrentHourlyPoint("metric_points", "$5")
	var invalidatedHRVDates []string
	if s.mayInvalidatePoint {
		err = tx.QueryRow(ctx, incomingSQL+`, invalidated AS MATERIALIZED (
			SELECT mp.id, mp.metric_name, substring(mp.date,1,10) AS day,
			       substring(mp.date,1,16) AS minute, mp.source
			FROM incoming i JOIN metric_points mp
			  ON mp.metric_name=i.metric_name AND mp.date=i.date AND mp.source=i.source
			WHERE ((mp.metric_name='heart_rate_variability' AND mp.quality='ok' AND mp.qty>0) OR (`+usableStepPointSQL("mp")+`))
			  AND NOT COALESCE(i.qty>0,false)
		), upserted AS (`+upsertSQL+` RETURNING id)
		SELECT array_agg(DISTINCT day) FILTER (WHERE metric_name='heart_rate_variability'),
		       jsonb_agg(DISTINCT jsonb_build_array(minute,coalesce(source,''))) FILTER (WHERE metric_name='step_count')
		FROM invalidated JOIN upserted USING (id)`,
			s.healthRecordID, appleHealthXMLOrigin, s.runID, s.snapshotAt, overlapKeys).Scan(&invalidatedHRVDates, &replacedStepCells)
	} else {
		_, err = tx.Exec(ctx, incomingSQL+upsertSQL,
			s.healthRecordID, appleHealthXMLOrigin, s.runID, s.snapshotAt, overlapKeys)
	}
	if err != nil {
		return nil, fmt.Errorf("upsert staged points: %w", err)
	}

	var changedStepCells []byte
	if len(changedStepKeys) > 0 {
		err = tx.QueryRow(ctx, `SELECT jsonb_agg(DISTINCT jsonb_build_array(substring(p.date,1,16),coalesce(p.source,'')))
			FROM jsonb_array_elements($1::jsonb) k JOIN metric_points p ON p.metric_name=k->>0 AND p.date=k->>1 AND p.source=k->>2
			WHERE p.import_run_id=$2`, json.RawMessage(changedStepKeys), s.runID).Scan(&changedStepCells)
		if err != nil {
			return nil, err
		}
	}
	if err := repairWithdrawnSteps(ctx, tx, deletedStepCells, replacedStepCells, archivedStepCells, changedStepCells); err != nil {
		return nil, err
	}
	s.counters.DeletedPoints = deletedPoints
	s.counters.UpdatedPoints = existingExact - protectedExact
	s.counters.InsertedPoints = distinctPoints - existingExact
	s.counters.SkippedPoints = s.counters.ParsedPoints - distinctPoints + protectedExact
	return append(deletedHRVDates, invalidatedHRVDates...), nil
}

// Compare positive corrections and additions against the cache before XML
// promotion. Only accepted keys are repaired once snapshot precedence resolves.
func (s *ImportSession) positiveStepChangesBeforeImport(ctx context.Context, tx pgx.Tx) ([]byte, error) {
	var changed []byte
	err := tx.QueryRow(ctx, `WITH incoming AS (
		SELECT DISTINCT ON (date,source) date,source,qty,units FROM import_stage_points
		WHERE import_run_id=$1 AND metric_name='step_count' ORDER BY date,source,staged_seq DESC
	), keys AS (
		SELECT 'step_count'::text AS metric_name,* FROM incoming WHERE qty>0
		UNION ALL SELECT $2::text,* FROM incoming WHERE $3 AND qty>0 AND substring(date,15,5)='00:00'
	), `+stepHourProofCTEs()+`
	SELECT jsonb_agg(jsonb_build_array(k.metric_name,k.date,k.source))
	FROM keys k LEFT JOIN metric_points p ON p.metric_name=k.metric_name AND p.date=k.date AND p.source=k.source
	WHERE (p.qty IS DISTINCT FROM k.qty OR p.units IS DISTINCT FROM k.units OR p.origin IS DISTINCT FROM 'apple_health_xml')
	  AND (p.id IS NOT NULL OR EXISTS (SELECT FROM wall_cache c WHERE c.hour=substring(k.date,1,13)||':00'))
	  AND NOT EXISTS (SELECT FROM incomplete i WHERE i.hour=substring(k.date,1,13)||':00')`,
		s.runID, archiveSamplePrefix+"step_count", s.catalogNormalized).Scan(&changed)
	return changed, err
}

// Core SUM metrics retain their original key for the existing v1 caches.
// Only hour-aligned archive samples can collide with the live v2 wire key.
func (s *ImportSession) preserveArchiveHourBoundarySamples(ctx context.Context, tx pgx.Tx) ([]byte, error) {
	const incoming = `
		WITH incoming AS (
			SELECT DISTINCT ON (metric_name, date, source) metric_name, date, source, units, qty
			FROM import_stage_points
			WHERE import_run_id=$3 AND metric_name=ANY($5::text[]) AND SUBSTRING(date,15,5)='00:00'
			ORDER BY metric_name,date,source,staged_seq DESC
		)`
	const upsert = `INSERT INTO metric_points (health_record_id,metric_name,date,source,units,qty,origin,import_run_id,source_snapshot_at)
		SELECT $1,$6||metric_name,date,source,units,qty,$2,$3,$4 FROM incoming
		ON CONFLICT(metric_name,date,source) DO UPDATE SET
		    health_record_id=excluded.health_record_id, units=excluded.units, qty=excluded.qty,
		    import_run_id=excluded.import_run_id, source_snapshot_at=excluded.source_snapshot_at
		WHERE COALESCE(metric_points.source_snapshot_at,'-infinity') <= excluded.source_snapshot_at`
	args := []any{s.healthRecordID, appleHealthXMLOrigin, s.runID, s.snapshotAt, overlappingArchiveSampleMetrics, archiveSamplePrefix}
	var cells []byte
	var err error
	if s.mayInvalidatePoint {
		err = tx.QueryRow(ctx, incoming+`, invalidated AS MATERIALIZED (
			SELECT p.id,substring(p.date,1,16) AS minute,p.source
			FROM incoming i JOIN metric_points p ON p.metric_name=$6||i.metric_name AND p.date=i.date AND p.source=i.source
			WHERE i.metric_name='step_count' AND i.qty<=0 AND p.qty>0 AND p.quality='ok'
		), upserted AS (`+upsert+` RETURNING id)
		SELECT jsonb_agg(DISTINCT jsonb_build_array(minute,coalesce(source,'')))
		FROM invalidated JOIN upserted USING(id)`, args...).Scan(&cells)
	} else {
		_, err = tx.Exec(ctx, incoming+" "+upsert, args...)
	}
	if err != nil {
		return nil, fmt.Errorf("preserve archive samples at hour boundaries: %w", err)
	}
	return cells, nil
}

func protectedCurrentHourlyPoint(row, keys string) string {
	return `(` + row + `.metric_name=ANY(` + keys + `::text[]) AND ` + row + `.origin='live'
	 AND EXISTS (SELECT 1 FROM health_records hr WHERE hr.id=` + row + `.health_record_id
	             AND hr.content_type='application/json; metrics-contract=2'))`
}

func (s *ImportSession) promoteWorkouts(ctx context.Context, tx pgx.Tx) error {
	// A dated export can replace a synthetic ID even with incomplete coverage.
	// Require one distinct incoming ID and one stored ID for the exact interval.
	// An ID taken over by live sync still blocks a duplicate XML correction.
	err := tx.QueryRow(ctx, `
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
		), matched AS (
			SELECT i.external_id AS incoming_id, MIN(w.external_id) AS stored_id,
			       MIN(COALESCE(w.source_snapshot_at, w.received_at)) AS stored_snapshot,
			       MIN(w.received_at) AS stored_receipt
			FROM incoming i
			JOIN workouts w ON (w.name,w.start_time,w.end_time)=(i.name,i.start_time,i.end_time)
			WHERE i.external_id LIKE 'applexml:%' AND w.external_id LIKE 'applexml:%'
			  AND w.origin IN ($2,'live') AND $4::timestamptz IS NOT NULL
			  AND NOT EXISTS (
			      SELECT 1 FROM incoming other
			      WHERE (other.name,other.start_time,other.end_time)=(i.name,i.start_time,i.end_time)
			        AND other.external_id<>i.external_id
			  )
			GROUP BY i.external_id
			HAVING COUNT(*)=1 AND MIN(w.external_id)<>i.external_id
		), replaced AS (
			DELETE FROM workouts w USING matched m
			WHERE w.external_id=m.stored_id AND w.origin=$2 AND m.stored_snapshot<$4
			  AND COALESCE(w.source_snapshot_at,w.received_at)=m.stored_snapshot
			  AND w.received_at IS NOT DISTINCT FROM m.stored_receipt
			RETURNING m.incoming_id
		), promoted AS (
		INSERT INTO workouts (
			health_record_id, external_id, name, start_time, end_time, duration_sec,
			is_indoor, location, avg_hr_bpm, max_hr_bpm, energy_kcal, intensity,
			distance_km, avg_speed_kmh, max_speed_kmh, elevation_up_m,
			step_count_total, step_cadence_spm, temperature_c, humidity_pct,
			hr_z1_sec, hr_z2_sec, hr_z3_sec, hr_z4_sec, hr_z5_sec,
			origin, import_run_id, source_snapshot_at
		)
		SELECT
			$1, external_id, name, start_time, end_time, duration_sec,
			is_indoor, location, avg_hr_bpm, max_hr_bpm, energy_kcal, intensity,
			distance_km, avg_speed_kmh, max_speed_kmh, elevation_up_m,
			step_count_total, step_cadence_spm, temperature_c, humidity_pct,
			hr_z1_sec, hr_z2_sec, hr_z3_sec, hr_z4_sec, hr_z5_sec,
			$2, $3, $4
		FROM incoming i
		WHERE NOT EXISTS (
			SELECT 1 FROM matched m WHERE m.incoming_id=i.external_id
			  AND NOT EXISTS (SELECT 1 FROM replaced r WHERE r.incoming_id=m.incoming_id)
		)
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
			import_run_id = excluded.import_run_id,
			source_snapshot_at = excluded.source_snapshot_at
		WHERE COALESCE(workouts.source_snapshot_at,
		      CASE WHEN workouts.origin <> 'apple_health_xml' THEN workouts.received_at END, '-infinity')
		      <= excluded.source_snapshot_at
		RETURNING 1
		)
		SELECT (SELECT COUNT(*) FROM replaced),(SELECT COUNT(*) FROM promoted)`,
		s.healthRecordID, appleHealthXMLOrigin, s.runID, s.snapshotAt).
		Scan(&s.counters.DeletedWorkouts, &s.counters.UpsertedWorkouts)
	if err != nil {
		return fmt.Errorf("promote staged workouts: %w", err)
	}
	return nil
}

func (s *ImportSession) markCommitted(ctx context.Context, tx pgx.Tx) error {
	tag, err := tx.Exec(ctx, `
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
		 WHERE id = $1 AND status = 'running' AND lease_token = $12`,
		s.runID, nullableString(s.counters.MinDate), nullableString(s.counters.MaxDate),
		s.counters.ParsedPoints, s.counters.InsertedPoints, s.counters.UpdatedPoints,
		s.counters.DeletedPoints, s.counters.SkippedPoints, s.counters.ParsedWorkouts,
		s.counters.UpsertedWorkouts, s.counters.DeletedWorkouts, s.leaseToken)
	if err != nil {
		return fmt.Errorf("mark import committed: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("active import lease is no longer owned")
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
	_, err := s.updateImportFailed(runID, msg, counters, nil)
	return err
}

func (s *DB) updateImportFailed(runID int64, msg string, counters ImportCounters, leaseToken *uuid.UUID) (bool, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	if len(msg) > 4000 {
		msg = msg[:4000]
	}
	var leaseParam any
	if leaseToken != nil {
		leaseParam = *leaseToken
	}
	tag, err := s.pool.Exec(ctx, `
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
		 WHERE id = $1
		   AND ($13::uuid IS NULL OR (status = 'running' AND lease_token = $13))`,
		runID, nullableString(counters.MinDate), nullableString(counters.MaxDate),
		counters.ParsedPoints, counters.InsertedPoints, counters.UpdatedPoints,
		counters.DeletedPoints, counters.SkippedPoints, counters.ParsedWorkouts,
		counters.UpsertedWorkouts, counters.DeletedWorkouts, msg, leaseParam)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
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
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		UPDATE import_runs
		   SET status = 'failed',
		       finished_at = COALESCE(finished_at, NOW()),
		       error = COALESCE(NULLIF(error, ''), 'import abandoned before completion')
		 WHERE status = 'running'
		   AND COALESCE(heartbeat_at, started_at) < $1`, cutoff); err != nil {
		return fmt.Errorf("mark abandoned imports failed: %w", err)
	}

	targetRuns := `
		SELECT id
		  FROM import_runs
		 WHERE status IN ('committed', 'failed')
		    OR (status = 'running' AND COALESCE(heartbeat_at, started_at) < $1)`
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
