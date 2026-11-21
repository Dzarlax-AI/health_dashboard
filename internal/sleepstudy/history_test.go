package sleepstudy

import (
	"testing"
	"time"
)

func TestHistoricalWindowAcrossZonesAndShift(t *testing.T) {
	for _, c := range []struct {
		zone string
		wake int
	}{{"Europe/Belgrade", 7}, {"America/New_York", 16}} {
		loc, err := time.LoadLocation(c.zone)
		if err != nil {
			t.Fatal(err)
		}
		target := time.Date(2026, 11, 20, c.wake, 0, 0, 0, loc)
		var history []PriorDay
		for i := 1; i <= 8; i++ {
			end := target.AddDate(0, 0, -i)
			history = append(history, PriorDay{Date: end.Format(time.DateOnly), Zone: c.zone, AvailableAt: end, Episodes: []Episode{{ID: "night", Covered: true, Asleep: []Span{{end.Add(-8 * time.Hour), end}}}}})
		}
		w := HistoricalWindow(target.Format(time.DateOnly), c.zone, &target, history)
		if w == nil || w.MinAsleep != 4*time.Hour || !w.End.Equal(target.Add(3*time.Hour)) {
			t.Fatalf("%s window = %+v", c.zone, w)
		}
		cutoff := target.AddDate(0, 0, -4)
		if w := HistoricalWindow(target.Format(time.DateOnly), c.zone, &cutoff, history); w != nil {
			t.Fatal("used future observations")
		}
	}
}

func TestCoarseExtensionAndAccounting(t *testing.T) {
	start := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	stages := []Stage{{"core", Span{start.Add(time.Hour), start.Add(3 * time.Hour)}}, {"asleep_unspecified", Span{start, start.Add(3 * time.Hour)}}}
	asleep, ambiguous := StagedCoverage(stages, 30*time.Minute)
	if !ambiguous || Duration(asleep) != 3*time.Hour {
		t.Fatalf("coarse edge not retained as uncertain: %v", ambiguous)
	}
	if _, ambiguous = StagedCoverage(stages[1:], 30*time.Minute); ambiguous {
		t.Fatal("coarse-only source should retain measured sleep")
	}
	if got := AccountedDuration([][]Span{asleep, asleep}, Span{start.Add(time.Hour), start.Add(4 * time.Hour)}); got != 2*time.Hour {
		t.Fatalf("accounted %s", got)
	}
}

func TestSeparateNapInsideWideWakeWindow(t *testing.T) {
	wake := time.Date(2026, 8, 30, 7, 0, 0, 0, time.UTC)
	napStart := wake.Add(93 * time.Minute)
	night := Episode{ID: "night", Covered: true, Asleep: []Span{{wake.Add(-332 * time.Minute), wake}}}
	nap := Episode{ID: "nap", Covered: true, Asleep: []Span{{napStart, napStart.Add(136 * time.Minute)}}}
	window := Window{Start: wake.Add(-9 * time.Hour), End: wake.Add(3 * time.Hour), MinAsleep: 4 * time.Hour}
	selected := Select([]Episode{nap, night}, &window, "")
	if selected.Main != "night" || len(selected.Additional) != 1 || selected.Additional[0] != "nap" {
		t.Fatalf("selection = %+v", selected)
	}
}

func TestLongOutOfWindowEpisodeStaysUncertain(t *testing.T) {
	wake := time.Date(2026, 8, 30, 7, 0, 0, 0, time.UTC)
	night := Episode{ID: "night", Covered: true, Asleep: []Span{{wake.Add(-8 * time.Hour), wake}}}
	later := Episode{ID: "later", Covered: true, Asleep: []Span{{wake.Add(4 * time.Hour), wake.Add(11 * time.Hour)}}}
	window := Window{Start: wake.Add(-10 * time.Hour), End: wake.Add(3 * time.Hour), MinAsleep: 4 * time.Hour}
	selected := Select([]Episode{later, night}, &window, "")
	if selected.Main != "night" || len(selected.Additional) != 0 || len(selected.Uncertain) != 1 || selected.Uncertain[0] != "later" {
		t.Fatalf("selection = %+v", selected)
	}
}

func TestHistoricalWindowAbstainsOnNonUniqueLocalWake(t *testing.T) {
	cases := []struct {
		name, zone, day string
		hour, minute    int
	}{
		{"Belgrade spring gap", "Europe/Belgrade", "2026-03-29", 2, 30},
		{"Belgrade fall fold", "Europe/Belgrade", "2026-10-25", 2, 30},
		{"New York spring gap", "America/New_York", "2026-03-08", 2, 30},
		{"New York fall fold", "America/New_York", "2026-11-01", 1, 30},
		{"Lord Howe half-hour gap", "Australia/Lord_Howe", "2026-10-04", 2, 15},
		{"Lord Howe half-hour fold", "Australia/Lord_Howe", "2026-04-05", 1, 45},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loc, err := time.LoadLocation(tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			day, err := time.Parse(time.DateOnly, tc.day)
			if err != nil {
				t.Fatal(err)
			}
			var history []PriorDay
			for i := 1; i <= 8; i++ {
				prior := day.AddDate(0, 0, -i)
				end := time.Date(prior.Year(), prior.Month(), prior.Day(), tc.hour, tc.minute, 0, 0, loc)
				history = append(history, PriorDay{Date: prior.Format(time.DateOnly), Zone: tc.zone, Episodes: []Episode{{ID: "night", Covered: true, Asleep: []Span{{Start: end.Add(-7 * time.Hour), End: end}}}}})
			}
			if window := HistoricalWindow(tc.day, tc.zone, nil, history); window != nil {
				t.Fatalf("ambiguous wall time produced window: %+v", window)
			}
			control := day.AddDate(0, 0, 1).Format(time.DateOnly)
			if window := HistoricalWindow(control, tc.zone, nil, history); window == nil {
				t.Fatal("adjacent unambiguous day lost its window")
			}
		})
	}
}
