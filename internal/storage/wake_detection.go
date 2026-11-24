package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	dbtmetrics "health-receiver/experiments/dbt_metrics"

	"github.com/jackc/pgx/v5"
)

const (
	WakeFormulaVersion = "wake-v2"

	WakeConfidenceLow    = "low"
	WakeConfidenceMedium = "medium"
	WakeConfidenceHigh   = "high"

	wakeIngestQuiet        = 20 * time.Minute
	wakeActivityAge        = 30 * time.Minute
	wakePassiveAge         = 60 * time.Minute
	wakeEarlyAge           = 90 * time.Minute
	wakeFallbackAge        = 90 * time.Minute
	wakeEarlyTolerance     = 120 * time.Minute
	wakeTypicalGuardOffset = 30 * time.Minute
	wakeActivitySteps      = 100.0
)

type MorningWakeStatus struct {
	Ready                 bool      `json:"ready"`
	Confidence            string    `json:"confidence"`
	Reason                string    `json:"reason"`
	CandidateWake         time.Time `json:"candidate_wake,omitempty"`
	LatestIngest          time.Time `json:"latest_ingest,omitempty"`
	PostWakeSteps         float64   `json:"post_wake_steps"`
	PreferredSource       string    `json:"preferred_source,omitempty"`
	SourceSelectionReason string    `json:"source_selection_reason,omitempty"`
	InputSource           string    `json:"input_source,omitempty"`
	InputsHash            string    `json:"inputs_hash,omitempty"`
	TypicalWakeMin        int       `json:"typical_wake_min,omitempty"`
	TypicalWakeOK         bool      `json:"typical_wake_ok"`
	Signal                string    `json:"signal,omitempty"`
}

type wakeInputSegment struct {
	Metric     string
	Start      time.Time
	Hours      float64
	Source     string
	Quality    string
	ReceivedAt time.Time
}

type wakeCandidate struct {
	Wake                  time.Time
	LatestIngest          time.Time
	IngestQuietSince      time.Time
	Source                string
	SourceSelectionReason string
	PreferredSource       string
	InputsHash            string
	Signal                string
}

type WakeBackfillResult struct {
	Attempted int `json:"attempted"`
	Detected  int `json:"detected"`
	Written   int `json:"written"`
	Missing   int `json:"missing"`
}

// WakeCandidateForDate returns the source-derived candidate without persisting
// it or applying wall-clock readiness policy. It powers historical probes and
// backfills without fabricating a historical confirmation time.
func (s *DB) WakeCandidateForDate(localDate string, loc *time.Location) (MorningWakeStatus, error) {
	if loc == nil {
		loc = time.UTC
	}
	candidate, ok, err := s.loadWakeCandidate(localDate, loc)
	if err != nil {
		return MorningWakeStatus{Confidence: WakeConfidenceLow, Reason: "query_error"}, err
	}
	if !ok {
		return MorningWakeStatus{Confidence: WakeConfidenceLow, Reason: "no_data"}, nil
	}
	return MorningWakeStatus{
		Confidence:            WakeConfidenceLow,
		Reason:                "candidate_only",
		CandidateWake:         candidate.Wake,
		LatestIngest:          candidate.LatestIngest,
		InputSource:           candidate.Source,
		SourceSelectionReason: candidate.SourceSelectionReason,
		PreferredSource:       candidate.PreferredSource,
		InputsHash:            candidate.InputsHash,
		Signal:                candidate.Signal,
	}, nil
}

// ComputeMorningWakeStatus calculates and persists the canonical wake_time
// derived metric for localDate. It is safe to call on every scheduler tick and
// ingest callback: identical source inputs overwrite the same row.
func (s *DB) ComputeMorningWakeStatus(localDate string, loc *time.Location, now time.Time) (MorningWakeStatus, error) {
	if loc == nil {
		loc = time.UTC
	}
	ctx, cancel := queryCtx()
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MorningWakeStatus{Confidence: WakeConfidenceLow, Reason: "query_error"}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Serialize computations per tenant/date. The schema name scopes the
	// transaction lock without adding a table or sharing state across tenants.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext(current_schema()), hashtext($1))`, "wake_time:"+localDate); err != nil {
		return MorningWakeStatus{Confidence: WakeConfidenceLow, Reason: "query_error"}, err
	}
	// Re-read after taking the lock so a slower worker cannot persist a
	// candidate captured before a newer computation for this date.
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	segments, preferred, err := unit.wakeCandidateInputs(ctx, localDate, loc)
	if err != nil {
		return MorningWakeStatus{Confidence: WakeConfidenceLow, Reason: "query_error"}, err
	}
	candidate, ok, err := queryWakeCandidateSQL(ctx, unit.pool, localDate, loc, preferred, segments)
	if err != nil {
		return MorningWakeStatus{Confidence: WakeConfidenceLow, Reason: "query_error"}, err
	}
	if ok {
		candidate.InputsHash = wakeCandidateInputHash(segments, localDate, loc, candidate)
	}
	if !ok {
		if _, err := tx.Exec(ctx, `
			UPDATE derived_metrics
			   SET metadata = metadata || jsonb_build_object(
				   'sleep_inputs_hash', '',
				   'sleep_inputs_changed_at', GREATEST(
					   COALESCE((metadata->>'sleep_inputs_changed_at')::timestamptz, $3), $3
				   )
			   )
			 WHERE metric_name=$1 AND metric_date=$2
		`, DerivedMetricWakeTime, localDate, now); err != nil {
			return MorningWakeStatus{Confidence: WakeConfidenceLow, Reason: "query_error"}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return MorningWakeStatus{Confidence: WakeConfidenceLow, Reason: "query_error"}, err
		}
		return MorningWakeStatus{Confidence: WakeConfidenceLow, Reason: "no_data"}, nil
	}
	previousHash, previousChangedAt, err := loadWakeInputClock(ctx, tx, localDate)
	if err != nil {
		return MorningWakeStatus{Confidence: WakeConfidenceLow, Reason: "query_error"}, err
	}
	candidate.IngestQuietSince = wakeInputChangedAt(candidate.InputsHash, previousHash, previousChangedAt, now)
	steps, err := postWakeSteps(ctx, tx, localDate, candidate.Wake, now)
	if err != nil {
		return MorningWakeStatus{Confidence: WakeConfidenceLow, Reason: "steps_query_error"}, err
	}
	typicalMin, typicalOK, err := typicalDerivedWakeMinutes(ctx, tx, localDate, 14, loc)
	if err != nil {
		return MorningWakeStatus{Confidence: WakeConfidenceLow, Reason: "typical_query_error"}, err
	}
	status, err := queryWakeConfirmationSQL(ctx, unit.pool, candidate, now, steps, typicalMin, typicalOK, loc)
	if err != nil {
		return MorningWakeStatus{Confidence: WakeConfidenceLow, Reason: "query_error"}, err
	}
	unit.captureWakeDecision(ctx, localDate, loc, now, segments, status, candidate.IngestQuietSince)
	if err := saveWakeStatusTx(ctx, tx, localDate, status, now, candidate.IngestQuietSince); err != nil {
		return status, err
	}
	if err := tx.Commit(ctx); err != nil {
		return status, err
	}
	return status, nil
}

// BackfillWakeTimes rebuilds canonical historical candidates without
// pretending that they were confirmed in real time. Dry-run executes the same
// detector but performs no writes.
func (s *DB) BackfillWakeTimes(from, to string, loc *time.Location, dryRun bool) (WakeBackfillResult, error) {
	if loc == nil {
		loc = time.UTC
	}
	fromDate, err := time.ParseInLocation("2006-01-02", from, loc)
	if err != nil {
		return WakeBackfillResult{}, fmt.Errorf("parse backfill from: %w", err)
	}
	toDate, err := time.ParseInLocation("2006-01-02", to, loc)
	if err != nil {
		return WakeBackfillResult{}, fmt.Errorf("parse backfill to: %w", err)
	}
	if fromDate.After(toDate) {
		return WakeBackfillResult{}, errors.New("wake backfill from must not be after to")
	}
	var result WakeBackfillResult
	for date := fromDate; !date.After(toDate); date = date.AddDate(0, 0, 1) {
		result.Attempted++
		localDate := date.Format("2006-01-02")
		candidate, ok, err := s.loadWakeCandidate(localDate, loc)
		if err != nil {
			return result, fmt.Errorf("detect wake %s: %w", localDate, err)
		}
		if !ok {
			result.Missing++
			continue
		}
		result.Detected++
		if dryRun {
			continue
		}
		status := MorningWakeStatus{
			Confidence:            WakeConfidenceLow,
			Reason:                "historical_backfill",
			CandidateWake:         candidate.Wake,
			LatestIngest:          candidate.LatestIngest,
			InputSource:           candidate.Source,
			SourceSelectionReason: candidate.SourceSelectionReason,
			PreferredSource:       candidate.PreferredSource,
			InputsHash:            candidate.InputsHash,
			Signal:                candidate.Signal,
		}
		if err := s.saveWakeStatus(localDate, status, time.Now()); err != nil {
			return result, fmt.Errorf("save wake %s: %w", localDate, err)
		}
		result.Written++
	}
	return result, nil
}

func (s *DB) loadWakeCandidate(localDate string, loc *time.Location) (wakeCandidate, bool, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	return s.loadWakeCandidateContext(ctx, localDate, loc)
}

type wakeQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func (s *DB) loadWakeCandidateContext(ctx context.Context, localDate string, loc *time.Location) (wakeCandidate, bool, error) {
	if loc == nil {
		loc = time.UTC
	}
	segments, preferred, err := s.wakeCandidateInputs(ctx, localDate, loc)
	if err != nil {
		return wakeCandidate{}, false, err
	}
	candidate, ok, err := queryWakeCandidateSQL(ctx, s.pool, localDate, loc, preferred, segments)
	if err != nil || !ok {
		return candidate, ok, err
	}
	// The dbt macro owns candidate selection. Preserve the existing quiet
	// clock contract from its selected source/signal/wake and the surrounding
	// episode inputs without running the Go selector a second time.
	candidate.InputsHash = wakeCandidateInputHash(segments, localDate, loc, candidate)
	candidate.PreferredSource = preferred
	return candidate, true, nil
}

func queryWakeCandidateSQL(ctx context.Context, queryer wakeQueryer, localDate string, loc *time.Location, preferred string, inputs []wakeInputSegment) (wakeCandidate, bool, error) {
	cases := `select current_user::text as profile,$1::text as capture,$2::date as target_day,$3::text as zone,$4::text as preferred_source`
	segments := `select current_user::text as profile,$1::text as capture,s.metric,s.start_at,s.hours,s.source,s.quality,s.received_at,s.ends_at from jsonb_to_recordset($5::jsonb) as s(metric text,start_at timestamptz,hours float8,source text,quality text,received_at timestamptz,ends_at timestamptz)`
	type candidateSQLSegment struct {
		Metric     string    `json:"metric"`
		Start      time.Time `json:"start_at"`
		End        time.Time `json:"ends_at"`
		Hours      float64   `json:"hours"`
		Source     string    `json:"source"`
		Quality    string    `json:"quality"`
		ReceivedAt time.Time `json:"received_at"`
	}
	rowsInput := make([]candidateSQLSegment, len(inputs))
	for i, segment := range inputs {
		rowsInput[i] = candidateSQLSegment{segment.Metric, segment.Start, segment.Start.Add(time.Duration(segment.Hours * float64(time.Hour))).Truncate(time.Microsecond), segment.Hours, segment.Source, segment.Quality, segment.ReceivedAt}
	}
	encoded, err := json.Marshal(rowsInput)
	if err != nil {
		return wakeCandidate{}, false, err
	}
	query := dbtmetrics.WakeCandidateResultSQL(cases, segments)
	rows, err := queryer.Query(ctx, query, localDate, localDate, loc.String(), preferred, json.RawMessage(encoded))
	if err != nil {
		return wakeCandidate{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return wakeCandidate{}, false, err
		}
		return wakeCandidate{}, false, nil
	}
	var state string
	var source, signal, reason *string
	var wake, latest *time.Time
	if err := rows.Scan(nil, nil, nil, &state, &source, &signal, &wake, &latest, &reason); err != nil {
		return wakeCandidate{}, false, err
	}
	if state != "available" || wake == nil {
		return wakeCandidate{}, false, nil
	}
	candidate := wakeCandidate{Wake: *wake, Source: derefWakeString(source), Signal: derefWakeString(signal), SourceSelectionReason: derefWakeString(reason), PreferredSource: preferred}
	if latest != nil {
		candidate.LatestIngest = *latest
	}
	return candidate, true, rows.Err()
}

func derefWakeString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func queryWakeConfirmationSQL(ctx context.Context, queryer wakeQueryer, candidate wakeCandidate, now time.Time, steps float64, typicalMin int, typicalOK bool, loc *time.Location) (MorningWakeStatus, error) {
	var quietSince any
	if !candidate.IngestQuietSince.IsZero() {
		quietSince = candidate.IngestQuietSince
	}
	var typical any
	if typicalOK {
		typical = typicalMin
	}
	observations := `select current_user::text as profile,$1::text as capture,$2::timestamptz as candidate_wake,$3::text as signal,$4::text as source,$5::timestamptz as latest_ingest,$6::timestamptz as decision_at,$7::timestamptz as quiet_since,$8::text as zone,$9::int as typical_minute,$10::float8 as steps,$11::text as inputs_hash,'{}'::jsonb as observed`
	query := dbtmetrics.WakeConfirmationResultSQL(observations)
	rows, err := queryer.Query(ctx, query, candidate.InputsHash, nullableWakeTime(candidate.Wake), candidate.Signal, candidate.Source, nullableWakeTime(candidate.LatestIngest), now, quietSince, loc.String(), typical, steps, candidate.InputsHash)
	if err != nil {
		return MorningWakeStatus{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return MorningWakeStatus{}, err
		}
		return MorningWakeStatus{}, errors.New("wake confirmation SQL returned no row")
	}
	var status MorningWakeStatus
	var profile, capture, reason, source, signal, hash string
	var wake *time.Time
	var ready bool
	if err := rows.Scan(&profile, &capture, &wake, &reason, &source, &signal, new(time.Time), &hash, new([]byte), &ready, &status.Confidence); err != nil {
		return MorningWakeStatus{}, err
	}
	status.Ready = ready
	status.Reason = reason
	if wake != nil {
		status.CandidateWake = *wake
	}
	status.LatestIngest = candidate.LatestIngest
	status.PostWakeSteps = steps
	status.InputSource = source
	status.SourceSelectionReason = candidate.SourceSelectionReason
	status.PreferredSource = candidate.PreferredSource
	status.Signal = signal
	status.InputsHash = candidate.InputsHash
	status.TypicalWakeMin = typicalMin
	status.TypicalWakeOK = typicalOK
	return status, rows.Err()
}

func nullableWakeTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func (s *DB) wakeCandidateInputs(ctx context.Context, date string, loc *time.Location) ([]wakeInputSegment, string, error) {
	segments, err := loadWakeSegments(ctx, s.pool, date, loc)
	if err != nil {
		return nil, "", err
	}
	preferred, err := readPreferredSleepSource(ctx, s.pool)
	return segments, preferred, err
}

func loadWakeSegments(ctx context.Context, queryer wakeQueryer, localDate string, loc *time.Location) ([]wakeInputSegment, error) {
	targetDate, err := time.ParseInLocation("2006-01-02", localDate, loc)
	if err != nil {
		return nil, fmt.Errorf("parse wake date: %w", err)
	}
	fromDate := targetDate.AddDate(0, 0, -1).Format("2006-01-02")
	rows, err := queryer.Query(ctx, `
		SELECT metric_name, date, qty, source, quality, received_at
		  FROM metric_points
		 WHERE metric_name IN ('sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_unspecified','sleep_awake')
		   AND quality='ok'
		   AND qty > 0
		   AND SUBSTRING(date,1,10) BETWEEN $1 AND $2
		 ORDER BY qty, date, metric_name COLLATE "C"
	`, fromDate, localDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var segments []wakeInputSegment
	for rows.Next() {
		var dateStr, source, quality string
		var hours float64
		var receivedAt time.Time
		var metric string
		if err := rows.Scan(&metric, &dateStr, &hours, &source, &quality, &receivedAt); err != nil {
			return nil, err
		}
		start, err := parseMetricDate(dateStr)
		if err != nil {
			continue
		}
		segments = append(segments, wakeInputSegment{
			Metric:     metric,
			Start:      start,
			Hours:      hours,
			Source:     source,
			Quality:    quality,
			ReceivedAt: receivedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return segments, nil
}

func wakeCandidateInputHash(segments []wakeInputSegment, localDate string, loc *time.Location, candidate wakeCandidate) string {
	if loc == nil {
		loc = time.UTC
	}
	isMidnight := func(start time.Time) bool { return start.In(loc).Format("15:04:05") == "00:00:00" }
	isWakeWindow := func(end time.Time) bool {
		localEnd := end.In(loc)
		if localEnd.Format("2006-01-02") != localDate {
			return false
		}
		minute := localEnd.Hour()*60 + localEnd.Minute()
		return minute >= 3*60 && minute <= 15*60
	}
	var selected []wakeInputSegment
	if candidate.Signal == "detailed_stage_end" || candidate.Signal == "raw_sleep_total_end" {
		for _, segment := range segments {
			if segment.Source != candidate.Source || segment.Hours <= 0 || segment.Quality != "ok" || isMidnight(segment.Start) {
				continue
			}
			if candidate.Signal == "detailed_stage_end" && segment.Metric == "sleep_total" || candidate.Signal == "raw_sleep_total_end" && segment.Metric != "sleep_total" {
				continue
			}
			if isWakeWindow(segment.Start.Add(time.Duration(segment.Hours * float64(time.Hour)))) {
				selected = append(selected, segment)
			}
		}
	} else if candidate.Signal == "midnight_summary" {
		type group struct {
			start         time.Time
			asleep, awake float64
			components    []wakeInputSegment
		}
		groups := make(map[string]*group)
		for _, segment := range segments {
			if segment.Source != candidate.Source || segment.Hours <= 0 || segment.Quality != "ok" ||
				(segment.Metric != "sleep_total" && segment.Metric != "sleep_awake") ||
				segment.Start.In(loc).Format("2006-01-02 15:04:05") != localDate+" 00:00:00" {
				continue
			}
			key := segment.Start.UTC().Format(time.RFC3339Nano)
			g := groups[key]
			if g == nil {
				g = &group{start: segment.Start}
				groups[key] = g
			}
			if segment.Metric == "sleep_total" {
				g.asleep += segment.Hours
			} else {
				g.awake += segment.Hours
			}
			g.components = append(g.components, segment)
		}
		var chosen *group
		var chosenEnd time.Time
		for _, g := range groups {
			end := g.start.Add(time.Duration((g.asleep + g.awake) * float64(time.Hour)))
			if g.asleep > 0 && isWakeWindow(end) && (chosen == nil || end.After(chosenEnd)) {
				chosen, chosenEnd = g, end
			}
		}
		if chosen != nil {
			selected = append(selected, chosen.components...)
		}
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].Start.Equal(selected[j].Start) {
			return selected[i].Metric < selected[j].Metric
		}
		return selected[i].Start.Before(selected[j].Start)
	})
	h := sha256.New()
	fmt.Fprintf(h, "signal=%s\n", candidate.Signal)
	for _, segment := range selected {
		fmt.Fprintf(h, "%s|%s|%.6f|%s|%s\n", segment.Metric, segment.Start.UTC().Format(time.RFC3339Nano), segment.Hours, segment.Source, segment.Quality)
	}
	wakeDay := candidate.Wake.In(loc)
	windowStart := time.Date(wakeDay.Year(), wakeDay.Month(), wakeDay.Day(), 12, 0, 0, 0, loc).AddDate(0, 0, -1)
	var episode []wakeInputSegment
	for _, segment := range segments {
		if segment.Metric == "sleep_total" || segment.Source == "" || segment.Hours <= 0 || segment.Quality != "ok" {
			continue
		}
		end := segment.Start.Add(time.Duration(segment.Hours * float64(time.Hour)))
		if segment.Start.Before(candidate.Wake) && end.After(windowStart) {
			episode = append(episode, segment)
		}
	}
	sort.Slice(episode, func(i, j int) bool {
		a, b := episode[i], episode[j]
		if !a.Start.Equal(b.Start) {
			return a.Start.Before(b.Start)
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Metric != b.Metric {
			return a.Metric < b.Metric
		}
		if a.Hours != b.Hours {
			return a.Hours < b.Hours
		}
		return a.Quality < b.Quality
	})
	fmt.Fprintln(h, "episode-inputs-v1")
	for _, segment := range episode {
		fmt.Fprintf(h, "%s|%s|%.6f|%s|%s\n", segment.Metric, segment.Start.UTC().Format(time.RFC3339Nano), segment.Hours, segment.Source, segment.Quality)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func postWakeSteps(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, localDate string, wake, now time.Time) (float64, error) {
	var steps sql.NullFloat64
	err := queryer.QueryRow(ctx, "select sum(source_total) from ("+
		physicalStepTotalsSQL("substring(date,1,10)=$1 and substring(date,21) ~ '^[+-]([01][0-9]|2[0-3])[0-5][0-9]$' and date::timestamptz between $2 and $3", "", "false")+") totals", localDate, wake, now).Scan(&steps)
	if err != nil {
		return 0, err
	}
	if !steps.Valid {
		return 0, nil
	}
	return steps.Float64, nil
}

func typicalDerivedWakeMinutes(ctx context.Context, queryer wakeQueryer, beforeDate string, days int, loc *time.Location) (int, bool, error) {
	if days <= 0 {
		days = 14
	}
	rows, err := queryer.Query(ctx, `
		SELECT value_timestamp
		  FROM derived_metrics
		 WHERE metric_name=$1
		   AND metric_date < $2
		   AND metric_date >= $2::date - $3::int
		 ORDER BY metric_date DESC
		 LIMIT $3
	`, DerivedMetricWakeTime, beforeDate, days)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	var minutes []int
	for rows.Next() {
		var wake time.Time
		if err := rows.Scan(&wake); err != nil {
			return 0, false, err
		}
		local := wake.In(loc)
		minutes = append(minutes, local.Hour()*60+local.Minute())
	}
	if err := rows.Err(); err != nil {
		return 0, false, err
	}
	if len(minutes) < 7 {
		return 0, false, nil
	}
	sort.Ints(minutes)
	return minutes[len(minutes)/2], true, nil
}

func (s *DB) typicalDerivedWakeMinutes(beforeDate string, days int, loc *time.Location) (int, bool, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	return typicalDerivedWakeMinutes(ctx, s.pool, beforeDate, days, loc)
}

func (s *DB) saveWakeStatus(localDate string, status MorningWakeStatus, now time.Time) error {
	if status.CandidateWake.IsZero() {
		return nil
	}
	metadata, err := json.Marshal(map[string]any{
		"confidence":              status.Confidence,
		"reason":                  status.Reason,
		"input_source":            status.InputSource,
		"source_selection_reason": status.SourceSelectionReason,
		"preferred_source":        status.PreferredSource,
		"latest_ingest":           status.LatestIngest,
		"post_wake_steps":         status.PostWakeSteps,
		"typical_wake_ok":         status.TypicalWakeOK,
		"signal":                  status.Signal,
	})
	if err != nil {
		return err
	}
	state := DerivedMetricStateProvisional
	var finalizedAt *time.Time
	if status.Ready {
		state = DerivedMetricStateFinal
		finalizedAt = &now
	}
	wake := status.CandidateWake
	return s.SaveDerivedMetric(DerivedMetric{
		MetricName:     DerivedMetricWakeTime,
		MetricDate:     localDate,
		ValueType:      DerivedValueTimestamp,
		ValueTimestamp: &wake,
		Unit:           "timestamp",
		State:          state,
		FormulaVersion: WakeFormulaVersion,
		InputsHash:     status.InputsHash,
		CalculatedAt:   now,
		FinalizedAt:    finalizedAt,
		Metadata:       metadata,
		MergeMetadata:  true,
	})
}

func loadWakeInputClock(ctx context.Context, tx pgx.Tx, localDate string) (string, time.Time, error) {
	var metadata []byte
	err := tx.QueryRow(ctx, `
		SELECT metadata FROM derived_metrics
		 WHERE metric_name=$1 AND metric_date=$2 FOR UPDATE
	`, DerivedMetricWakeTime, localDate).Scan(&metadata)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, nil
	}
	if err != nil {
		return "", time.Time{}, err
	}
	var marker struct {
		Hash      string    `json:"sleep_inputs_hash"`
		ChangedAt time.Time `json:"sleep_inputs_changed_at"`
	}
	if err := json.Unmarshal(metadata, &marker); err != nil {
		return "", time.Time{}, nil
	}
	return marker.Hash, marker.ChangedAt, nil
}

func wakeInputChangedAt(currentHash, previousHash string, previousChangedAt, now time.Time) time.Time {
	if currentHash != "" && currentHash == previousHash && !previousChangedAt.IsZero() {
		return previousChangedAt
	}
	if !previousChangedAt.IsZero() && now.Before(previousChangedAt) {
		return previousChangedAt
	}
	// Missing legacy markers intentionally start a fresh quiet window. This
	// avoids treating an old receipt timestamp as proof that partial data is
	// complete, while subsequent identical syncs preserve the clock.
	return now
}

func saveWakeStatusTx(ctx context.Context, tx pgx.Tx, localDate string, status MorningWakeStatus, now, inputChangedAt time.Time) error {
	if status.CandidateWake.IsZero() {
		return nil
	}
	metadata, err := json.Marshal(map[string]any{
		"confidence": status.Confidence, "reason": status.Reason,
		"source_selection_reason": status.SourceSelectionReason,
		"preferred_source":        status.PreferredSource,
		"input_source":            status.InputSource, "latest_ingest": status.LatestIngest,
		"post_wake_steps": status.PostWakeSteps, "typical_wake_ok": status.TypicalWakeOK,
		"signal": status.Signal, "sleep_inputs_hash": status.InputsHash,
		"sleep_inputs_changed_at": inputChangedAt,
	})
	if err != nil {
		return err
	}
	state := DerivedMetricStateProvisional
	var finalizedAt *time.Time
	if status.Ready {
		state = DerivedMetricStateFinal
		finalizedAt = &now
	}
	wake := status.CandidateWake
	_, err = tx.Exec(ctx, `
		INSERT INTO derived_metrics (
			metric_name, metric_date, value_type, value_timestamp, unit,
			state, formula_version, inputs_hash, calculated_at, finalized_at, metadata
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (metric_name, metric_date) DO UPDATE SET
			value_timestamp=EXCLUDED.value_timestamp,
			state=CASE WHEN derived_metrics.state='final' THEN derived_metrics.state ELSE EXCLUDED.state END,
			formula_version=EXCLUDED.formula_version, inputs_hash=EXCLUDED.inputs_hash,
			calculated_at=EXCLUDED.calculated_at,
			finalized_at=COALESCE(derived_metrics.finalized_at, EXCLUDED.finalized_at),
			metadata=derived_metrics.metadata || EXCLUDED.metadata
	`, DerivedMetricWakeTime, localDate, DerivedValueTimestamp, wake, "timestamp", state,
		WakeFormulaVersion, status.InputsHash, now, finalizedAt, json.RawMessage(metadata))
	return err
}

// RecordWakeCheckinEvidence annotates the canonical wake candidate with the
// time the user answered the existing morning check-in. The answer proves the
// user was awake by that moment, but does not claim the candidate time itself
// was exact.
func (s *DB) RecordWakeCheckinEvidence(localDate string, answeredAt time.Time) error {
	ctx, cancel := queryCtx()
	defer cancel()
	_, err := s.pool.Exec(ctx, `
		UPDATE derived_metrics
		   SET metadata = metadata || jsonb_build_object('subjective_checkin_answered_at', $3)
		 WHERE metric_name=$1 AND metric_date=$2
	`, DerivedMetricWakeTime, localDate, answeredAt.Format(time.RFC3339Nano))
	return err
}

// The immutable segment batch is shared by all confirmation decisions for a
// date. Retain the first clock observation per distinct confirmation state.
func (s *DB) captureWakeDecision(ctx context.Context, date string, loc *time.Location, now time.Time, segments []wakeInputSegment, status MorningWakeStatus, quiet time.Time) {
	if len(segments) > 4096 {
		return
	}
	_, _ = withDecisionJournal(ctx, s, func(db *DB, enabled bool) (bool, error) {
		if !enabled {
			return false, nil
		}
		type replaySegment struct {
			wakeInputSegment
			StartSecond time.Time
			StartNanos  int
		}
		rows := make([]replaySegment, len(segments))
		for i, segment := range segments {
			rows[i] = replaySegment{segment, segment.Start.UTC().Truncate(time.Second), segment.Start.Nanosecond()}
		}
		raw, err := json.Marshal(rows)
		if err != nil {
			return false, err
		}
		var typical *int
		if status.TypicalWakeOK {
			typical = &status.TypicalWakeMin
		}
		contextData, err := json.Marshal(map[string]any{"zone": loc.String(), "preferred_source": status.PreferredSource,
			"decision_at": now, "quiet_since": quiet, "steps": status.PostWakeSteps, "typical_minute": typical,
			"sleep_inputs_hash": status.InputsHash})
		if err != nil {
			return false, err
		}
		expected, err := json.Marshal(struct {
			MorningWakeStatus
			StoredWake time.Time
		}{status, status.CandidateWake.Truncate(time.Microsecond)})
		if err != nil {
			return false, err
		}
		_, err = db.pool.Exec(ctx, `with packet as (
			select $1::text as day, $2::jsonb as segments, $3::jsonb as context, $4::jsonb as observed
		), bundle as (
			select *, encode(sha256(convert_to(segments::text,'UTF8')),'hex') as segment_hash from packet
		), saved_segments as (
			insert into hdbt_decision_inputs(metric,decision_key,input_hash,captured_at,formula_version,inputs,result)
			select 'wake_segments', day,segment_hash,statement_timestamp(),2,jsonb_build_object('segments',segments),'{}'::jsonb from bundle
			on conflict do nothing
		), confirmation as (
			select day,context||jsonb_build_object('segment_hash',segment_hash) as inputs,observed from bundle
		) insert into hdbt_decision_inputs(metric,decision_key,input_hash,captured_at,formula_version,inputs,result)
		select 'wake_confirmation',day,encode(sha256(convert_to(jsonb_build_array(2,inputs)::text,'UTF8')),'hex'),statement_timestamp(),2,inputs,observed
		from confirmation c where not exists (
			select from hdbt_decision_inputs old where old.metric='wake_confirmation' and old.decision_key=c.day
			  and old.formula_version=2 and old.inputs-'decision_at'=c.inputs-'decision_at'
			  and old.result->>'reason'=c.observed->>'reason'
		) on conflict do nothing`, date, json.RawMessage(raw), json.RawMessage(contextData), json.RawMessage(expected))
		return err == nil, err
	})
}
