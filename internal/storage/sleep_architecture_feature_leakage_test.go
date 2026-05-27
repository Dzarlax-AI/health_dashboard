package storage

import (
	"reflect"
	"testing"

	"health-receiver/internal/health"
)

func architectureDayForTest(date string) SleepArchitectureDay {
	return SleepArchitectureDay{
		Date:                 date,
		Source:               "Apple Watch",
		AsleepHours:          7,
		WASOHours:            0.25,
		ExplicitWakeBouts:    1,
		GapInferredWakeBouts: 0,
		LongestWakeBoutHours: 0.25,
		FragmentationIndex:   1.0 / 7.0,
		Eligible:             true,
		Reason:               SleepArchitectureReasonOK,
		Confidence:           SleepArchitectureConfidenceHigh,
	}
}

func TestRecoveryFeatures_DoNotReadFutureArchitectureSegments(t *testing.T) {
	date := mustParseDateForTest(t, "2026-05-10")
	withoutFuture := map[string]SleepArchitectureDay{
		"2026-05-10": architectureDayForTest("2026-05-10"),
	}
	withFuture := map[string]SleepArchitectureDay{
		"2026-05-10": architectureDayForTest("2026-05-10"),
		"2026-05-11": {
			Date:                 "2026-05-11",
			Source:               "Apple Watch",
			AsleepHours:          7,
			WASOHours:            3,
			ExplicitWakeBouts:    9,
			GapInferredWakeBouts: 5,
			LongestWakeBoutHours: 1,
			FragmentationIndex:   2,
			Eligible:             true,
			Reason:               SleepArchitectureReasonOK,
			Confidence:           SleepArchitectureConfidenceHigh,
		},
	}

	a := buildRecoveryFeatures(date, "", map[string]health.SleepRow{}, map[string]health.SleepEfficiencyResult{}, map[string]health.SleepCaptureConfidenceResult{}, withoutFuture)
	b := buildRecoveryFeatures(date, "", map[string]health.SleepRow{}, map[string]health.SleepEfficiencyResult{}, map[string]health.SleepCaptureConfidenceResult{}, withFuture)
	if !reflect.DeepEqual(a.SleepArchitectureFeatureFields, b.SleepArchitectureFeatureFields) {
		t.Fatalf("Recovery features for t changed after adding t+1 architecture:\nwithout=%+v\nwith=%+v",
			a.SleepArchitectureFeatureFields, b.SleepArchitectureFeatureFields)
	}
}

func TestChronicFeatures_DoNotReadFutureArchitectureSegments(t *testing.T) {
	date := mustParseDateForTest(t, "2026-05-10")
	withoutFuture := map[string]SleepArchitectureDay{
		"2026-05-10": architectureDayForTest("2026-05-10"),
	}
	withFuture := map[string]SleepArchitectureDay{
		"2026-05-10": architectureDayForTest("2026-05-10"),
		"2026-05-20": {
			Date:                 "2026-05-20",
			Source:               "Apple Watch",
			AsleepHours:          7,
			WASOHours:            4,
			ExplicitWakeBouts:    12,
			GapInferredWakeBouts: 7,
			LongestWakeBoutHours: 2,
			FragmentationIndex:   3,
			Eligible:             true,
			Reason:               SleepArchitectureReasonOK,
			Confidence:           SleepArchitectureConfidenceHigh,
		},
	}
	lookup := func(string) (*float64, bool) { return nil, false }

	a := buildChronicLoadFeatures(date, "", map[string]recoveryRolling3dRow{}, lookup, 0, false, withoutFuture)
	b := buildChronicLoadFeatures(date, "", map[string]recoveryRolling3dRow{}, lookup, 0, false, withFuture)
	if !reflect.DeepEqual(a.SleepArchitectureFeatureFields, b.SleepArchitectureFeatureFields) {
		t.Fatalf("Chronic features for t changed after adding future-window architecture:\nwithout=%+v\nwith=%+v",
			a.SleepArchitectureFeatureFields, b.SleepArchitectureFeatureFields)
	}
}
