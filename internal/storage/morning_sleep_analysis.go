package storage

import (
	"context"
	"math"
	"time"

	"health-receiver/internal/health"
)

// recordedMorningSleepEpisode uses the canonical source and currently recorded
// intervals. Analysis can describe gaps; it does not borrow the scheduler's
// stricter quietness or continuous-coverage gate.
func (s *DB) recordedMorningSleepEpisode(ctx context.Context, night health.CompletedNightSleep) (*health.MorningSleepEpisode, string, error) {
	segments, err := loadWakeSegments(ctx, s.pool, night.WakeDate, s.reportTZLocation())
	if err != nil {
		return nil, "read_error", err
	}
	episode, reason := buildRecordedSleepEpisode(segments, night, s.reportTZLocation())
	return episode, reason, nil
}

func buildRecordedSleepEpisode(segments []wakeInputSegment, night health.CompletedNightSleep, loc *time.Location) (*health.MorningSleepEpisode, string) {
	if night.CaptureCompleteness != health.NightCaptureComplete || night.DurationAssessment != health.NightDurationPlausible {
		return nil, "capture_not_complete"
	}
	var selected []wakeInputSegment
	for _, s := range segments {
		// Match the existing architecture contract: midnight summary rows are
		// aggregates, not interval starts. Mixing them creates artificial overlaps.
		if s.Source == night.Source && s.Metric != "sleep_total" && s.Start.Format("15:04:05") != "00:00:00" {
			selected = append(selected, s)
		}
	}
	candidate, ok, err := selectWakeCandidate(selected, night.WakeDate, loc)
	if err != nil || !ok || candidate.Signal != "detailed_stage_end" {
		return nil, "detailed_intervals_unavailable"
	}
	previousNoon := time.Date(candidate.Wake.In(loc).Year(), candidate.Wake.In(loc).Month(), candidate.Wake.In(loc).Day(), 12, 0, 0, 0, loc).AddDate(0, 0, -1)
	stages := map[string][]sleepSegment{}
	var asleep, all []sleepSegment
	var start time.Time
	for _, s := range selected {
		end := s.Start.Add(time.Duration(s.Hours * float64(time.Hour)))
		if s.Start.Before(previousNoon) || end.After(candidate.Wake.Add(time.Second)) || !end.After(s.Start) {
			continue
		}
		interval := sleepSegment{Start: s.Start, End: end}
		stages[s.Metric] = append(stages[s.Metric], interval)
		all = append(all, interval)
		if s.Metric != "sleep_awake" {
			asleep = append(asleep, interval)
		}
		if start.IsZero() || s.Start.Before(start) {
			start = s.Start
		}
	}
	minutes := func(intervals []sleepSegment) float64 {
		var n float64
		for _, v := range mergeSegments(intervals, 0) {
			n += v.End.Sub(v.Start).Minutes()
		}
		return n
	}
	asleepMinutes := minutes(asleep)
	if start.IsZero() || math.Abs(asleepMinutes-night.DurationHours*60) > 1 {
		return nil, "recorded_duration_mismatch"
	}
	localStart, localWake := start.In(loc), candidate.Wake.In(loc)
	episode := &health.MorningSleepEpisode{Start: &localStart, Wake: &localWake, AsleepMinutes: int(asleepMinutes + 0.5), WindowMinutes: int(candidate.Wake.Sub(start).Minutes() + 0.5)}
	unknown := episode.WindowMinutes - int(minutes(all)+0.5)
	if unknown > 0 {
		episode.UnclassifiedMinutes = &unknown
	}
	conflicting := false
	metrics := []string{"sleep_deep", "sleep_rem", "sleep_core", "sleep_unspecified", "sleep_awake"}
	for i, a := range metrics {
		for _, b := range metrics[i+1:] {
			for _, x := range stages[a] {
				for _, y := range stages[b] {
					if x.Start.Before(y.End.Add(-time.Second)) && y.Start.Before(x.End.Add(-time.Second)) {
						conflicting = true
					}
				}
			}
		}
	}
	if conflicting {
		episode.StageStatus = "overlapping_stage_labels"
		return episode, "duration_and_timing_only"
	}
	for metric, target := range map[string]**int{"sleep_deep": &episode.DeepMinutes, "sleep_rem": &episode.REMMinutes, "sleep_core": &episode.CoreMinutes, "sleep_unspecified": &episode.UnspecifiedMinutes, "sleep_awake": &episode.AwakeMinutes} {
		if intervals := stages[metric]; len(intervals) > 0 {
			n := int(minutes(intervals) + 0.5)
			*target = &n
		}
	}
	if episode.AwakeMinutes != nil {
		n := len(mergeSegments(stages["sleep_awake"], 0))
		episode.AwakeBouts = &n
	}
	episode.StageStatus = "recorded"
	return episode, "available"
}
