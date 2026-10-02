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

	"github.com/jackc/pgx/v5"
)

const (
	WakeFormulaVersion = "wake-v1"

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
	Ready          bool      `json:"ready"`
	Confidence     string    `json:"confidence"`
	Reason         string    `json:"reason"`
	CandidateWake  time.Time `json:"candidate_wake,omitempty"`
	LatestIngest   time.Time `json:"latest_ingest,omitempty"`
	PostWakeSteps  float64   `json:"post_wake_steps"`
	InputSource    string    `json:"input_source,omitempty"`
	InputsHash     string    `json:"inputs_hash,omitempty"`
	TypicalWakeMin int       `json:"typical_wake_min,omitempty"`
	TypicalWakeOK  bool      `json:"typical_wake_ok"`
	Signal         string    `json:"signal,omitempty"`
}

type wakeInputSegment struct {
	Metric     string
	Start      time.Time
	Hours      float64
	Source     string
	Quality    string
	ReceivedAt time.Time
}

type wakeEligibleSegment struct {
	wakeInputSegment
	End            time.Time
	HashComponents []wakeInputSegment
}

type wakeCandidate struct {
	Wake             time.Time
	LatestIngest     time.Time
	IngestQuietSince time.Time
	Source           string
	InputsHash       string
	Signal           string
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
		Confidence:    WakeConfidenceLow,
		Reason:        "candidate_only",
		CandidateWake: candidate.Wake,
		LatestIngest:  candidate.LatestIngest,
		InputSource:   candidate.Source,
		InputsHash:    candidate.InputsHash,
		Signal:        candidate.Signal,
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
	candidate, ok, err := loadWakeCandidate(ctx, tx, localDate, loc)
	if err != nil {
		return MorningWakeStatus{Confidence: WakeConfidenceLow, Reason: "query_error"}, err
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
	status := evaluateMorningWake(candidate, now, steps, typicalMin, typicalOK, loc)
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
			Confidence:    WakeConfidenceLow,
			Reason:        "historical_backfill",
			CandidateWake: candidate.Wake,
			LatestIngest:  candidate.LatestIngest,
			InputSource:   candidate.Source,
			InputsHash:    candidate.InputsHash,
			Signal:        candidate.Signal,
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
	return loadWakeCandidate(ctx, s.pool, localDate, loc)
}

type wakeQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func loadWakeCandidate(ctx context.Context, queryer wakeQueryer, localDate string, loc *time.Location) (wakeCandidate, bool, error) {
	targetDate, err := time.ParseInLocation("2006-01-02", localDate, loc)
	if err != nil {
		return wakeCandidate{}, false, fmt.Errorf("parse wake date: %w", err)
	}
	fromDate := targetDate.AddDate(0, 0, -1).Format("2006-01-02")
	rows, err := queryer.Query(ctx, `
		SELECT metric_name, date, qty, source, quality, received_at
		  FROM metric_points
		 WHERE metric_name IN ('sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_unspecified','sleep_awake')
		   AND quality='ok'
		   AND qty > 0
		   AND SUBSTRING(date,1,10) BETWEEN $1 AND $2
		 ORDER BY date, metric_name
	`, fromDate, localDate)
	if err != nil {
		return wakeCandidate{}, false, err
	}
	defer rows.Close()
	var segments []wakeInputSegment
	for rows.Next() {
		var dateStr, source, quality string
		var hours float64
		var receivedAt time.Time
		var metric string
		if err := rows.Scan(&metric, &dateStr, &hours, &source, &quality, &receivedAt); err != nil {
			return wakeCandidate{}, false, err
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
		return wakeCandidate{}, false, err
	}
	return selectWakeCandidate(segments, localDate, loc)
}

func selectWakeCandidate(segments []wakeInputSegment, localDate string, loc *time.Location) (wakeCandidate, bool, error) {
	if loc == nil {
		loc = time.UTC
	}
	isWakeWindow := func(end time.Time) bool {
		localEnd := end.In(loc)
		if localEnd.Format("2006-01-02") != localDate {
			return false
		}
		minute := localEnd.Hour()*60 + localEnd.Minute()
		return minute >= 3*60 && minute <= 15*60
	}
	isMidnight := func(start time.Time) bool {
		return start.In(loc).Format("15:04:05") == "00:00:00"
	}

	var detailed, rawTotal []wakeEligibleSegment
	detailedTotals := map[string]float64{}
	sleepTotalTotals := map[string]float64{}
	for _, segment := range segments {
		if segment.Hours <= 0 || segment.Source == "" {
			continue
		}
		end := segment.Start.Add(time.Duration(segment.Hours * float64(time.Hour)))
		if isMidnight(segment.Start) || !isWakeWindow(end) {
			continue
		}
		if segment.Metric == "sleep_total" {
			sleepTotalTotals[segment.Source] += segment.Hours
			rawTotal = append(rawTotal, wakeEligibleSegment{wakeInputSegment: segment, End: end})
		} else {
			detailed = append(detailed, wakeEligibleSegment{wakeInputSegment: segment, End: end})
		}
		if segment.Metric != "sleep_total" && segment.Metric != "sleep_awake" {
			detailedTotals[segment.Source] += segment.Hours
		}
	}

	source := pickWinningSource(detailedTotals)
	selected := detailed
	signal := "detailed_stage_end"
	if source == "" {
		source = pickWinningSource(sleepTotalTotals)
		selected = rawTotal
		signal = "raw_sleep_total_end"
	}
	if source != "" {
		selected = slicesForSource(selected, source)
	}
	if source == "" || len(selected) == 0 {
		var ok bool
		source, selected, ok = selectMidnightSummary(segments, localDate, loc, isWakeWindow)
		if !ok {
			return wakeCandidate{}, false, nil
		}
		signal = "midnight_summary"
	}

	sort.Slice(selected, func(i, j int) bool {
		if selected[i].Start.Equal(selected[j].Start) {
			return selected[i].Metric < selected[j].Metric
		}
		return selected[i].Start.Before(selected[j].Start)
	})
	var wake, latestIngest time.Time
	for _, segment := range selected {
		if segment.End.After(wake) {
			wake = segment.End
		}
		if segment.ReceivedAt.After(latestIngest) {
			latestIngest = segment.ReceivedAt
		}
	}
	if wake.IsZero() {
		return wakeCandidate{}, false, nil
	}
	h := sha256.New()
	fmt.Fprintf(h, "signal=%s\n", signal)
	for _, segment := range selected {
		components := segment.HashComponents
		if len(components) == 0 {
			components = []wakeInputSegment{segment.wakeInputSegment}
		}
		for _, component := range components {
			fmt.Fprintf(h, "%s|%s|%.6f|%s|%s\n", component.Metric, component.Start.UTC().Format(time.RFC3339Nano), component.Hours, component.Source, component.Quality)
		}
	}
	return wakeCandidate{
		Wake:         wake,
		LatestIngest: latestIngest,
		Source:       source,
		InputsHash:   hex.EncodeToString(h.Sum(nil)),
		Signal:       signal,
	}, true, nil
}

func slicesForSource(segments []wakeEligibleSegment, source string) []wakeEligibleSegment {
	out := make([]wakeEligibleSegment, 0, len(segments))
	for _, segment := range segments {
		if segment.Source == source {
			out = append(out, segment)
		}
	}
	return out
}

func selectMidnightSummary(
	segments []wakeInputSegment,
	localDate string,
	loc *time.Location,
	isWakeWindow func(time.Time) bool,
) (string, []wakeEligibleSegment, bool) {
	type summaryGroup struct {
		start        time.Time
		source       string
		asleepHours  float64
		awakeHours   float64
		latestIngest time.Time
		components   []wakeInputSegment
	}
	groups := map[string]*summaryGroup{}
	sourceTotals := map[string]float64{}
	for _, segment := range segments {
		if segment.Hours <= 0 || segment.Source == "" ||
			segment.Start.In(loc).Format("2006-01-02 15:04:05") != localDate+" 00:00:00" {
			continue
		}
		if segment.Metric != "sleep_total" && segment.Metric != "sleep_awake" {
			continue
		}
		key := segment.Source + "|" + segment.Start.UTC().Format(time.RFC3339Nano)
		group := groups[key]
		if group == nil {
			group = &summaryGroup{start: segment.Start, source: segment.Source}
			groups[key] = group
		}
		if segment.Metric == "sleep_total" {
			group.asleepHours += segment.Hours
			sourceTotals[segment.Source] += segment.Hours
		} else {
			group.awakeHours += segment.Hours
		}
		group.components = append(group.components, segment)
		if segment.ReceivedAt.After(group.latestIngest) {
			group.latestIngest = segment.ReceivedAt
		}
	}
	source := pickWinningSource(sourceTotals)
	if source == "" {
		return "", nil, false
	}
	var best *summaryGroup
	var wake time.Time
	for _, group := range groups {
		if group.source != source || group.asleepHours <= 0 {
			continue
		}
		end := group.start.Add(time.Duration((group.asleepHours + group.awakeHours) * float64(time.Hour)))
		if isWakeWindow(end) && end.After(wake) {
			best = group
			wake = end
		}
	}
	if best == nil {
		return "", nil, false
	}
	sort.Slice(best.components, func(i, j int) bool {
		if best.components[i].Start.Equal(best.components[j].Start) {
			return best.components[i].Metric < best.components[j].Metric
		}
		return best.components[i].Start.Before(best.components[j].Start)
	})
	return source, []wakeEligibleSegment{{
		wakeInputSegment: wakeInputSegment{
			Metric:     "sleep_summary",
			Start:      best.start,
			Hours:      best.asleepHours + best.awakeHours,
			Source:     best.source,
			ReceivedAt: best.latestIngest,
		},
		End:            wake,
		HashComponents: best.components,
	}}, true
}

func evaluateMorningWake(candidate wakeCandidate, now time.Time, steps float64, typicalMin int, typicalOK bool, loc *time.Location) MorningWakeStatus {
	status := MorningWakeStatus{
		Confidence:     WakeConfidenceLow,
		Reason:         "recent_segment",
		CandidateWake:  candidate.Wake,
		LatestIngest:   candidate.LatestIngest,
		PostWakeSteps:  steps,
		InputSource:    candidate.Source,
		InputsHash:     candidate.InputsHash,
		TypicalWakeMin: typicalMin,
		TypicalWakeOK:  typicalOK,
		Signal:         candidate.Signal,
	}
	candidateAge := now.Sub(candidate.Wake)
	if candidateAge < 0 || candidateAge < wakeActivityAge {
		return status
	}
	ingestQuietSince := candidate.IngestQuietSince
	if ingestQuietSince.IsZero() {
		ingestQuietSince = candidate.LatestIngest
	}
	if !ingestQuietSince.IsZero() && now.Sub(ingestQuietSince) < wakeIngestQuiet {
		status.Reason = "still_writing"
		return status
	}
	if steps >= wakeActivitySteps {
		status.Ready = true
		if candidate.Signal == "detailed_stage_end" {
			status.Confidence = WakeConfidenceHigh
			status.Reason = "post_wake_activity"
		} else {
			status.Confidence = WakeConfidenceMedium
			status.Reason = "post_wake_activity_fallback"
		}
		return status
	}
	if candidate.Signal != "detailed_stage_end" && candidateAge < wakeFallbackAge {
		status.Reason = "fallback_candidate"
		return status
	}
	if typicalOK {
		localWake := candidate.Wake.In(loc)
		candidateMin := localWake.Hour()*60 + localWake.Minute()
		if typicalMin-candidateMin > int(wakeEarlyTolerance/time.Minute) {
			typicalGuard := time.Date(
				localWake.Year(), localWake.Month(), localWake.Day(),
				typicalMin/60, typicalMin%60, 0, 0, loc,
			).Add(-wakeTypicalGuardOffset)
			earlyDeadline := candidate.Wake.Add(wakeEarlyAge)
			if typicalGuard.Before(earlyDeadline) {
				earlyDeadline = typicalGuard
			}
			if now.Before(earlyDeadline) {
				status.Reason = "early_candidate"
				return status
			}
			status.Ready = true
			status.Confidence = WakeConfidenceMedium
			status.Reason = "early_candidate_timeout"
			return status
		}
	}
	if candidateAge < wakePassiveAge {
		status.Reason = "awaiting_confirmation"
		return status
	}
	status.Ready = true
	status.Confidence = WakeConfidenceMedium
	status.Reason = "quiet_timeout"
	return status
}

func postWakeSteps(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, localDate string, wake, now time.Time) (float64, error) {
	var steps sql.NullFloat64
	err := queryer.QueryRow(ctx, `
		WITH source_totals AS (
			SELECT source, SUM(qty) AS source_total
			  FROM metric_points
			 WHERE metric_name='step_count'
			   AND quality='ok'
			   AND qty > 0
			   AND SUBSTRING(date,1,10)=$1
			   AND date::timestamptz >= $2
			   AND date::timestamptz <= $3
			 GROUP BY source
		) `+preferredSourceSQL, localDate, wake, now).Scan(&steps)
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
		"confidence":      status.Confidence,
		"reason":          status.Reason,
		"input_source":    status.InputSource,
		"latest_ingest":   status.LatestIngest,
		"post_wake_steps": status.PostWakeSteps,
		"typical_wake_ok": status.TypicalWakeOK,
		"signal":          status.Signal,
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
		"input_source": status.InputSource, "latest_ingest": status.LatestIngest,
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
