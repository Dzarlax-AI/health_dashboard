package storage

import (
	"context"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/sleepstudy"
)

// studyRejection examines inputs to the frozen policy without relaxing its
// gates. The categories distinguish one blocking gate from joint blockers.
func studyRejection(day *studyDay, window *sleepstudy.Window) string {
	if window == nil || day == nil {
		return ""
	}
	bound := sleepstudy.Span{Start: window.Start, End: window.End}
	var enoughTotal, enoughInWindow, coverageOnly, groupingOnly, coarseOnly bool
	for _, c := range day.candidates {
		if sleepstudy.Duration(c.episode.Asleep) >= window.MinAsleep {
			enoughTotal = true
		}
		if sleepstudy.Overlap(c.episode.Asleep, bound) < window.MinAsleep {
			continue
		}
		enoughInWindow = true
		coverageOnly = coverageOnly || (!c.profileCovered && !c.sleepBridge && !c.coarseEdge)
		groupingOnly = groupingOnly || (c.profileCovered && c.sleepBridge && !c.coarseEdge)
		coarseOnly = coarseOnly || (c.profileCovered && !c.sleepBridge && c.coarseEdge)
	}
	switch {
	case !enoughTotal:
		return "insufficient_duration_even_without_window"
	case !enoughInWindow:
		return "enough_duration_but_outside_window"
	case groupingOnly && !coverageOnly && !coarseOnly:
		return "asleep_gap_only"
	case coverageOnly && !groupingOnly && !coarseOnly:
		return "coverage_only"
	case coarseOnly && !groupingOnly && !coverageOnly:
		return "coarse_extension_only"
	default:
		return "combined_candidate_gates"
	}
}

// studyHistoryRejection separates a genuinely short same-zone candidate
// history from dates where sufficient candidate days fail the history rule.
func studyHistoryRejection(date, zone string, history []sleepstudy.PriorDay) string {
	from := subtractDays(date, sleepstudy.HistoryDays)
	sameZone, usable := 0, 0
	for _, day := range history {
		if day.Zone != zone || day.Date < from || day.Date >= date {
			continue
		}
		sameZone++
		for _, e := range day.Episodes {
			if e.Covered {
				usable++
				break
			}
		}
	}
	switch {
	case sameZone < sleepstudy.HistoryMinDays:
		return "too_few_same_zone_candidate_days"
	case usable < sleepstudy.HistoryMinDays:
		return "too_few_covered_history_days"
	default:
		return "history_timing_or_episode_ambiguity"
	}
}

type studyDurationReference struct {
	hours float64
	final bool
}

// studyOrdinaryReferences is an evaluator only. These labels were generated
// by the separate ordinary pipeline from the same wearable data. A duration
// match establishes agreement with that pipeline, not verified episode identity.
func studyOrdinaryReferences(ctx context.Context, tx pgx.Tx, from, through string) (map[string]studyDurationReference, error) {
	rows, err := tx.Query(ctx, `
WITH per_source AS (
	SELECT substring(date,1,10) AS day, source, SUM(qty) AS hours
	FROM metric_points WHERE metric_name='night_sleep_total' AND qty>0 AND quality='ok'
	AND substring(date,1,10) BETWEEN $1 AND $2 GROUP BY 1,2
), ordinary AS (
	SELECT day, MAX(hours) AS hours FROM per_source GROUP BY day
)
SELECT d.day,d.hours,coalesce(n.capture_completeness='complete'
	AND n.finalization_state='final' AND n.claim_eligibility='eligible'
	AND abs(n.duration_hours-d.hours)*3600<=1,false)
FROM ordinary d LEFT JOIN completed_night_sleep n ON n.wake_date=d.day`, from, through)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]studyDurationReference{}
	for rows.Next() {
		var day string
		var reference studyDurationReference
		if err := rows.Scan(&day, &reference.hours, &reference.final); err != nil {
			return nil, err
		}
		out[day] = reference
	}
	return out, rows.Err()
}

func (s *studySummary) evaluateOrdinary(reference studyDurationReference, selected *LocalShadowSleep) {
	recorded := selected != nil && selected.Total != nil
	for _, row := range []struct {
		prefix string
		valid  bool
	}{{"ordinary", reference.hours > 0}, {"ordinary_final_corroborated", reference.final}} {
		if !row.valid {
			continue
		}
		match := recorded && math.Abs(*selected.Total-reference.hours) <= time.Second.Hours()
		s.tally(row.prefix+"_reference_dates", true)
		s.tally(row.prefix+"_selected", recorded)
		s.tally(row.prefix+"_abstained", !recorded)
		s.tally(row.prefix+"_duration_agrees_1s", match)
		s.tally(row.prefix+"_duration_differs_over_1s", recorded && !match)
	}
}

// This exploratory total limits how many long observed breaks can join
// widely separated sleep blocks. It is an engineering bound for this scenario.
const studyMaxRecordedWakeTotal = 2 * time.Hour

// studyRecordedWakeOnly tests whether the same captured episode could be
// considered if its long gaps were explicitly observed awake. It is a
// same-day counterfactual. It does not change the classifier or its history.
func studyRecordedWakeOnly(day *studyDay, window *sleepstudy.Window) sleepstudy.Decision {
	if day == nil || window == nil {
		return sleepstudy.Decision{}
	}
	episodes := make([]sleepstudy.Episode, 0, len(day.candidates))
	for _, c := range day.candidates {
		e := c.episode
		if c.profileCovered && !c.coarseEdge && c.sleepBridge {
			var awake []sleepstudy.Span
			for _, stage := range c.candidate.Stages {
				if stage.Kind == "awake" {
					awake = append(awake, sleepstudy.Span{Start: stage.Start, End: stage.End})
				}
			}
			e.Covered = studyHasRecordedWakeGaps(c.episode.Asleep, awake, sleepstudy.Span{Start: window.Start, End: window.End})
		}
		episodes = append(episodes, e)
	}
	return sleepstudy.Select(episodes, window, "")
}

func studyHasRecordedWakeGaps(asleep, awake []sleepstudy.Span, bound sleepstudy.Span) bool {
	ordered := sleepstudy.Union(asleep)
	var totalGap time.Duration
	for i := 1; i < len(ordered); i++ {
		gap := sleepstudy.Span{Start: ordered[i-1].End, End: ordered[i].Start}
		duration := gap.End.Sub(gap.Start)
		if duration <= studyBridgeLimit {
			continue
		}
		totalGap += duration
		if totalGap > studyMaxRecordedWakeTotal || gap.Start.Before(bound.Start) || gap.End.After(bound.End) ||
			sleepstudy.Overlap(awake, gap) < duration*9/10 {
			return false
		}
	}
	return totalGap > 0
}
