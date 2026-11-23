package storage

import (
	"sort"
	"time"
)

// Original feature writer kept as an independent acceptance reference.
// ComputeSleepArchitectureDays computes per-end-date sleep architecture
// from raw metric segments. It picks one source per date before
// deriving architecture features.
func ComputeSleepArchitectureDays(raw []SleepArchitectureRawSegment) map[string]SleepArchitectureDay {
	return computeSleepArchitectureDays(raw, "")
}

func computeSleepArchitectureDays(raw []SleepArchitectureRawSegment, preferred string) map[string]SleepArchitectureDay {
	grouped := map[string][]architectureSegment{}
	for _, r := range raw {
		if r.Hours <= 0 || r.Source == "" {
			continue
		}
		start, err := parseSleepDate(r.Date)
		if err != nil {
			continue
		}
		end := start.Add(time.Duration(r.Hours * float64(time.Hour)))
		date := end.Format(isoDate)
		grouped[date] = append(grouped[date], architectureSegment{
			Metric: r.MetricName,
			Start:  start,
			End:    end,
			Source: r.Source,
		})
	}

	out := map[string]SleepArchitectureDay{}
	for date, segs := range grouped {
		out[date] = computeSleepArchitectureDay(date, segs, preferred)
	}
	return out
}

func computeSleepArchitectureDay(date string, segs []architectureSegment, preferred string) SleepArchitectureDay {
	if len(segs) == 0 {
		return missingArchitectureDay(date, SleepArchitectureReasonMissingSegments)
	}

	nightAsleep, source := architectureNightAsleepSegments(date, segs, preferred)
	if source == "" {
		return missingArchitectureDay(date, SleepArchitectureReasonMissingSegments)
	}

	var nightStart, nightEnd time.Time
	for i, s := range nightAsleep {
		if i == 0 || s.Start.Before(nightStart) {
			nightStart = s.Start
		}
		if i == 0 || s.End.After(nightEnd) {
			nightEnd = s.End
		}
	}

	var asleep, explicitWake []sleepSegment
	var asleepHours, stagedHours float64
	for _, s := range segs {
		if s.Source != source {
			continue
		}
		switch {
		case isArchitectureAsleepMetric(s.Metric):
			if !architectureSegmentInList(s, nightAsleep) {
				continue
			}
			h := s.End.Sub(s.Start).Hours()
			asleepHours += h
			if s.Metric != "sleep_unspecified" {
				stagedHours += h
			}
			asleep = append(asleep, sleepSegment{Start: s.Start, End: s.End})
		case s.Metric == "sleep_awake":
			if !s.End.After(nightStart) || !s.Start.Before(nightEnd) {
				continue
			}
			explicitWake = append(explicitWake, sleepSegment{Start: s.Start, End: s.End})
		}
	}
	if asleepHours <= 0 {
		return missingArchitectureDay(date, SleepArchitectureReasonMissingSegments)
	}
	if stagedHours <= 0 {
		day := missingArchitectureDay(date, SleepArchitectureReasonCoarseOnly)
		day.Source = source
		day.AsleepHours = asleepHours
		return day
	}

	mergedAsleep := mergeSegments(asleep, 0)
	mergedWake := mergeSegments(explicitWake, 0)
	wasoHours, explicitBouts, longestWake := wakeStats(mergedWake)
	gapBouts := countInferredGaps(mergedAsleep)
	frag := float64(explicitBouts+gapBouts) / asleepHours
	return SleepArchitectureDay{
		Date:                 date,
		Source:               source,
		AsleepHours:          asleepHours,
		WASOHours:            wasoHours,
		ExplicitWakeBouts:    explicitBouts,
		GapInferredWakeBouts: gapBouts,
		LongestWakeBoutHours: longestWake,
		FragmentationIndex:   frag,
		Eligible:             true,
		Reason:               SleepArchitectureReasonOK,
		Confidence:           SleepArchitectureConfidenceHigh,
	}
}

func architectureNightAsleepSegments(date string, segs []architectureSegment, preferred string) ([]architectureSegment, string) {
	if len(segs) == 0 {
		return nil, ""
	}
	loc := segs[0].End.Location()
	refMidnight, err := time.ParseInLocation(isoDate, date, loc)
	if err != nil {
		return nil, ""
	}
	windowStart := refMidnight.Add(-midnightWindow)
	windowEnd := refMidnight.Add(12 * time.Hour)
	bySource := map[string][]architectureSegment{}
	sourceHours := map[string]float64{}
	for _, s := range segs {
		if !isArchitectureAsleepMetric(s.Metric) {
			continue
		}
		if !s.End.After(windowStart) || !s.Start.Before(windowEnd) {
			continue
		}
		bySource[s.Source] = append(bySource[s.Source], s)
		sourceHours[s.Source] += s.End.Sub(s.Start).Hours()
	}

	source, _ := chooseSleepSource(sourceHours, true, preferred)
	if source == "" {
		return nil, ""
	}
	return append([]architectureSegment(nil), bySource[source]...), source
}

func architectureSegmentInList(target architectureSegment, list []architectureSegment) bool {
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}

func missingArchitectureDay(date, reason string) SleepArchitectureDay {
	return SleepArchitectureDay{
		Date:       date,
		Reason:     reason,
		Confidence: SleepArchitectureConfidenceUnavailable,
	}
}

func isArchitectureAsleepMetric(metric string) bool {
	switch metric {
	case "sleep_deep", "sleep_rem", "sleep_core", "sleep_unspecified":
		return true
	default:
		return false
	}
}

func wakeStats(segs []sleepSegment) (hours float64, bouts int, longest float64) {
	for _, s := range segs {
		h := s.End.Sub(s.Start).Hours()
		hours += h
		bouts++
		if h > longest {
			longest = h
		}
	}
	return hours, bouts, longest
}

func countInferredGaps(asleep []sleepSegment) int {
	if len(asleep) < 2 {
		return 0
	}
	sort.Slice(asleep, func(i, j int) bool { return asleep[i].Start.Before(asleep[j].Start) })
	var out int
	prev := asleep[0]
	for i := 1; i < len(asleep); i++ {
		cur := asleep[i]
		if cur.Start.Sub(prev.End) > mergeTolerance {
			out++
		}
		if cur.End.After(prev.End) {
			prev = cur
		}
	}
	return out
}

// BuildSleepArchitectureWindow summarizes architecture over the
// inclusive trailing window [endDate-days+1, endDate].
func BuildSleepArchitectureWindow(endDate time.Time, days int, byDate map[string]SleepArchitectureDay) SleepArchitectureWindow {
	if days <= 0 {
		days = 1
	}
	out := SleepArchitectureWindow{
		Days:                days,
		MissingReasonCounts: map[string]int{},
		Confidence:          SleepArchitectureConfidenceUnavailable,
	}
	var waso, explicit, gaps, frag, longest float64
	for i := days - 1; i >= 0; i-- {
		date := endDate.AddDate(0, 0, -i).Format(isoDate)
		day, ok := byDate[date]
		if !ok {
			out.MissingReasonCounts[SleepArchitectureReasonMissingSegments]++
			continue
		}
		if day.Source != "" {
			out.SourceDays++
		}
		if !day.Eligible {
			reason := day.Reason
			if reason == "" {
				reason = SleepArchitectureReasonMissingSegments
			}
			out.MissingReasonCounts[reason]++
			continue
		}
		out.EligibleDays++
		waso += day.WASOHours
		explicit += float64(day.ExplicitWakeBouts)
		gaps += float64(day.GapInferredWakeBouts)
		frag += day.FragmentationIndex
		if day.LongestWakeBoutHours > longest {
			longest = day.LongestWakeBoutHours
		}
	}
	if out.EligibleDays == 0 {
		return out
	}
	n := float64(out.EligibleDays)
	out.Confidence = SleepArchitectureConfidenceHigh
	if out.EligibleDays < days {
		out.Confidence = "partial"
	}
	out.WASOHours = ptrFloat(waso / n)
	out.ExplicitWakeBouts = ptrFloat(explicit / n)
	out.GapInferredWakeBouts = ptrFloat(gaps / n)
	out.FragmentationIndex = ptrFloat(frag / n)
	out.LongestWakeBoutHours = ptrFloat(longest)
	return out
}

func BuildSleepArchitectureFeatureFields(t time.Time, byDate map[string]SleepArchitectureDay) SleepArchitectureFeatureFields {
	w7 := BuildSleepArchitectureWindow(t, 7, byDate)
	w14 := BuildSleepArchitectureWindow(t, 14, byDate)
	return architectureFields(t.Format(isoDate), w7, w14)
}
