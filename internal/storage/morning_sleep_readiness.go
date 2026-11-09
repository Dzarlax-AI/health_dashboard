package storage

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/health"
)

// MorningSleepReadiness reconstructs a narrowly observed episode without
// changing canonical completeness, finality, or eligibility for comparisons.
func (s *DB) MorningSleepReadiness(ctx context.Context, date string, now time.Time) (health.MorningSleepReadiness, error) {
	none := health.MorningSleepReadiness{State: health.MorningSleepMissing, Reason: "unavailable"}
	loc := s.reportTZLocation()
	day, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return none, err
	}
	rows, err := s.pool.Query(ctx, `SELECT metric_name,date,qty,source,quality,received_at FROM metric_points WHERE metric_name IN ('sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_unspecified','sleep_awake') AND quality='ok' AND qty>0 AND SUBSTRING(date,1,10) BETWEEN $1 AND $2 ORDER BY date,metric_name`, day.AddDate(0, 0, -1).Format("2006-01-02"), date)
	if err != nil {
		return none, err
	}
	var segments []wakeInputSegment
	for rows.Next() {
		var v wakeInputSegment
		var timestamp string
		if err := rows.Scan(&v.Metric, &timestamp, &v.Hours, &v.Source, &v.Quality, &v.ReceivedAt); err != nil {
			rows.Close()
			return none, err
		}
		v.Start, err = parseMetricDate(timestamp)
		if err == nil {
			segments = append(segments, v)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return none, err
	}
	candidate, ok, err := selectWakeCandidate(segments, date, loc)
	if err != nil || !ok {
		none.Reason = "wake_unavailable"
		if len(segments) > 0 {
			none.State = health.MorningSleepCollecting
		}
		return none, err
	}
	none.State = health.MorningSleepCollecting
	saved, err := s.GetDerivedMetric(DerivedMetricWakeTime, date)
	if errors.Is(err, pgx.ErrNoRows) {
		none.Reason = "wake_unavailable"
		return none, nil
	}
	if err != nil {
		return none, err
	}
	var meta struct {
		Confidence string    `json:"confidence"`
		Reason     string    `json:"reason"`
		Source     string    `json:"input_source"`
		Signal     string    `json:"signal"`
		Hash       string    `json:"sleep_inputs_hash"`
		ChangedAt  time.Time `json:"sleep_inputs_changed_at"`
	}
	if json.Unmarshal(saved.Metadata, &meta) != nil || saved.ValueTimestamp == nil || !observedWakeConfirmed(meta.Confidence, meta.Reason, candidate.Wake, now) || meta.Source != candidate.Source || meta.Signal != "detailed_stage_end" || candidate.Signal != meta.Signal || meta.Hash == "" || meta.Hash != candidate.InputsHash || saved.InputsHash != candidate.InputsHash || !saved.ValueTimestamp.Equal(candidate.Wake) || meta.ChangedAt.IsZero() || now.Before(meta.ChangedAt.Add(wakeIngestQuiet)) {
		none.Reason = "wake_not_stable"
		return none, nil
	}
	return observedSleepEpisode(segments, candidate, loc)
}

func observedWakeConfirmed(confidence, reason string, wake, now time.Time) bool {
	if confidence == WakeConfidenceHigh {
		return true
	}
	if confidence != WakeConfidenceMedium {
		return false
	}
	return (reason == "quiet_timeout" && now.Sub(wake) >= wakePassiveAge) || (reason == "early_candidate_timeout" && now.Sub(wake) >= wakeEarlyAge)
}

func observedSleepEpisode(segments []wakeInputSegment, candidate wakeCandidate, loc *time.Location) (health.MorningSleepReadiness, error) {
	none := health.MorningSleepReadiness{State: health.MorningSleepGappedOrConflicting, Reason: "incomplete_intervals"}
	w := candidate.Wake.In(loc)
	windowStart := time.Date(w.Year(), w.Month(), w.Day(), 12, 0, 0, 0, loc).AddDate(0, 0, -1)
	type interval struct {
		start, end time.Time
		asleep     bool
		metric     string
	}
	var selected []interval
	for _, s := range segments {
		if s.Source != candidate.Source || s.Metric == "sleep_total" || s.Hours <= 0 || s.Start.After(candidate.Wake) {
			continue
		}
		end := s.Start.Add(time.Duration(s.Hours * float64(time.Hour)))
		if end.After(candidate.Wake.Add(time.Second)) && s.Start.Before(candidate.Wake) {
			none.Reason = "conflicting_intervals"
			return none, nil
		}
		if end.After(candidate.Wake.Add(time.Second)) || !end.After(windowStart) {
			continue
		}
		selected = append(selected, interval{s.Start, end, s.Metric != "sleep_awake", s.Metric})
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].end.After(selected[j].end) })
	if len(selected) == 0 {
		none.State = health.MorningSleepMissing
		return none, nil
	}
	if selected[0].end.Sub(candidate.Wake).Abs() > time.Second {
		return none, nil
	}
	start := candidate.Wake
	var run []interval
	for _, v := range selected {
		if v.end.Before(start.Add(-time.Second)) {
			return none, nil
		}
		if v.start.Before(start) {
			start = v.start
		}
		run = append(run, v)
	}
	if len(run) == 0 {
		return none, nil
	}
	// Inputs earlier than the wake hash's prior-noon boundary cannot share its
	// quietness proof; fail closed if a segment crosses that boundary.
	if start.Before(windowStart) {
		none.State = health.MorningSleepCollecting
		none.Reason = "untracked_interval_stability"
		return none, nil
	}
	// Overlapping sources and overlapping different stages are ambiguous; do
	// not sum them or claim a continuous main episode.
	for i, a := range run {
		for _, b := range run[i+1:] {
			if a.start.Before(b.end.Add(-time.Second)) && b.start.Before(a.end.Add(-time.Second)) {
				none.Reason = "conflicting_intervals"
				return none, nil
			}
		}
	}
	for _, s := range segments {
		if s.Source == candidate.Source || s.Metric == "sleep_total" || s.Hours <= 0 {
			continue
		}
		end := s.Start.Add(time.Duration(s.Hours * float64(time.Hour)))
		if s.Start.Before(candidate.Wake.Add(-time.Second)) && end.After(start.Add(time.Second)) {
			none.Reason = "conflicting_source"
			return none, nil
		}
	}
	var hours float64
	for _, v := range run {
		if v.asleep {
			hours += v.end.Sub(v.start).Hours()
		}
	}
	if hours <= 0 {
		none.State = health.MorningSleepMissing
		return none, nil
	}
	if hours > 24 {
		return none, nil
	}
	localStart, localWake := start.In(loc), candidate.Wake.In(loc)
	episode := &health.MorningSleepEpisode{Start: &localStart, Wake: &localWake, AsleepMinutes: int(hours*60 + 0.5), WindowMinutes: int(candidate.Wake.Sub(start).Minutes() + 0.5)}
	totals := map[string]float64{}
	awakeBouts, previousAwake := 0, false
	for _, v := range run {
		totals[v.metric] += v.end.Sub(v.start).Minutes()
		if !v.asleep && !previousAwake {
			awakeBouts++
		}
		previousAwake = !v.asleep
	}
	for metric, target := range map[string]**int{"sleep_deep": &episode.DeepMinutes, "sleep_rem": &episode.REMMinutes, "sleep_core": &episode.CoreMinutes, "sleep_unspecified": &episode.UnspecifiedMinutes, "sleep_awake": &episode.AwakeMinutes} {
		if value, present := totals[metric]; present {
			minutes := int(value + 0.5)
			*target = &minutes
		}
	}
	if episode.AwakeMinutes != nil {
		episode.AwakeBouts = &awakeBouts
	}
	return health.MorningSleepReadiness{State: health.MorningSleepObservedStable, Hours: hours, Wake: candidate.Wake, Episode: episode}, nil
}
