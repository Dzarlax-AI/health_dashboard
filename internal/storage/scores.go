package storage

import (
	"fmt"
	"log"
	"time"

	"health-receiver/internal/health"
)

// RunIncrementalBackfill fills all pre-aggregated caches for data that is
// not yet cached. Safe to call from a goroutine at any time.
func (s *DB) RunIncrementalBackfill() {
	if err := s.BackfillAggregates(false); err != nil {
		log.Printf("backfill aggregates: %v", err)
	}
	if err := s.BackfillScores(false); err != nil {
		log.Printf("backfill scores: %v", err)
	}
}

// ScoreVersion identifies the readiness formula revision.
// Bump this constant whenever the scoring logic changes —
// rows with an older version will be ignored by the cache
// and recomputed on the next request or backfill run.
const ScoreVersion = 1

// readinessFromCache returns the most-recent `limit` readiness scores
// (ascending by date) that match the current ScoreVersion.
func (s *DB) readinessFromCache(limit int) ([]health.ReadinessPoint, error) {
	rows, err := s.db.Query(`
		SELECT date, readiness FROM daily_scores
		WHERE score_version = ?
		ORDER BY date DESC
		LIMIT ?`, ScoreVersion, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pts []health.ReadinessPoint
	for rows.Next() {
		var p health.ReadinessPoint
		if rows.Scan(&p.Date, &p.Score) == nil {
			pts = append(pts, p)
		}
	}
	// reverse to ascending order
	for i, j := 0, len(pts)-1; i < j; i, j = i+1, j-1 {
		pts[i], pts[j] = pts[j], pts[i]
	}
	return pts, nil
}

// saveReadinessScores upserts readiness scores without touching metric columns.
func (s *DB) saveReadinessScores(pts []health.ReadinessPoint) {
	now := time.Now().Format(time.RFC3339)
	for _, p := range pts {
		if _, err := s.db.Exec(`
			INSERT INTO daily_scores (date, readiness, score_version, computed_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(date) DO UPDATE SET
				readiness     = excluded.readiness,
				score_version = excluded.score_version,
				computed_at   = excluded.computed_at`,
			p.Date, p.Score, ScoreVersion, now,
		); err != nil {
			log.Printf("save readiness score %s: %v", p.Date, err)
		}
	}
}

// isCacheRecent returns true when the cache has at least one entry and the
// most-recent date is within the last two days (accounts for late phone syncs).
func isCacheRecent(pts []health.ReadinessPoint) bool {
	if len(pts) == 0 {
		return false
	}
	threshold := time.Now().AddDate(0, 0, -2).Format("2006-01-02")
	return pts[len(pts)-1].Date >= threshold
}

// InvalidateRecentScores NULLs out the readiness columns for the last `days`
// days so they are recomputed on the next GetReadinessHistory call.
// Metric columns (hrv_avg, steps, …) are preserved.
// Safe to call from a goroutine; errors are logged, not returned.
func (s *DB) InvalidateRecentScores(days int) {
	cutoff := time.Now().AddDate(0, 0, -days).Format("2006-01-02")
	if _, err := s.db.Exec(
		`UPDATE daily_scores SET readiness=NULL, score_version=NULL WHERE date >= ?`, cutoff,
	); err != nil {
		log.Printf("invalidate recent scores: %v", err)
	}
}

// BackfillScores (re)computes readiness scores for all dates that have health
// data.  If force=true the entire cache is cleared first; otherwise only rows
// with an outdated score_version are removed before computing.
func (s *DB) BackfillScores(force bool) error {
	if force {
		// Wipe only the readiness columns — metric columns are refilled by
		// BackfillAggregates and should not be lost here.
		if _, err := s.db.Exec(`UPDATE daily_scores SET readiness=NULL, score_version=NULL`); err != nil {
			return fmt.Errorf("clear readiness cache: %w", err)
		}
		log.Println("daily_scores readiness cleared (metric columns preserved)")
	} else {
		// NULL out stale-version rows so they get recomputed.
		if _, err := s.db.Exec(
			`UPDATE daily_scores SET readiness=NULL, score_version=NULL WHERE score_version != ?`,
			ScoreVersion,
		); err != nil {
			return fmt.Errorf("remove stale scores: %w", err)
		}
	}

	var earliest string
	if err := s.db.QueryRow(
		`SELECT MIN(substr(hour,1,10)) FROM hourly_metrics`,
	).Scan(&earliest); err != nil || earliest == "" {
		return fmt.Errorf("no metric data found")
	}

	t, err := time.Parse("2006-01-02", earliest)
	if err != nil {
		return fmt.Errorf("parse earliest date: %w", err)
	}
	days := int(time.Since(t).Hours()/24) + 2 // +2 so today is always included

	log.Printf("backfilling readiness scores for ~%d days (from %s)…", days, earliest)

	pts, err := s.computeReadinessHistory(days)
	if err != nil {
		return fmt.Errorf("compute: %w", err)
	}

	s.saveReadinessScores(pts)
	log.Printf("saved %d readiness scores (ScoreVersion=%d)", len(pts), ScoreVersion)
	return nil
}
