package storage

import (
	"fmt"
	"sort"
	"time"
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
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT date, metric_name, source, qty
		  FROM metric_points
		 WHERE metric_name IN ('sleep_deep','sleep_rem','sleep_core','sleep_unspecified','sleep_awake')
		   AND qty > 0
		   AND quality = 'ok'
		   AND SUBSTRING(date, 12, 8) != '00:00:00'
		   AND SUBSTRING(date, 1, 10) BETWEEN $1 AND $2
		 ORDER BY date ASC
	`, queryFrom, to)
	if err != nil {
		return nil, fmt.Errorf("LoadSleepArchitectureDays: %w", err)
	}
	defer rows.Close()

	raw := []SleepArchitectureRawSegment{}
	for rows.Next() {
		var r SleepArchitectureRawSegment
		if err := rows.Scan(&r.Date, &r.MetricName, &r.Source, &r.Hours); err != nil {
			return nil, fmt.Errorf("LoadSleepArchitectureDays scan: %w", err)
		}
		raw = append(raw, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	all := ComputeSleepArchitectureDays(raw)
	out := map[string]SleepArchitectureDay{}
	for date, day := range all {
		if date >= from && date <= to {
			out[date] = day
		}
	}
	return out, nil
}

// ComputeSleepArchitectureDays computes per-end-date sleep architecture
// from raw metric segments. It picks one source per date before
// deriving architecture features.
func ComputeSleepArchitectureDays(raw []SleepArchitectureRawSegment) map[string]SleepArchitectureDay {
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
		out[date] = computeSleepArchitectureDay(date, segs)
	}
	return out
}

func computeSleepArchitectureDay(date string, segs []architectureSegment) SleepArchitectureDay {
	if len(segs) == 0 {
		return missingArchitectureDay(date, SleepArchitectureReasonMissingSegments)
	}

	nightAsleep, source := architectureNightAsleepSegments(date, segs)
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

func architectureNightAsleepSegments(date string, segs []architectureSegment) ([]architectureSegment, string) {
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

	source := pickWinningSource(sourceHours)
	if source == "" {
		return nil, ""
	}
	out := make([]architectureSegment, 0, len(bySource[source]))
	for _, s := range bySource[source] {
		out = append(out, s)
	}
	return out, source
}

func architectureSegmentInList(target architectureSegment, list []architectureSegment) bool {
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
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
	return SleepArchitectureFeatureFields{
		ArchitectureAvailableThrough: t.Format(isoDate),
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
