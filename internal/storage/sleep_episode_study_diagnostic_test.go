package storage

import (
	"testing"
	"time"

	"health-receiver/internal/sleepstudy"
	"health-receiver/internal/sleepwire"
)

func TestStudyRejectionIdentifiesIsolatedBlockingGate(t *testing.T) {
	wake := time.Date(2026, 1, 30, 7, 0, 0, 0, time.UTC)
	window := &sleepstudy.Window{Start: wake.Add(-9 * time.Hour), End: wake.Add(time.Hour), MinAsleep: 4 * time.Hour}
	night := []sleepstudy.Span{{Start: wake.Add(-7 * time.Hour), End: wake}}
	away := []sleepstudy.Span{{Start: wake.Add(3 * time.Hour), End: wake.Add(10 * time.Hour)}}
	short := []sleepstudy.Span{{Start: wake.Add(-time.Hour), End: wake}}
	for _, test := range []struct {
		want                 string
		spans                []sleepstudy.Span
		covered, gap, coarse bool
	}{
		{"asleep_gap_only", night, true, true, false},
		{"coverage_only", night, false, false, false},
		{"coarse_extension_only", night, true, false, true},
		{"combined_candidate_gates", night, false, true, false},
		{"enough_duration_but_outside_window", away, true, false, false},
		{"insufficient_duration_even_without_window", short, true, false, false},
	} {
		t.Run(test.want, func(t *testing.T) {
			day := &studyDay{candidates: []studyCandidate{{episode: sleepstudy.Episode{Asleep: test.spans}, profileCovered: test.covered, sleepBridge: test.gap, coarseEdge: test.coarse}}}
			if got := studyRejection(day, window); got != test.want {
				t.Fatalf("rejection = %q, want %q", got, test.want)
			}
		})
	}
}

func TestStudyOrdinaryReferenceKeepsAbstentionSeparate(t *testing.T) {
	out := studySummary{Counters: map[string]int{}}
	reference := studyDurationReference{hours: 8, final: true}
	hours := 8.0
	out.evaluateOrdinary(reference, &LocalShadowSleep{Total: &hours})
	out.evaluateOrdinary(reference, &LocalShadowSleep{})
	hours = 7
	out.evaluateOrdinary(reference, &LocalShadowSleep{Total: &hours})
	for _, prefix := range []string{"ordinary", "ordinary_final_corroborated"} {
		for key, want := range map[string]int{"reference_dates": 3, "selected": 2, "abstained": 1, "duration_agrees_1s": 1, "duration_differs_over_1s": 1} {
			if got := out.Counters[prefix+"_"+key]; got != want {
				t.Fatalf("%s %s = %d, want %d", prefix, key, got, want)
			}
		}
	}
}

func TestStudyRecordedWakeGapsRequireObservation(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	left, right := start.Add(2*time.Hour), start.Add(3*time.Hour)
	asleep := []sleepstudy.Span{{Start: start, End: left}, {Start: right, End: right.Add(4 * time.Hour)}}
	for _, tc := range []struct {
		minutes int
		want    bool
	}{{0, false}, {53, false}, {54, true}, {60, true}} {
		awake := []sleepstudy.Span{{Start: left, End: left.Add(time.Duration(tc.minutes) * time.Minute)}}
		if got := studyHasRecordedWakeGaps(asleep, awake, sleepstudy.Span{Start: start, End: start.Add(8 * time.Hour)}); got != tc.want {
			t.Fatalf("%d awake minutes: got %v, want %v", tc.minutes, got, tc.want)
		}
	}
}

func TestStudyRecordedWakeSelectionMatrix(t *testing.T) {
	wake := time.Date(2026, 4, 7, 7, 0, 0, 0, time.UTC)
	window := &sleepstudy.Window{Start: wake.Add(-12 * time.Hour), End: wake.Add(time.Hour), MinAsleep: 4 * time.Hour}
	for _, tc := range []struct {
		name, stage           string
		gap, recorded         time.Duration
		covered, coarse, want bool
	}{
		{"observed_hour", "awake", time.Hour, time.Hour, true, false, true},
		{"at_two_hour_limit", "awake", 2 * time.Hour, 2 * time.Hour, true, false, true},
		{"over_two_hour_limit", "awake", 2*time.Hour + time.Second, 2*time.Hour + time.Second, true, false, false},
		{"separate_sleep_blocks", "awake", 4 * time.Hour, 4 * time.Hour, true, false, false},
		{"no_reading", "awake", time.Hour, 0, true, false, false},
		{"time_in_bed_is_not_awake", "in_bed", time.Hour, time.Hour, true, false, false},
		{"insufficient_observation", "awake", time.Hour, 53 * time.Minute, true, false, false},
		{"missing_coverage", "awake", time.Hour, time.Hour, false, false, false},
		{"coarse_extension", "awake", time.Hour, time.Hour, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			left, right := wake.Add(-3*time.Hour-tc.gap), wake.Add(-3*time.Hour)
			first, last := left.Add(-3*time.Hour), wake
			spans := []sleepstudy.Span{{Start: first, End: left}, {Start: right, End: last}}
			stages := []sleepwire.Stage{
				{Kind: "core", Start: first, End: left},
				{Kind: "rem", Start: right, End: last},
				{Kind: tc.stage, Start: left, End: left.Add(tc.recorded)},
			}
			input := studyCandidate{candidate: sleepwire.Candidate{Stages: stages}, episode: sleepstudy.Episode{ID: "night", Asleep: spans}, profileCovered: tc.covered, coarseEdge: tc.coarse, sleepBridge: true}
			day := &studyDay{candidates: []studyCandidate{input}}
			selected := studyRecordedWakeOnly(day, window)
			if (selected.Main == "night") != tc.want {
				t.Fatalf("selected %+v", selected)
			}
			if tc.want {
				block := studySleepBlock(input)
				if block.Total == nil || *block.Total != 6 || block.Awake == nil || *block.Awake != tc.gap.Hours() {
					t.Fatalf("duration and wake should remain separate: %+v", block)
				}
			}
		})
	}
}

func TestStudyRecordedWakeGapCannotStraddleWindow(t *testing.T) {
	wake := time.Date(2026, 4, 7, 7, 0, 0, 0, time.UTC)
	left, right := wake.Add(-4*time.Hour), wake.Add(-3*time.Hour)
	asleep := []sleepstudy.Span{{Start: left.Add(-time.Hour), End: left}, {Start: right, End: wake}}
	awake := []sleepstudy.Span{{Start: left, End: right}}
	bound := sleepstudy.Span{Start: left.Add(time.Second), End: wake}
	if studyHasRecordedWakeGaps(asleep, awake, bound) {
		t.Fatal("gap crossed selection window")
	}
}

func TestStudyRecordedWakeLimitsCumulativeLongGaps(t *testing.T) {
	start := time.Date(2026, 4, 7, 0, 0, 0, 0, time.UTC)
	bound := sleepstudy.Span{Start: start, End: start.Add(11 * time.Hour)}
	for _, tc := range []struct {
		secondGap time.Duration
		want      bool
	}{{time.Hour, true}, {time.Hour + time.Second, false}, {2 * time.Hour, false}} {
		left := start.Add(2 * time.Hour)
		right := left.Add(time.Hour)
		nextLeft := right.Add(2 * time.Hour)
		nextRight := nextLeft.Add(tc.secondGap)
		asleep := []sleepstudy.Span{{Start: start, End: left}, {Start: right, End: nextLeft}, {Start: nextRight, End: nextRight.Add(2 * time.Hour)}}
		awake := []sleepstudy.Span{{Start: left, End: right}, {Start: nextLeft, End: nextRight}}
		if got := studyHasRecordedWakeGaps(asleep, awake, bound); got != tc.want {
			t.Fatalf("second gap %v: got %v, want %v", tc.secondGap, got, tc.want)
		}
	}
}
