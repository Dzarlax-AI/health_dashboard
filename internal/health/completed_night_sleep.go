package health

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// CompletedNightSleep is the only input accepted by the definitive sleep
// claim. It is intentionally distinct from daily_scores.sleep_total: a display
// aggregate does not prove that a night was fully captured.
type CompletedNightSleep struct {
	WakeDate             string
	DurationHours        float64
	Source               string
	SourceEpoch          string
	InputHash            string
	CaptureCompleteness  string
	DurationAssessment   string
	FinalizationState    string
	ClaimEligibility     string
	ObservedAt           time.Time
	FinalizedAt          *time.Time
	AlgorithmVersion     string
	CoverageGeneration   string
	CoveredIntervalStart *time.Time
	CoveredIntervalEnd   *time.Time
}

const (
	NightCaptureComplete = "complete"
	NightCapturePartial  = "partial"
	NightCaptureUnknown  = "unknown"

	NightDurationPlausible = "plausible"
	NightDurationOutlier   = "outlier"
	NightDurationUnknown   = "unknown"

	NightFinalProvisional = "provisional"
	NightFinalFinal       = "final"

	NightClaimEligible   = "eligible"
	NightClaimIneligible = "ineligible"

	RecentSleepClaimTrue        = "true"
	RecentSleepClaimFalse       = "false"
	RecentSleepClaimUnknown     = "unknown"
	RecentSleepClaimProvisional = "provisional"
)

// CanonicalNightInput is emitted by the controlled sync adapter. A coverage
// generation is deliberately required for `complete`: Apple Health points by
// themselves are not an atomic "night closed" signal.
type CanonicalNightInput struct {
	WakeDate             string
	DurationHours        float64
	Source               string
	SourceEpoch          string
	InputHash            string
	CaptureCompleteness  string
	CoverageGeneration   string
	CoveredIntervalStart *time.Time
	CoveredIntervalEnd   *time.Time
	ObservedAt           time.Time
	AlgorithmVersion     string
}

// CanonicalizeCompletedNight keeps capture completeness, plausibility, and
// finalization independent. In particular, a complete but implausible duration
// remains an outlier fact rather than becoming a misleading partial record.
func CanonicalizeCompletedNight(in CanonicalNightInput, now time.Time, loc *time.Location) (CompletedNightSleep, error) {
	if loc == nil {
		return CompletedNightSleep{}, fmt.Errorf("night canonicalization requires a tenant timezone")
	}
	if _, err := time.ParseInLocation("2006-01-02", in.WakeDate, loc); err != nil {
		return CompletedNightSleep{}, fmt.Errorf("invalid wake date: %w", err)
	}
	if in.Source == "" || in.SourceEpoch == "" || in.InputHash == "" || in.AlgorithmVersion == "" {
		return CompletedNightSleep{}, fmt.Errorf("night canonicalization requires source, epoch, input hash, and algorithm version")
	}
	if in.ObservedAt.IsZero() {
		return CompletedNightSleep{}, fmt.Errorf("night canonicalization requires observed time")
	}

	capture := in.CaptureCompleteness
	switch capture {
	case NightCaptureComplete:
		if in.CoverageGeneration == "" || in.CoveredIntervalStart == nil || in.CoveredIntervalEnd == nil || !in.CoveredIntervalEnd.After(*in.CoveredIntervalStart) {
			return CompletedNightSleep{}, fmt.Errorf("complete night requires a valid coverage commitment")
		}
	case NightCapturePartial:
	case NightCaptureUnknown, "":
		capture = NightCaptureUnknown
	default:
		return CompletedNightSleep{}, fmt.Errorf("invalid capture completeness %q", capture)
	}

	assessment := NightDurationUnknown
	if in.DurationHours > 0 {
		assessment = NightDurationPlausible
		if in.DurationHours < 3 || in.DurationHours > 14 {
			assessment = NightDurationOutlier
		}
	}
	state := NightFinalProvisional
	finalizedAt := (*time.Time)(nil)
	finalAt, err := nightFinalizationTime(in.WakeDate, loc)
	if err != nil {
		return CompletedNightSleep{}, err
	}
	if !now.Before(finalAt) {
		state = NightFinalFinal
		finalized := finalAt
		finalizedAt = &finalized
	}
	eligibility := NightClaimIneligible
	if capture == NightCaptureComplete && assessment == NightDurationPlausible && state == NightFinalFinal {
		eligibility = NightClaimEligible
	}
	return CompletedNightSleep{
		WakeDate: in.WakeDate, DurationHours: in.DurationHours, Source: in.Source, SourceEpoch: in.SourceEpoch,
		InputHash: in.InputHash, CaptureCompleteness: capture, DurationAssessment: assessment,
		FinalizationState: state, ClaimEligibility: eligibility, ObservedAt: in.ObservedAt,
		FinalizedAt: finalizedAt, AlgorithmVersion: in.AlgorithmVersion, CoverageGeneration: in.CoverageGeneration,
		CoveredIntervalStart: in.CoveredIntervalStart, CoveredIntervalEnd: in.CoveredIntervalEnd,
	}, nil
}

func nightFinalizationTime(wakeDate string, loc *time.Location) (time.Time, error) {
	date, err := time.ParseInLocation("2006-01-02", wakeDate, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse wake date: %w", err)
	}
	return time.Date(date.Year(), date.Month(), date.Day(), 18, 0, 0, 0, loc), nil
}

func (n CompletedNightSleep) IsProvisionalObservationEligible() bool {
	return n.CaptureCompleteness == NightCaptureComplete &&
		n.DurationAssessment == NightDurationPlausible &&
		n.FinalizationState == NightFinalProvisional
}

func (n CompletedNightSleep) IsDefinitiveClaimEligible() bool {
	return n.ClaimEligibility == NightClaimEligible &&
		n.CaptureCompleteness == NightCaptureComplete &&
		n.DurationAssessment == NightDurationPlausible &&
		n.FinalizationState == NightFinalFinal
}

// RecentSleepBelowReference is a descriptive claim, never a sleep-debt or
// biological-need estimate. Unknown input remains unknown rather than zero.
type RecentSleepBelowReference struct {
	State            string
	Reason           string
	ReferenceHours   float64
	CurrentShortDays int
	ActionEvent      bool
	EvidenceDigest   string
}

// EvaluateRecentSleepBelowReference implements the closed B0 policy. The
// reference window is D-93..D-4 (90 dates); the current window is D-3..D.
// Unknown previous cadence dates never block a current evening action.
func EvaluateRecentSleepBelowReference(records []CompletedNightSleep, wakeDate string, now time.Time, loc *time.Location) RecentSleepBelowReference {
	finalize := func(result RecentSleepBelowReference) RecentSleepBelowReference {
		result.EvidenceDigest = recentSleepEvidenceDigest(records, wakeDate)
		return result
	}
	if loc == nil {
		return finalize(RecentSleepBelowReference{State: RecentSleepClaimUnknown, Reason: "invalid_timezone"})
	}
	date, err := time.ParseInLocation("2006-01-02", wakeDate, loc)
	if err != nil {
		return finalize(RecentSleepBelowReference{State: RecentSleepClaimUnknown, Reason: "invalid_wake_date"})
	}
	byDate := make(map[string]CompletedNightSleep, len(records))
	for _, record := range records {
		if record.WakeDate != "" {
			byDate[record.WakeDate] = record
		}
	}

	current := make([]CompletedNightSleep, 0, 4)
	for offset := -3; offset <= 0; offset++ {
		key := date.AddDate(0, 0, offset).Format("2006-01-02")
		record, ok := byDate[key]
		if !ok {
			return finalize(RecentSleepBelowReference{State: RecentSleepClaimUnknown, Reason: "current_night_missing"})
		}
		if offset == 0 && record.IsProvisionalObservationEligible() {
			return finalize(RecentSleepBelowReference{State: RecentSleepClaimProvisional, Reason: "current_night_provisional"})
		}
		if !record.IsDefinitiveClaimEligible() {
			return finalize(RecentSleepBelowReference{State: RecentSleepClaimUnknown, Reason: "current_night_ineligible"})
		}
		current = append(current, record)
	}

	epoch := current[0].SourceEpoch
	algorithm := current[0].AlgorithmVersion
	for _, record := range current[1:] {
		if record.SourceEpoch != epoch || record.AlgorithmVersion != algorithm {
			return finalize(RecentSleepBelowReference{State: RecentSleepClaimUnknown, Reason: "current_epoch_mismatch"})
		}
	}

	baseline := make([]float64, 0, 90)
	for offset := -93; offset <= -4; offset++ {
		record, ok := byDate[date.AddDate(0, 0, offset).Format("2006-01-02")]
		if !ok || !record.IsDefinitiveClaimEligible() {
			continue
		}
		if record.SourceEpoch != epoch || record.AlgorithmVersion != algorithm {
			continue
		}
		baseline = append(baseline, record.DurationHours)
	}
	if len(baseline) < 60 {
		return finalize(RecentSleepBelowReference{State: RecentSleepClaimUnknown, Reason: "reference_history_insufficient"})
	}
	sort.Float64s(baseline)
	middle := len(baseline) / 2
	reference := baseline[middle]
	if len(baseline)%2 == 0 {
		reference = (baseline[middle-1] + baseline[middle]) / 2
	}
	short := 0
	for _, record := range current {
		if record.DurationHours <= reference-0.5 {
			short++
		}
	}
	state := RecentSleepClaimFalse
	if short >= 3 {
		state = RecentSleepClaimTrue
	}
	result := RecentSleepBelowReference{State: state, ReferenceHours: reference, CurrentShortDays: short}
	if state != RecentSleepClaimTrue || now.In(loc).Before(mustNightFinalizationTime(wakeDate, loc)) {
		return finalize(result)
	}
	for offset := -7; offset <= -1; offset++ {
		prior := date.AddDate(0, 0, offset).Format("2006-01-02")
		priorResult := EvaluateRecentSleepBelowReference(records, prior, now, loc)
		if priorResult.State == RecentSleepClaimTrue {
			return finalize(result)
		}
	}
	result.ActionEvent = true
	return finalize(result)
}

func recentSleepEvidenceDigest(records []CompletedNightSleep, wakeDate string) string {
	ordered := append([]CompletedNightSleep(nil), records...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].WakeDate < ordered[j].WakeDate })
	parts := []string{wakeDate}
	for _, record := range ordered {
		parts = append(parts, fmt.Sprintf("%s|%.9g|%s|%s|%s|%s|%s|%s|%s|%s", record.WakeDate,
			record.DurationHours, record.Source, record.SourceEpoch, record.InputHash,
			record.CaptureCompleteness, record.DurationAssessment, record.FinalizationState,
			record.ClaimEligibility, record.AlgorithmVersion))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(sum[:])
}

func mustNightFinalizationTime(wakeDate string, loc *time.Location) time.Time {
	result, err := nightFinalizationTime(wakeDate, loc)
	if err != nil {
		return time.Time{}
	}
	return result
}
