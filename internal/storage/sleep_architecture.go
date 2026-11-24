package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	dbtmetrics "health-receiver/experiments/dbt_metrics"
)

const (
	SleepArchitectureReasonOK              = "ok"
	SleepArchitectureReasonMissingSegments = "architecture_segments_missing"
	SleepArchitectureReasonCoarseOnly      = "coarse_only_source"

	SleepArchitectureConfidenceHigh        = "high"
	SleepArchitectureConfidenceUnavailable = "unavailable"
)

// SleepArchitectureRawSegment is the minimal segment row needed to
// compute per-night sleep architecture. Date uses the metric_points
// "YYYY-MM-DD HH:MM:SS ±TZ" representation.
type SleepArchitectureRawSegment struct {
	Date       string
	MetricName string
	Source     string
	Hours      float64
}

// SleepArchitectureDay is one leakage-safe architecture payload for
// the sleep that ended on Date.
type SleepArchitectureDay struct {
	Date                 string
	Source               string
	AsleepHours          float64
	WASOHours            float64
	ExplicitWakeBouts    int
	GapInferredWakeBouts int
	LongestWakeBoutHours float64
	FragmentationIndex   float64
	Eligible             bool
	Reason               string
	Confidence           string
}

// SleepArchitectureWindow is a trailing-window aggregate for feature
// snapshots. MissingReasonCounts is always populated so downstream
// probes can distinguish "healthy zero" from unavailable data.
type SleepArchitectureWindow struct {
	Days                 int            `json:"architecture_window_days"`
	EligibleDays         int            `json:"architecture_eligible_days"`
	SourceDays           int            `json:"architecture_source_days"`
	MissingReasonCounts  map[string]int `json:"architecture_missing_reason_counts"`
	Confidence           string         `json:"architecture_confidence"`
	WASOHours            *float64       `json:"waso_hours,omitempty"`
	ExplicitWakeBouts    *float64       `json:"explicit_wake_bouts,omitempty"`
	GapInferredWakeBouts *float64       `json:"gap_inferred_wake_bouts,omitempty"`
	FragmentationIndex   *float64       `json:"fragmentation_index,omitempty"`
	LongestWakeBoutHours *float64       `json:"longest_wake_bout_hours,omitempty"`
}

type SleepArchitectureFeatureFields struct {
	ArchitectureAvailableThrough string         `json:"architecture_available_through"`
	WASOHours7d                  *float64       `json:"waso_hours_7d,omitempty"`
	ExplicitWakeBouts7d          *float64       `json:"explicit_wake_bouts_7d,omitempty"`
	GapInferredWakeBouts7d       *float64       `json:"gap_inferred_wake_bouts_7d,omitempty"`
	FragmentationIndex7d         *float64       `json:"fragmentation_index_7d,omitempty"`
	LongestWakeBoutHours7d       *float64       `json:"longest_wake_bout_hours_7d,omitempty"`
	ArchitectureEligibleDays7d   int            `json:"architecture_eligible_days_7d"`
	ArchitectureSourceDays7d     int            `json:"architecture_source_days_7d"`
	MissingReasonCounts7d        map[string]int `json:"architecture_missing_reason_counts_7d"`
	ArchitectureConfidence7d     string         `json:"architecture_confidence_7d"`
	WASOHours14d                 *float64       `json:"waso_hours_14d,omitempty"`
	ExplicitWakeBouts14d         *float64       `json:"explicit_wake_bouts_14d,omitempty"`
	GapInferredWakeBouts14d      *float64       `json:"gap_inferred_wake_bouts_14d,omitempty"`
	FragmentationIndex14d        *float64       `json:"fragmentation_index_14d,omitempty"`
	LongestWakeBoutHours14d      *float64       `json:"longest_wake_bout_hours_14d,omitempty"`
	ArchitectureEligibleDays14d  int            `json:"architecture_eligible_days_14d"`
	ArchitectureSourceDays14d    int            `json:"architecture_source_days_14d"`
	MissingReasonCounts14d       map[string]int `json:"architecture_missing_reason_counts_14d"`
	ArchitectureConfidence14d    string         `json:"architecture_confidence_14d"`
}

type architectureSegment struct {
	Metric string
	Start  time.Time
	End    time.Time
	Source string
}

// LoadSleepArchitectureDays reads segment-level sleep rows and returns
// architecture rows keyed by the date the segment ended on. The SQL
// reads one extra start-date before `from` so cross-midnight sleeps
// ending on `from` are available.
func (s *DB) LoadSleepArchitectureDays(from, to string) (map[string]SleepArchitectureDay, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	days, err := s.sleepArchitectureRows(ctx, from, to, "select * from architecture")
	out := map[string]SleepArchitectureDay{}
	if err != nil {
		return nil, err
	}
	defer days.Close()
	for days.Next() {
		var day SleepArchitectureDay
		if err := days.Scan(&day.Date, &day.Source, &day.AsleepHours, &day.WASOHours, &day.ExplicitWakeBouts, &day.GapInferredWakeBouts, &day.LongestWakeBoutHours, &day.FragmentationIndex, &day.Eligible, &day.Reason, &day.Confidence); err != nil {
			return nil, err
		}
		out[day.Date] = day
	}
	if err := days.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *DB) sleepArchitectureRows(ctx context.Context, from, to, projection string, windowAnchors ...string) (pgx.Rows, error) {
	fromT, err := time.Parse(isoDate, from)
	if err != nil {
		return nil, fmt.Errorf("LoadSleepArchitectureDays: parse from: %w", err)
	}
	toT, err := time.Parse(isoDate, to)
	if err != nil {
		return nil, fmt.Errorf("LoadSleepArchitectureDays: parse to: %w", err)
	}
	if toT.Before(fromT) {
		return nil, fmt.Errorf("LoadSleepArchitectureDays: to %q before from %q", to, from)
	}

	queryFrom := fromT.AddDate(0, 0, -1).Format(isoDate)
	preferred, err := readPreferredSleepSource(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT date, metric_name, source, qty
		  FROM metric_points
		 WHERE metric_name IN ('sleep_deep','sleep_rem','sleep_core','sleep_unspecified','sleep_awake')
		   AND qty > 0
		   AND quality = 'ok'
		   AND SUBSTRING(date, 12, 8) != '00:00:00'
		   AND SUBSTRING(date, 1, 10) BETWEEN $1 AND $2
		 ORDER BY date ASC, metric_name COLLATE "C", qty
	`, queryFrom, to)
	if err != nil {
		return nil, fmt.Errorf("LoadSleepArchitectureDays: %w", err)
	}
	defer rows.Close()

	// Preserve the recorded fixed offset and nanosecond duration of every
	// interval. The first observation for an end date defines its local midnight.
	type clockSegment struct {
		Day    string  `json:"day"`
		Source string  `json:"source"`
		Metric string  `json:"metric"`
		Start  int64   `json:"start_ns"`
		End    int64   `json:"end_ns"`
		Hours  float64 `json:"hours"`
	}
	origins := map[string]time.Time{}
	segments := []clockSegment{}
	for rows.Next() {
		var r SleepArchitectureRawSegment
		if err := rows.Scan(&r.Date, &r.MetricName, &r.Source, &r.Hours); err != nil {
			return nil, err
		}
		start, err := parseSleepDate(r.Date)
		if err != nil || r.Source == "" {
			continue
		}
		end := start.Add(time.Duration(r.Hours * float64(time.Hour)))
		date := end.Format(isoDate)
		if date < from || date > to {
			continue
		}
		origin, ok := origins[date]
		if !ok {
			origin = time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, end.Location())
			origins[date] = origin
		}
		segments = append(segments, clockSegment{date, r.Source, r.MetricName, int64(start.Sub(origin)), int64(end.Sub(origin)), end.Sub(start).Hours()})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	payload, err := json.Marshal(segments)
	if err != nil {
		return nil, err
	}
	var anchorFrom, anchorTo *string
	var args []any
	if len(windowAnchors) == 2 {
		anchorFrom, anchorTo = &windowAnchors[0], &windowAnchors[1]
		args = []any{*anchorFrom, *anchorTo}
	}
	_, err = withDecisionJournal(ctx, s, func(db *DB, enabled bool) (bool, error) {
		if !enabled {
			return false, nil
		}
		_, err := db.pool.Exec(ctx, architectureReceiptSQL, json.RawMessage(payload), preferred, from, to, anchorFrom, anchorTo)
		return true, err
	})
	if err != nil {
		return nil, err
	}
	query := `with segments as (
		select r.*,p.ordinal,$2::text as preferred
		from jsonb_array_elements($1::jsonb) with ordinality p(item,ordinal)
		cross join lateral jsonb_to_record(p.item) r(day text,source text,metric text,start_ns bigint,end_ns bigint,hours float8)
	), architecture as (` + dbtmetrics.SleepArchitectureDaysSQL + `) ` + projection
	return s.pool.Query(ctx, query, append([]any{json.RawMessage(payload), preferred}, args...)...)
}

func (s *DB) LoadSleepArchitectureFeatures(from, to string) (map[string]SleepArchitectureFeatureFields, error) {
	first, err := time.Parse(isoDate, from)
	if err != nil {
		return nil, err
	}
	last, err := time.Parse(isoDate, to)
	if err != nil {
		return nil, err
	}
	if last.Before(first) {
		return nil, fmt.Errorf("LoadSleepArchitectureFeatures: to %q before from %q", to, from)
	}
	// Whole-history backfills normalize segment clocks in bounded batches.
	// Each batch reads its own 13 preceding days for the first window.
	const batchDays = 90
	out := map[string]SleepArchitectureFeatureFields{}
	for start := first; !start.After(last); start = start.AddDate(0, 0, batchDays) {
		end := start.AddDate(0, 0, batchDays-1)
		if end.After(last) {
			end = last
		}
		if err := s.readSleepArchitectureFeatures(start, end, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *DB) readSleepArchitectureFeatures(first, last time.Time, out map[string]SleepArchitectureFeatureFields) error {
	from, to := first.Format(isoDate), last.Format(isoDate)
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.sleepArchitectureRows(ctx, first.AddDate(0, 0, -13).Format(isoDate), to, `,
		anchors as (select $3::date+n as anchor from generate_series(0,$4::date-$3::date) n)
		select * from (`+dbtmetrics.SleepArchitectureWindowsSQL+`) windows order by anchor,days`, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	var seven SleepArchitectureWindow
	for rows.Next() {
		var date string
		var w SleepArchitectureWindow
		if err := rows.Scan(&date, &w.Days, &w.EligibleDays, &w.SourceDays, &w.MissingReasonCounts, &w.Confidence, &w.WASOHours, &w.ExplicitWakeBouts, &w.GapInferredWakeBouts, &w.FragmentationIndex, &w.LongestWakeBoutHours); err != nil {
			return err
		}
		if w.Days == 7 {
			seven = w
		} else {
			out[date] = architectureFields(date, seven, w)
		}
	}
	return rows.Err()
}

func architectureFields(date string, w7, w14 SleepArchitectureWindow) SleepArchitectureFeatureFields {
	return SleepArchitectureFeatureFields{
		ArchitectureAvailableThrough: date,
		WASOHours7d:                  w7.WASOHours,
		ExplicitWakeBouts7d:          w7.ExplicitWakeBouts,
		GapInferredWakeBouts7d:       w7.GapInferredWakeBouts,
		FragmentationIndex7d:         w7.FragmentationIndex,
		LongestWakeBoutHours7d:       w7.LongestWakeBoutHours,
		ArchitectureEligibleDays7d:   w7.EligibleDays,
		ArchitectureSourceDays7d:     w7.SourceDays,
		MissingReasonCounts7d:        w7.MissingReasonCounts,
		ArchitectureConfidence7d:     w7.Confidence,
		WASOHours14d:                 w14.WASOHours,
		ExplicitWakeBouts14d:         w14.ExplicitWakeBouts,
		GapInferredWakeBouts14d:      w14.GapInferredWakeBouts,
		FragmentationIndex14d:        w14.FragmentationIndex,
		LongestWakeBoutHours14d:      w14.LongestWakeBoutHours,
		ArchitectureEligibleDays14d:  w14.EligibleDays,
		ArchitectureSourceDays14d:    w14.SourceDays,
		MissingReasonCounts14d:       w14.MissingReasonCounts,
		ArchitectureConfidence14d:    w14.Confidence,
	}
}
