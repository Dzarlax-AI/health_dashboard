package storage

import (
	"context"
	"fmt"
)

// BulkInsertPoints creates a single lightweight health_records row (no payload
// body — the source of truth is the original export file) and bulk-inserts the
// parsed metric_points. Duplicates are silently ignored (ON CONFLICT DO NOTHING).
// Returns the number of rows actually inserted.
func (s *DB) BulkInsertPoints(description string, points []MetricPoint) (int, error) {
	if len(points) == 0 {
		return 0, nil
	}

	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var recordID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO health_records (automation_name, content_type, payload)
		 VALUES ($1, 'apple-health-import', $2)
		 RETURNING id`,
		description, fmt.Sprintf("%d points", len(points)),
	).Scan(&recordID)
	if err != nil {
		return 0, fmt.Errorf("insert health_record: %w", err)
	}

	inserted := 0
	for _, p := range points {
		ct, err := tx.Exec(ctx, `INSERT INTO metric_points
			(health_record_id, metric_name, units, date, qty, source)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT DO NOTHING`,
			recordID, p.MetricName, p.Units, p.Date, p.Qty, p.Source)
		if err != nil {
			return inserted, fmt.Errorf("insert %s/%s: %w", p.MetricName, p.Date, err)
		}
		if ct.RowsAffected() > 0 {
			inserted++
		}
	}

	return inserted, tx.Commit(ctx)
}
