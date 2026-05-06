package storage

import (
	"fmt"

	"health-receiver/internal/health"
)

// QualityAuditEntry is one row of the impossible-values audit.
type QualityAuditEntry struct {
	Metric    string  `json:"metric"`
	Min       float64 `json:"min"`
	Max       float64 `json:"max"`
	BadCount  int     `json:"bad_count"`
	TotalRows int     `json:"total_rows"`
	Sample    float64 `json:"sample"` // an example bad value, for sanity-checking
}

// AuditImpossibleValues counts points already in metric_points whose qty
// values fall outside the configured physiological ranges (see
// internal/health/quality.go). Returns one entry per metric that *has* bad
// values; metrics that are clean are omitted to keep the response small.
//
// This is a one-shot diagnostic — not invoked automatically. Surfaced through
// /api/admin/quality-audit so we can decide whether a backfill cleanup is
// worth doing before flipping on stricter validation.
func (s *DB) AuditImpossibleValues() ([]QualityAuditEntry, error) {
	ctx, cancel := queryCtx()
	defer cancel()

	// Build the audit as one CTE-per-metric union — one round-trip beats N+1
	// queries, and the index on (metric_name) plus the BETWEEN bound makes
	// each branch a fast partial-index scan.
	type query struct {
		metric   string
		min, max float64
	}
	var qs []query
	for _, name := range auditedMetrics {
		min, max, ok := health.QualityRange(name)
		if !ok {
			continue
		}
		qs = append(qs, query{metric: name, min: min, max: max})
	}

	out := make([]QualityAuditEntry, 0, len(qs))
	for _, q := range qs {
		// Two simple count queries per metric. Keeps SQL readable; per-metric
		// runtime is already index-bounded.
		row := s.pool.QueryRow(ctx, `
			SELECT
				COUNT(*) FILTER (WHERE qty < $2 OR qty > $3),
				COUNT(*),
				COALESCE(MIN(qty) FILTER (WHERE qty < $2 OR qty > $3), 0)
			  FROM metric_points
			 WHERE metric_name = $1`,
			q.metric, q.min, q.max)
		var bad, total int
		var sample float64
		if err := row.Scan(&bad, &total, &sample); err != nil {
			continue
		}
		if bad == 0 {
			continue
		}
		out = append(out, QualityAuditEntry{
			Metric:    q.metric,
			Min:       q.min,
			Max:       q.max,
			BadCount:  bad,
			TotalRows: total,
			Sample:    sample,
		})
	}
	return out, nil
}

// QualityFixResult summarises the outcome of MarkExistingImpossible +
// MarkSuspectPoints. Used by /api/admin/quality-fix and the weekly digest.
type QualityFixResult struct {
	ImpossibleFlagged int            `json:"impossible_flagged"`
	SuspectFlagged    int            `json:"suspect_flagged"`
	PerMetric         map[string]int `json:"per_metric"` // metric → suspect count for the run
}

// MarkExistingImpossible scans all metric_points and sets quality='impossible'
// for rows whose qty falls outside the configured physiological range. Idempotent:
// re-running only updates rows that aren't already flagged. Returns the number
// of rows newly flagged.
//
// Use case: one-shot cleanup of legacy data ingested before the IsImpossible
// gate was added at the handler. After this runs, baselines that filter
// quality='ok' will exclude these points.
func (s *DB) MarkExistingImpossible() (int, error) {
	ctx, cancel := longCtx()
	defer cancel()

	total := 0
	for _, name := range auditedMetrics {
		min, max, ok := health.QualityRange(name)
		if !ok {
			continue
		}
		tag, err := s.pool.Exec(ctx, `
			UPDATE metric_points
			   SET quality = 'impossible'
			 WHERE metric_name = $1
			   AND quality = 'ok'
			   AND (qty < $2 OR qty > $3)`, name, min, max)
		if err != nil {
			return total, fmt.Errorf("mark impossible %s: %w", name, err)
		}
		total += int(tag.RowsAffected())
	}
	return total, nil
}

// MarkSuspectPoints sweeps the last `days` calendar days of metric_points and
// flags values that lie more than `sigma` standard deviations from the trailing
// 30-day per-metric baseline. The baseline excludes already-flagged rows
// (quality != 'ok') so a previously-suspect day doesn't re-poison its own check.
//
// This is run on demand from /admin and (eventually) on a daily timer. Cost
// per run: one query per audited metric, each bounded by index range scan on
// (metric_name, date). Per-metric cost is small even on the 3.7M-row table.
//
// `sigma` defaults to 3 if non-positive. `days` defaults to 7.
func (s *DB) MarkSuspectPoints(days int, sigma float64) (map[string]int, error) {
	if days <= 0 {
		days = 7
	}
	if sigma <= 0 {
		sigma = 3
	}
	ctx, cancel := longCtx()
	defer cancel()

	out := map[string]int{}
	for _, name := range auditedMetrics {
		// Sleep_* and step_count are noisy by nature — z-score thresholds
		// flag legitimate variation. Skip until we have a per-metric tuned
		// sigma table; for now focus on the autonomic metrics where 3σ is
		// a well-established threshold (Plews 2014, Altini 2021).
		if !zScoreEligible[name] {
			continue
		}
		// Compute mean+sd over a 30-day baseline window (excluding flagged
		// rows), then UPDATE recent rows whose deviation exceeds sigma. CTE +
		// UPDATE-FROM keeps it one round-trip per metric.
		tag, err := s.pool.Exec(ctx, `
			WITH baseline AS (
				SELECT AVG(qty)    AS mean,
				       STDDEV(qty) AS sd
				  FROM metric_points
				 WHERE metric_name = $1
				   AND quality    = 'ok'
				   AND SUBSTRING(date,1,10) >= TO_CHAR(NOW() - INTERVAL '30 days', 'YYYY-MM-DD')
			)
			UPDATE metric_points mp
			   SET quality = 'suspect'
			  FROM baseline b
			 WHERE mp.metric_name = $1
			   AND mp.quality     = 'ok'
			   AND b.sd IS NOT NULL
			   AND b.sd > 0
			   AND SUBSTRING(mp.date,1,10) >= TO_CHAR(NOW() - INTERVAL '1 day' * $2, 'YYYY-MM-DD')
			   AND ABS(mp.qty - b.mean) / b.sd > $3`,
			name, days, sigma)
		if err != nil {
			return out, fmt.Errorf("mark suspect %s: %w", name, err)
		}
		if n := int(tag.RowsAffected()); n > 0 {
			out[name] = n
		}
	}
	return out, nil
}

// zScoreEligible lists metrics where a 3σ z-score sweep is meaningful. These
// are autonomic and physiological signals with a stable personal distribution.
// Behavioural metrics (steps, calories) have legitimately bimodal distributions
// (rest day vs active day) that confuse z-score — they belong in a different
// quality check.
var zScoreEligible = map[string]bool{
	"heart_rate_variability": true,
	"resting_heart_rate":     true,
	"oxygen_saturation":      true,
	"respiratory_rate":       true,
	"wrist_temperature":      true,
	"vo2_max":                true,
	"body_mass":              true,
}

// auditedMetrics is the list of metrics we run the audit over. Kept here
// (rather than iterating the qualityRanges map directly) so we have a stable
// audit order and so we can tweak which metrics are user-visible without
// changing the validation map.
var auditedMetrics = []string{
	"heart_rate_variability",
	"heart_rate",
	"resting_heart_rate",
	"walking_heart_rate",
	"oxygen_saturation",
	"respiratory_rate",
	"body_mass",
	"body_fat_percentage",
	"vo2_max",
	"step_count",
	"active_energy",
	"basal_energy_burned",
	"apple_exercise_time",
	"apple_stand_time",
	"flights_climbed",
	"sleep_total",
	"sleep_deep",
	"sleep_rem",
	"sleep_core",
	"sleep_awake",
	"wrist_temperature",
}

