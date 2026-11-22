package storage

import (
	"testing"
	"time"

	"health-receiver/internal/sleepstudy"
	"health-receiver/internal/sleepwire"
)

func TestRolloutGroupCoverageAndCoarseEdges(t *testing.T) {
	day := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	stage := func(kind string, start, end time.Duration) sleepwire.Stage {
		return sleepwire.Stage{Kind: kind, Start: day.Add(start), End: day.Add(end)}
	}
	coverage := sleepwire.Coverage{SourceID: "fixture", Revision: 1, Start: day.Add(-12 * time.Hour), End: day.Add(12 * time.Hour)}
	for _, test := range []struct {
		name      string
		stages    []sleepwire.Stage
		partial   bool
		durations []int
		covered   []bool
	}{
		{"separate coarse nap", []sleepwire.Stage{stage("core", 0, 7*time.Hour), stage("asleep_unspecified", 9*time.Hour, 10*time.Hour)}, false, []int{420, 60}, []bool{true, true}},
		{"unverified coarse edge", []sleepwire.Stage{stage("core", 0, 280*time.Minute), stage("asleep_unspecified", -138*time.Minute, 280*time.Minute)}, false, []int{280}, []bool{false}},
		{"query matches only observed segment", []sleepwire.Stage{stage("core", 0, 7*time.Hour)}, true, []int{420}, []bool{false}},
		{"gap exactly thirty minutes", []sleepwire.Stage{stage("core", 0, 120*time.Minute), stage("rem", 150*time.Minute, 300*time.Minute)}, false, []int{270}, []bool{true}},
		{"gap over thirty minutes with in-bed bridge", []sleepwire.Stage{stage("core", 0, 120*time.Minute),
			stage("in_bed", 120*time.Minute, 150*time.Minute+time.Second), stage("rem", 150*time.Minute+time.Second, 300*time.Minute+time.Second)}, false, []int{120, 150}, []bool{true, true}},
		{"documented awake gap", []sleepwire.Stage{stage("core", 0, 120*time.Minute),
			stage("awake", 120*time.Minute, 180*time.Minute), stage("rem", 180*time.Minute, 420*time.Minute)}, false, []int{360}, []bool{true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			proof := coverage
			if test.partial {
				proof.Start, proof.End = day, day.Add(7*time.Hour)
			}
			snapshot := sleepwire.Candidate{Zone: "UTC", Coverage: &proof, Stages: test.stages}
			episodes := groupRolloutSnapshot("fixture", "fixture", 0, snapshot, time.UTC)
			if len(episodes) != len(test.durations) {
				t.Fatalf("got %d groups, want %d", len(episodes), len(test.durations))
			}
			for i, e := range episodes {
				if got := int(sleepstudy.Duration(e.episode.Asleep).Minutes()); got != test.durations[i] || e.episode.Covered != test.covered[i] {
					t.Fatalf("group %d duration=%d covered=%v", i, got, e.episode.Covered)
				}
			}
		})
	}
}

func TestRolloutSelectionDaytimeAndDST(t *testing.T) {
	location, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 10, 25, 0, 0, 0, 0, location)
	date := day.Format(time.DateOnly)
	if bound := rolloutAccountingWindow(date, location); bound.End.Sub(bound.Start) != 25*time.Hour {
		t.Fatalf("fall-back window = %v", bound.End.Sub(bound.Start))
	}
	days := map[string]*rolloutEpisodeDay{}
	for i := -7; i <= 0; i++ {
		d := day.AddDate(0, 0, i)
		key := d.Format(time.DateOnly)
		start := time.Date(d.Year(), d.Month(), d.Day(), 7, 0, 0, 0, location)
		end := time.Date(d.Year(), d.Month(), d.Day(), 16, 3, 0, 0, location)
		bound := rolloutAccountingWindow(subtractDays(key, -1), location)
		snapshot := sleepwire.Candidate{Zone: location.String(), Coverage: &sleepwire.Coverage{
			Start: rolloutAccountingWindow(key, location).Start, End: bound.End,
		}, Stages: []sleepwire.Stage{{Kind: "asleep_unspecified", Start: start, End: end}}}
		days[key] = &rolloutEpisodeDay{zone: location.String(), episodes: groupRolloutSnapshot("shift", "ring", 1, snapshot, location)}
	}
	decision, main := selectRolloutDay(days, date, "")
	if decision.Main == "" || sleepstudy.Duration(main.episode.Asleep) != 543*time.Minute {
		t.Fatalf("daytime main at DST: %+v", decision)
	}
	days[date].mixedZone = true
	decision, _ = selectRolloutDay(days, date, "")
	if decision.Main != "" || decision.Reason != "mixed_zones" {
		t.Fatalf("mixed-zone selection: %+v", decision)
	}
}

func TestRolloutSplitPacketsAreUncertain(t *testing.T) {
	day := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	e := func(id string, start, end time.Duration) rolloutEpisode {
		return rolloutEpisode{packetID: id, studyCandidate: studyCandidate{episode: sleepstudy.Episode{ID: id, Source: "one", Covered: true,
			Asleep: []sleepstudy.Span{{Start: day.Add(start), End: day.Add(end)}}}}}
	}
	for _, test := range []struct {
		name          string
		first, second rolloutEpisode
		secondDate    string
	}{
		{"same wake date", e("first", 0, 4*time.Hour), e("second", 4*time.Hour+20*time.Minute, 7*time.Hour), "2026-09-20"},
		{"different wake dates", e("first", 19*time.Hour, 23*time.Hour+55*time.Minute), e("second", 24*time.Hour+5*time.Minute, 29*time.Hour+45*time.Minute), "2026-09-21"},
	} {
		t.Run(test.name, func(t *testing.T) {
			days := map[string]*rolloutEpisodeDay{"2026-09-20": {episodes: []rolloutEpisode{test.first}}}
			if days[test.secondDate] == nil {
				days[test.secondDate] = &rolloutEpisodeDay{}
			}
			days[test.secondDate].episodes = append(days[test.secondDate].episodes, test.second)
			disqualifySplitSourcePackets(days)
			for _, day := range days {
				for _, episode := range day.episodes {
					if episode.episode.Covered {
						t.Fatalf("independently revised fragment stayed eligible")
					}
				}
			}
		})
	}
}

func TestRolloutUnresolvedAdjacentEpisodeWithholdsAccounting(t *testing.T) {
	day := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	date := day.Format(time.DateOnly)
	next := day.AddDate(0, 0, 1).Format(time.DateOnly)
	main := rolloutEpisode{packetID: "main", studyCandidate: studyCandidate{
		candidate: sleepwire.Candidate{Coverage: &sleepwire.Coverage{Start: day.Add(-12 * time.Hour), End: day.Add(12 * time.Hour)}},
		episode:   sleepstudy.Episode{ID: "main", Asleep: []sleepstudy.Span{{Start: day, End: day.Add(7 * time.Hour)}}},
	}}
	unresolved := rolloutEpisode{packetID: "other", studyCandidate: studyCandidate{
		episode: sleepstudy.Episode{ID: "other", Asleep: []sleepstudy.Span{
			{Start: day.Add(11 * time.Hour), End: day.Add(24*time.Hour + time.Hour)},
		}},
	}}
	days := map[string]*rolloutEpisodeDay{
		date: {zone: "UTC", episodes: []rolloutEpisode{main}},
		next: {zone: "UTC", episodes: []rolloutEpisode{unresolved}},
	}
	decisions := map[string]sleepstudy.Decision{date: {Main: "main"}, next: {Reason: "blind_mode"}}
	result := summarizeRolloutDay(days, decisions, map[string]rolloutEpisode{date: main}, date)
	if result.State != "measured" || result.AccountedSeconds != nil {
		t.Fatalf("accounting was reported despite unknown coverage: %+v", result)
	}
}
