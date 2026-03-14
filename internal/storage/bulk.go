package storage

import "fmt"

// BulkInsertPoints creates a single lightweight health_records row (no payload
// body — the source of truth is the original export file) and bulk-inserts the
// parsed metric_points. Duplicates are silently ignored (INSERT OR IGNORE).
// Returns the number of rows actually inserted.
func (s *DB) BulkInsertPoints(description string, points []MetricPoint) (int, error) {
	if len(points) == 0 {
		return 0, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO health_records (automation_name, content_type, payload)
		 VALUES (?, 'apple-health-import', ?)`,
		description, fmt.Sprintf("%d points", len(points)),
	)
	if err != nil {
		return 0, fmt.Errorf("insert health_record: %w", err)
	}
	recordID, _ := res.LastInsertId()

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO metric_points
		(health_record_id, metric_name, units, date, qty, source)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	inserted := 0
	for _, p := range points {
		r, err := stmt.Exec(recordID, p.MetricName, p.Units, p.Date, p.Qty, p.Source)
		if err != nil {
			return inserted, fmt.Errorf("insert %s/%s: %w", p.MetricName, p.Date, err)
		}
		if n, _ := r.RowsAffected(); n > 0 {
			inserted++
		}
	}

	return inserted, tx.Commit()
}
