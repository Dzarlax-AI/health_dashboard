package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"health-receiver/internal/health"

	"github.com/jackc/pgx/v5"
)

const nightIngestDiagnosticsKey = "night_ingest_versions"

// The immutable receipt order handles out-of-order worker replay. Keeping
// accepted full fingerprints also handles resubmissions with a new record ID.
type nightIngestVersion struct {
	RecordID   int64           `json:"record_id"`
	ReceivedAt time.Time       `json:"received_at"`
	Active     string          `json:"active"`
	FullSeen   map[string]bool `json:"full_seen,omitempty"`
}

type nightIngestKey struct {
	wakeDate, source string
}

type nightIngestEntry struct {
	key         nightIngestKey
	point       MetricPoint
	coverage    NightSleepCoverageCommitment
	conflicting bool
}

func lockNightSleepIngest(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('health/night-ingest/' || current_schema(), 0))`)
	return err
}

// InsertAcceptedPoints applies explicitly paired nightly observations in the
// same transaction as their points and canonical projection. A failed write
// leaves the raw record available to the normal accepted-record replay.
func (s *DB) InsertAcceptedPoints(recordID int64, points []MetricPoint, coverage []NightSleepCoverageCommitment) ([]MetricPoint, error) {
	ordinary := make([]MetricPoint, 0, len(points))
	var nightPoints []MetricPoint
	for _, point := range points {
		if point.MetricName == "night_sleep_total" {
			nightPoints = append(nightPoints, point)
		} else {
			ordinary = append(ordinary, point)
		}
	}
	if len(nightPoints) == 0 {
		return points, s.InsertPoints(recordID, points)
	}

	loc := s.reportTZLocation()
	entries := nightIngestEntries(nightPoints, coverage, loc)
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	ctx, cancel := longCtx()
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = lockNightSleepIngest(ctx, tx); err != nil {
		return nil, err
	}
	var received time.Time
	if err = tx.QueryRow(ctx, `SELECT received_at FROM health_records WHERE id=$1`, recordID).Scan(&received); err != nil {
		return nil, err
	}

	accepted := ordinary
	var extraDates []string
	for _, entry := range entries {
		applied, displacedDate, err := applyNightIngestEntry(ctx, tx, entry, recordID, received, loc, s.pointOrdering())
		if err != nil {
			return nil, err
		}
		if applied {
			accepted = append(accepted, entry.point)
			if entry.coverage.InputHash != "" && !strings.HasPrefix(entry.point.Date, entry.key.wakeDate) {
				extraDates = append(extraDates, entry.key.wakeDate)
			}
		}
		if displacedDate != "" {
			extraDates = append(extraDates, displacedDate)
		}
	}
	if err = insertPointsTx(ctx, tx, recordID, ordinary, s.pointOrdering()); err != nil {
		return nil, err
	}
	dates := append(cacheDatesFromPoints(accepted), extraDates...)
	if len(dates) > 0 {
		if err = s.MarkCacheDirtyTx(ctx, tx, dates); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return accepted, nil
}

func nightIngestEntries(points []MetricPoint, coverages []NightSleepCoverageCommitment, loc *time.Location) []nightIngestEntry {
	byKey := make(map[nightIngestKey]nightIngestEntry)
	for _, point := range points {
		at, err := parseMetricDate(point.Date)
		if err != nil || point.Qty <= 0 || math.IsNaN(point.Qty) || math.IsInf(point.Qty, 0) {
			continue
		}
		key := nightIngestKey{at.In(loc).Format(time.DateOnly), point.Source}
		entry, exists := byKey[key]
		if !exists {
			byKey[key] = nightIngestEntry{key: key, point: point}
		} else if entry.point != point {
			entry.conflicting = true
			byKey[key] = entry
		}
	}
	for _, coverage := range coverages {
		key := nightIngestKey{coverage.WakeDate, coverage.Source}
		entry, exists := byKey[key]
		if !exists || (coverage.CaptureCompleteness != health.NightCaptureComplete && coverage.CaptureCompleteness != health.NightCapturePartial) {
			continue
		}
		if coverage.MetricDate != entry.point.Date || validateNightSleepWindow(coverage, loc) != nil ||
			(entry.coverage.InputHash != "" && entry.coverage.InputHash != coverage.InputHash) {
			entry.conflicting = true
		} else if entry.point.Units == "hr" {
			entry.coverage = coverage
		} else {
			entry.conflicting = true
		}
		byKey[key] = entry
	}
	entries := make([]nightIngestEntry, 0, len(byKey))
	for _, entry := range byKey {
		if !entry.conflicting {
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		l, r := entries[i], entries[j]
		if l.key.wakeDate != r.key.wakeDate {
			return l.key.wakeDate < r.key.wakeDate
		}
		lf, rf := l.coverage.CaptureCompleteness == health.NightCaptureComplete, r.coverage.CaptureCompleteness == health.NightCaptureComplete
		if lf != rf {
			return lf
		}
		return l.key.source < r.key.source
	})
	return entries
}

func nightIngestFingerprint(coverageHash, units string, hours float64) string {
	// metric_points stores REAL. Use its precision for both new and legacy
	// versions so loading a previously accepted value does not change identity.
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s\x1f%s\x1f%g", coverageHash, units, float32(hours)))))
}

type priorNightIngest struct {
	date     string
	hash     string
	capture  string
	origin   string
	units    string
	hours    float64
	recordID int64
	received time.Time
}

func hasNightPointConflict(ctx context.Context, tx pgx.Tx, entry nightIngestEntry, priorDate string, loc *time.Location) (bool, error) {
	// Writer and tenant offsets can put their calendar dates up to two days
	// apart. Use the indexed prefix only to bound the scan, then compare instants
	// with the same parser and location used to validate the commitment.
	noon := entry.coverage.CoveredIntervalEnd.In(loc)
	rows, err := tx.Query(ctx, `
		SELECT date FROM metric_points
		 WHERE metric_name='night_sleep_total' AND source=$1
		   AND substring(date,1,10) BETWEEN $2 AND $3
		   AND ((date=$4 AND origin<>'live') OR
		        (date<>$4 AND date<>$5 AND qty>0 AND quality='ok'))`,
		entry.key.source, noon.AddDate(0, 0, -2).Format(time.DateOnly), noon.AddDate(0, 0, 2).Format(time.DateOnly),
		entry.point.Date, priorDate)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var stamp string
		if err := rows.Scan(&stamp); err != nil {
			return false, err
		}
		if stamp == entry.point.Date {
			return true, nil
		}
		at, err := parseMetricDate(stamp)
		if err != nil {
			// Preserve the conservative guard for legacy, unreadable timestamps.
			if strings.HasPrefix(stamp, entry.point.Date[:len(time.DateOnly)]) || strings.HasPrefix(stamp, entry.key.wakeDate) {
				return true, nil
			}
			continue
		}
		if at.In(loc).Format(time.DateOnly) == entry.key.wakeDate {
			return true, nil
		}
	}
	return false, rows.Err()
}

func applyNightIngestEntry(ctx context.Context, tx pgx.Tx, entry nightIngestEntry, id int64, received time.Time, loc *time.Location, ordering ingestPointOrdering) (bool, string, error) {
	day, source := entry.key.wakeDate, entry.key.source
	full := entry.coverage.CaptureCompleteness == health.NightCaptureComplete
	if !full {
		var protected bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT FROM night_sleep_coverage_commitments
			 WHERE wake_date=$1 AND (capture_completeness='complete' OR (source=$2 AND $3))
		) OR EXISTS (
			SELECT FROM completed_night_sleep WHERE wake_date=$1 AND capture_completeness='complete'
		)`, day, source, entry.coverage.InputHash == "").Scan(&protected)
		if err != nil || protected {
			return false, "", err
		}
	}
	if entry.coverage.InputHash == "" {
		err := insertPointsTx(ctx, tx, id, []MetricPoint{entry.point}, ordering)
		return err == nil, "", err
	}

	var prior priorNightIngest
	err := tx.QueryRow(ctx, `
		SELECT c.metric_date,c.input_hash,c.capture_completeness,COALESCE(m.origin,''),
		       COALESCE(m.units,''),COALESCE(m.qty,0),COALESCE(m.health_record_id,0),COALESCE(h.received_at,c.observed_at)
		  FROM night_sleep_coverage_commitments c
		  LEFT JOIN metric_points m ON m.metric_name='night_sleep_total' AND m.date=c.metric_date AND m.source=c.source
		  LEFT JOIN health_records h ON h.id=m.health_record_id
		 WHERE c.wake_date=$1 AND c.source=$2`, day, source).Scan(
		&prior.date, &prior.hash, &prior.capture, &prior.origin, &prior.units, &prior.hours, &prior.recordID, &prior.received)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, "", err
	}

	versions := make(map[string]nightIngestVersion)
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT diagnostics->$2 FROM completed_night_sleep WHERE wake_date=$1`, day, nightIngestDiagnosticsKey).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, "", err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &versions); err != nil {
			return false, "", err
		}
	}
	if versions == nil {
		versions = make(map[string]nightIngestVersion)
	}
	version, exists := versions[source]
	if version.FullSeen == nil {
		version.FullSeen = make(map[string]bool)
	}
	if !exists && prior.recordID != 0 && prior.origin == "live" {
		version.RecordID, version.ReceivedAt = prior.recordID, prior.received
		version.Active = nightIngestFingerprint(prior.hash, prior.units, prior.hours)
		if prior.capture == health.NightCaptureComplete {
			version.FullSeen[version.Active] = true
		}
	}
	fingerprint := nightIngestFingerprint(entry.coverage.InputHash, entry.point.Units, entry.point.Qty)
	if version.Active == fingerprint || (full && version.FullSeen[fingerprint]) {
		return false, "", nil
	}
	olderReceipt := received.Before(version.ReceivedAt) || (received.Equal(version.ReceivedAt) && id <= version.RecordID)
	if olderReceipt && !(full && prior.capture == health.NightCapturePartial) {
		if full {
			version.FullSeen[fingerprint] = true
			versions[source] = version
			return false, "", persistNightIngestVersions(ctx, tx, day, versions)
		}
		return false, "", nil
	}

	if ambiguousKey, err := hasNightPointConflict(ctx, tx, entry, prior.date, loc); err != nil || ambiguousKey {
		return false, "", err
	}

	// With an existing claimed key, prove ownership of the superseded point
	// through the same-source commitment and its previously accepted live row.
	// An unrelated import remains untouched and needs review if it conflicts.
	if prior.date == entry.point.Date && prior.recordID != 0 && prior.origin != "live" {
		return false, "", nil
	}
	var displacedDate string
	if prior.date != "" && prior.date != entry.point.Date && prior.recordID != 0 {
		if prior.origin != "live" || prior.recordID != version.RecordID {
			return false, "", nil
		}
		if _, err = tx.Exec(ctx, `DELETE FROM metric_points WHERE metric_name='night_sleep_total'
			AND date=$1 AND source=$2 AND health_record_id=$3 AND origin='live'`, prior.date, source, prior.recordID); err != nil {
			return false, "", err
		}
		displacedDate = prior.date[:len(time.DateOnly)]
	}

	entry.coverage.ObservedAt = received
	// Store the bound pair identity. Same coverage metadata with a corrected
	// measurement can therefore update both the point and the commitment.
	entry.coverage.InputHash = fingerprint
	if err = insertPointsTx(ctx, tx, id, []MetricPoint{entry.point}, coveredNightOrdering); err != nil {
		return false, "", err
	}
	if err = saveNightSleepCoverage(ctx, tx, entry.coverage); err != nil {
		return false, "", err
	}
	if err = reconcileCompletedNightSleep(ctx, tx, day, time.Now(), loc); err != nil {
		return false, "", err
	}
	version.Active, version.RecordID, version.ReceivedAt = fingerprint, id, received
	if full {
		version.FullSeen[fingerprint] = true
	}
	versions[source] = version
	return true, displacedDate, persistNightIngestVersions(ctx, tx, day, versions)
}

func persistNightIngestVersions(ctx context.Context, tx pgx.Tx, date string, versions map[string]nightIngestVersion) error {
	body, err := json.Marshal(versions)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE completed_night_sleep
		SET diagnostics = COALESCE(NULLIF(diagnostics,'null'::jsonb),'{}'::jsonb) || jsonb_build_object($2::text, $3::jsonb)
		WHERE wake_date=$1`, date, nightIngestDiagnosticsKey, json.RawMessage(body))
	return err
}
