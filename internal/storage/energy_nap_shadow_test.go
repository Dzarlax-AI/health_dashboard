package storage

import (
	"testing"
	"time"

	"health-receiver/internal/sleepstudy"
	"health-receiver/internal/sleepwire"
)

func TestEnergyNapShadowTrajectory(t *testing.T) {
	ended := time.Date(2026, 8, 30, 14, 40, 0, 0, time.UTC)
	nap := energyNap{End: ended, ObservedAt: ended, Seconds: 40 * 60}
	base := BankResult{State: "fresh", Bank: 44, Display: 44}
	at := func(delay time.Duration) EnergyNapShadowResult {
		return compareEnergyNapShadow(base, 43.8, []energyNap{nap}, ended.Add(delay))
	}
	if before := at(-time.Minute); before.Episodes != 0 || before.Immediate.SignedDelta != 0 {
		t.Fatal("unfinished episode received credit")
	}
	if wake := at(0); wake.Episodes != 1 || wake.Immediate.SignedDelta <= 0 || wake.Gradual.SignedDelta != 0 {
		t.Fatalf("policies did not diverge at wake: %+v", wake)
	}
	if after := at(napShadowRamp); *after.Immediate != *after.Gradual || after.Immediate.SignedDelta <= 0 {
		t.Fatalf("gradual credit did not reach the immediate credit: %+v", after)
	}
	if end := at(napShadowFade); end.Episodes != 0 || end.Immediate.SignedDelta != 0 {
		t.Fatal("expired contribution remained active")
	}
	nearMax := compareEnergyNapShadow(BankResult{State: "fresh", Bank: 99, Display: 99}, 98.7, []energyNap{nap}, ended)
	if nearMax.Immediate.PointsBeforeLimit <= 1 || nearMax.Immediate.SignedDelta != 1 {
		t.Fatal("bank ceiling or rounding was bypassed")
	}
	negative := compareEnergyNapShadow(BankResult{State: "estimated", Bank: -10}, -10.1, []energyNap{nap}, ended)
	if negative.Immediate.SignedDelta <= 0 || negative.Immediate.VisibleDelta != 0 {
		t.Fatal("visible delta did not respect the display floor")
	}
	if nearMax.Immediate.Reason != NapCreditCapped || negative.Immediate.Reason != NapBelowDisplayFloor {
		t.Fatal("explanation lost the reason for the limit")
	}
	if now := at(0); now.Gradual.Reason != NapCreditRamping || now.Immediate.Reason != NapCreditShown {
		t.Fatal("explanation does not distinguish a contribution that has started growing")
	}
	maxed := compareEnergyNapShadow(BankResult{State: "fresh", Bank: 100, Display: 100}, 100, []energyNap{nap}, ended)
	if maxed.Immediate.Reason != NapAtCeiling || maxed.Immediate.VisibleDelta != 0 ||
		maxed.DisplayBefore == nil || *maxed.DisplayBefore != 100 || maxed.Immediate.DisplayAfter != 100 {
		t.Fatal("explanation must account for the already full bank")
	}
	small := nap
	small.Seconds = 10
	rounded := compareEnergyNapShadow(base, 44, []energyNap{small}, ended)
	if rounded.Immediate.Reason != NapCreditRoundedAway || rounded.Immediate.PointsBeforeLimit <= 0 {
		t.Fatal("rounding erased the explanation")
	}
	nap.ObservedAt = ended.Add(time.Hour)
	if got := compareEnergyNapShadow(base, 43.8, []energyNap{nap}, ended.Add(15*time.Minute)); got.Episodes != 0 {
		t.Fatal("future source evidence received credit")
	}
}

func TestEnergyNapShadowSelectsUniqueIntervals(t *testing.T) {
	start := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	episode := func(id string, from, to time.Duration) rolloutEpisode {
		end := start.Add(to)
		return rolloutEpisode{studyCandidate: studyCandidate{
			episode:   sleepstudy.Episode{ID: id, Asleep: []sleepstudy.Span{{Start: start.Add(from), End: end}}},
			candidate: sleepwire.Candidate{Coverage: &sleepwire.Coverage{ObservedAt: end}},
		}}
	}
	main := episode("main", 0, 7*time.Hour)
	nap := episode("watch", 14*time.Hour, 14*time.Hour+40*time.Minute)
	duplicate := episode("ring", 14*time.Hour, 14*time.Hour+40*time.Minute)
	nap.candidate.Coverage.ObservedAt = nap.candidate.Coverage.ObservedAt.Add(5 * time.Hour)
	otherMain := episode("other-main", 18*time.Hour, 25*time.Hour)
	overlapping := episode("overlap", 18*time.Hour, 19*time.Hour)
	day := &rolloutEpisodeDay{episodes: []rolloutEpisode{main, nap, duplicate, overlapping}}
	decision := sleepstudy.Decision{Main: "main", Additional: []string{"watch", "ring", "overlap"}}
	got := selectedEnergyNaps(day, main, decision, map[string]rolloutEpisode{"today": main, "tomorrow": otherMain})
	if len(got) != 1 || got[0].Seconds != 40*60 || !got[0].ObservedAt.Equal(duplicate.candidate.Coverage.ObservedAt) {
		t.Fatalf("selection counted a duplicate or an overlap with a main episode: %v", got)
	}
}

func TestEarlyEnergyNapRejectsConflictsAndSplitMain(t *testing.T) {
	endMain := time.Date(2026, 8, 30, 8, 0, 0, 0, time.UTC)
	start := endMain.Add(6 * time.Hour)
	end := start.Add(40 * time.Minute)
	candidate := sleepwire.Candidate{Zone: "UTC", Observation: &sleepwire.Observation{ObservedAt: end},
		Stages: []sleepwire.Stage{{ID: "core", Kind: "core", Start: start, End: end}}}
	if _, ok := earlyEnergyNap(candidate, end, endMain, "UTC", 3*60*60); !ok {
		t.Fatal("eligible nap was skipped")
	}
	candidate.Stages = append(candidate.Stages, sleepwire.Stage{ID: "awake", Kind: "awake", Start: start, End: end})
	if _, ok := earlyEnergyNap(candidate, end, endMain, "UTC", 3*60*60); ok {
		t.Fatal("conflicting awake/asleep was credited")
	}
	candidate.Stages = candidate.Stages[:1]
	candidate.Stages[0].End = start.Add(7 * time.Hour)
	if _, ok := earlyEnergyNap(candidate, candidate.Stages[0].End, endMain, "UTC", 3*60*60); ok {
		t.Fatal("alternative main sleep was credited")
	}
}

func TestEnergyNapNeverCrossesTenantMidnight(t *testing.T) {
	local, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	// The candidate's source zone is two hours ahead of the tenant's zone.
	end := time.Date(2026, 8, 31, 1, 45, 0, 0, time.FixedZone("source", 4*60*60))
	if len(energyNapsOnDate([]energyNap{{End: end}}, "2026-08-31", local)) != 0 {
		t.Fatal("previous local day contributed credit")
	}
}
