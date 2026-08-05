package storage

import (
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
	ReceivedAt time.Time
}

type wakeEligibleSegment struct {
	wakeInputSegment
	End time.Time
}

type wakeCandidate struct {
	Wake         time.Time
	LatestIngest time.Time
	Source       string
	InputsHash   string
	Signal       string
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
	candidate, ok, err := s.loadWakeCandidate(localDate, loc)
	if err != nil {
		return MorningWakeStatus{Confidence: WakeConfidenceLow, Reason: "query_error"}, err
	}
	if !ok {
		return MorningWakeStatus{Confidence: WakeConfidenceLow, Reason: "no_data"}, nil
	}
	steps, err := s.postWakeSteps(localDate, candidate.Wake, now)
	if err != nil {
		return MorningWakeStatus{Confidence: WakeConfidenceLow, Reason: "steps_query_error"}, err
	}
	typicalMin, typicalOK, err := s.typicalDerivedWakeMinutes(localDate, 14, loc)
	if err != nil {
		return MorningWakeStatus{Confidence: WakeConfidenceLow, Reason: "typical_query_error"}, err
	}
	status := evaluateMorningWake(candidate, now, steps, typicalMin, typicalOK, loc)
	if err := s.saveWakeStatus(localDate, status, now); err != nil {
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
	targetDate, err := time.ParseInLocation("2006-01-02", localDate, loc)
	if err != nil {
		return wakeCandidate{}, false, fmt.Errorf("parse wake date: %w", err)
	}
	fromDate := targetDate.AddDate(0, 0, -1).Format("2006-01-02")
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT metric_name, date, qty, source, received_at
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
		var dateStr, source string
		var hours float64
		var receivedAt time.Time
		var metric string
		if err := rows.Scan(&metric, &dateStr, &hours, &source, &receivedAt); err != nil {
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
	for _, segment := range selected {
		fmt.Fprintf(h, "%s|%s|%.6f|%s\n", segment.Metric, segment.Start.UTC().Format(time.RFC3339Nano), segment.Hours, segment.Source)
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
	return source, []wakeEligibleSegment{{
		wakeInputSegment: wakeInputSegment{
			Metric:     "sleep_summary",
			Start:      best.start,
			Hours:      best.asleepHours + best.awakeHours,
			Source:     best.source,
			ReceivedAt: best.latestIngest,
		},
		End: wake,
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
	if !candidate.LatestIngest.IsZero() && now.Sub(candidate.LatestIngest) < wakeIngestQuiet {
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

func (s *DB) postWakeSteps(localDate string, wake, now time.Time) (float64, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	var steps sql.NullFloat64
	err := s.pool.QueryRow(ctx, `
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

func (s *DB) typicalDerivedWakeMinutes(beforeDate string, days int, loc *time.Location) (int, bool, error) {
	if days <= 0 {
		days = 14
	}
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT value_timestamp
		  FROM derived_metrics
		 WHERE metric_name=$1
		   AND metric_date < $2
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
	})
}

// RecordWakeCheckinEvidence annotates the canonical wake candidate with the
// time the user answered the existing morning check-in. The answer proves the
// user was awake by that moment, but does not claim the candidate time itself
// was exact.
func (s *DB) RecordWakeCheckinEvidence(localDate string, answeredAt time.Time) error {
	metric, err := s.GetDerivedMetric(DerivedMetricWakeTime, localDate)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil || metric == nil {
		return err
	}
	metadata := map[string]any{}
	if len(metric.Metadata) != 0 {
		_ = json.Unmarshal(metric.Metadata, &metadata)
	}
	metadata["subjective_checkin_answered_at"] = answeredAt
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	metric.Metadata = encoded
	return s.SaveDerivedMetric(*metric)
}
