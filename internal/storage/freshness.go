package storage

import (
	"strconv"
	"strings"
	"time"
)

// MetricsLastSeen returns the most recent data timestamp per requested metric.
// Metrics with no data ever are absent from the returned map (callers should
// treat "missing key" identically to "very old").
//
// The query reads metric_points.date (TEXT timestamp) — the same field used
// everywhere else for time ordering. Source is ignored: we only care that
// *some* device wrote *some* point recently. The expression index
// idx_mp_name_day covers the (metric_name, day) lookup; MAX(date) within a
// metric is cheap.
func (s *DB) MetricsLastSeen(metrics ...string) map[string]time.Time {
	out := map[string]time.Time{}
	if len(metrics) == 0 {
		return out
	}

	ctx, cancel := queryCtx()
	defer cancel()

	// Build $1, $2, ... placeholders. We can't use ANY($1) cleanly with pgx
	// SimpleProtocol mode + array binding, so expand inline.
	placeholders := make([]string, len(metrics))
	args := make([]any, len(metrics))
	for i, m := range metrics {
		placeholders[i] = "$" + strconv.Itoa(i+1)
		args[i] = m
	}

	query := `
		SELECT metric_name, MAX(date)
		  FROM metric_points
		 WHERE metric_name IN (` + strings.Join(placeholders, ",") + `)
		 GROUP BY metric_name`

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return out
	}
	defer rows.Close()

	for rows.Next() {
		var name, dateStr string
		if err := rows.Scan(&name, &dateStr); err != nil {
			continue
		}
		if t, perr := parseMetricDate(dateStr); perr == nil {
			out[name] = t
		}
	}
	return out
}

