package sleepstudy

import (
	"reflect"
	"testing"
	"time"
)

// These fictional scenarios are labelled from their descriptions before invoking
// the selector. They are not sampled from a user's sleep history.
func TestFictionalHoldout(t *testing.T) {
	target := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	episode := func(id, source string, priority int, start, end time.Time, covered bool) Episode {
		return Episode{ID: id, Source: source, Priority: priority, Covered: covered, Asleep: []Span{{Start: start, End: end}}}
	}
	historical := func(zone string, wakeHour int) []PriorDay {
		var days []PriorDay
		for i := 1; i <= 8; i++ {
			wake := target.AddDate(0, 0, -i).Add(time.Duration(wakeHour) * time.Hour)
			days = append(days, PriorDay{Date: wake.Format(time.DateOnly), Zone: zone, AvailableAt: wake, Episodes: []Episode{episode("prior", "fictional", 1, wake.Add(-8*time.Hour), wake, true)}})
		}
		return days
	}
	day := target.Format(time.DateOnly)
	t.Run("shift worker daytime sleep and short later episode", func(t *testing.T) {
		// Expected: the regular daytime sleep is main; the later short sleep is additional.
		wantMain, wantAdditional := "day", []string{"short"}
		w := HistoricalWindow(day, "UTC", nil, historical("UTC", 16))
		if w == nil {
			t.Fatal("expected historical daytime window")
		}
		got := Select([]Episode{episode("day", "a", 1, target.Add(8*time.Hour), target.Add(16*time.Hour), true), episode("short", "a", 1, target.Add(20*time.Hour), target.Add(21*time.Hour), true)}, w, "")
		if got.Main != wantMain || !reflect.DeepEqual(got.Additional, wantAdditional) || len(got.Uncertain) != 0 {
			t.Errorf("got %+v; expected main %q and additional %v", got, wantMain, wantAdditional)
		}
	})
	t.Run("two comparable separate sleeps", func(t *testing.T) {
		// Expected: no reliable main; both episodes remain uncertain.
		wantReason, wantUncertain := "ambiguous_overlap", map[string]bool{"first": true, "second": true}
		w := &Window{Start: target, End: target.Add(24 * time.Hour), MinAsleep: 2 * time.Hour}
		got := Select([]Episode{episode("first", "a", 1, target.Add(time.Hour), target.Add(5*time.Hour), true), episode("second", "a", 1, target.Add(12*time.Hour), target.Add(16*time.Hour), true)}, w, "")
		found := map[string]bool{}
		for _, id := range got.Uncertain {
			found[id] = true
		}
		if got.Main != "" || got.Reason != wantReason || !reflect.DeepEqual(found, wantUncertain) {
			t.Errorf("got %+v; expected both uncertain", got)
		}
	})
	t.Run("short priority watch versus full ring", func(t *testing.T) {
		// Expected: full ring is main; overlapping shorter watch is uncertain.
		wantMain, wantUncertain := "ring", []string{"watch"}
		w := &Window{Start: target, End: target.Add(10 * time.Hour), MinAsleep: 4 * time.Hour}
		got := Select([]Episode{episode("watch", "watch", 1, target.Add(2*time.Hour), target.Add(5*time.Hour), true), episode("ring", "ring", 2, target.Add(time.Hour), target.Add(9*time.Hour), true)}, w, "")
		if got.Main != wantMain || !reflect.DeepEqual(got.Uncertain, wantUncertain) {
			t.Errorf("got %+v; expected main %q and uncertain %v", got, wantMain, wantUncertain)
		}
	})
	t.Run("same sleep observed by two sources", func(t *testing.T) {
		// Expected: one main observation, no additional or uncertain duplicate.
		wantMain := "watch"
		w := &Window{Start: target, End: target.Add(10 * time.Hour), MinAsleep: 4 * time.Hour}
		got := Select([]Episode{episode("ring", "ring", 2, target.Add(time.Hour), target.Add(9*time.Hour), true), episode("watch", "watch", 1, target.Add(time.Hour), target.Add(9*time.Hour), true)}, w, "")
		if got.Main != wantMain || len(got.Additional) != 0 || len(got.Uncertain) != 0 {
			t.Errorf("got %+v; expected single main %q", got, wantMain)
		}
	})
	t.Run("zone change without confirmed window", func(t *testing.T) {
		// Expected: prior UTC history cannot supply a window in the new zone;
		// the episode must not be labelled main without a confirmed window.
		w := HistoricalWindow(day, "Europe/Belgrade", nil, historical("UTC", 7))
		if w != nil {
			t.Fatalf("unexpected window %+v", w)
		}
		got := Select([]Episode{episode("sleep", "a", 1, target.Add(time.Hour), target.Add(9*time.Hour), true)}, w, "")
		if got.Main != "" || got.Reason != "blind_mode" {
			t.Errorf("got %+v; expected abstention", got)
		}
	})
	t.Run("coverage gap", func(t *testing.T) {
		// Expected: an uncovered observation is not a confirmed main or nap.
		w := &Window{Start: target, End: target.Add(10 * time.Hour), MinAsleep: 4 * time.Hour}
		got := Select([]Episode{episode("incomplete", "a", 1, target.Add(time.Hour), target.Add(9*time.Hour), false)}, w, "")
		if got.Main != "" || len(got.Additional) != 0 || got.Reason != "below_threshold" {
			t.Errorf("got %+v; expected no confirmed role", got)
		}
	})
}
