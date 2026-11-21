package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"health-receiver/internal/sleepwire"
)

func TestRevisionBoundSleepSelectionWithdrawal(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	if _, err := db.pool.Exec(ctx, `DROP TABLE IF EXISTS __tenant_identity;
		TRUNCATE sleep_rollout_packets,sleep_rollout_batches,sleep_rollout_candidates,sleep_rollout_daily,sleep_rollout_results`); err != nil {
		t.Fatal(err)
	}
	defer db.pool.Exec(ctx, `DROP TABLE IF EXISTS __tenant_identity;
		TRUNCATE sleep_rollout_packets,sleep_rollout_batches,sleep_rollout_candidates,sleep_rollout_daily,sleep_rollout_results`)
	tenant := uuid.New()
	if err := db.EnsureTenantIdentity(ctx, tenant, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSettings(map[string]string{SleepRolloutModeSetting: "prepare", "timezone": "UTC",
		"sleep_rollout_source_priority": `["watch","ring"]`}); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	date := day.Format(time.DateOnly)
	clock := func() time.Time { return day.Add(22 * time.Hour) }
	t.Setenv("HEALTH_SLEEP_V2_ENABLED", "1")
	t.Setenv("HEALTH_SLEEP_GO_PROJECTION_ENABLED", "1")
	t.Setenv("HEALTH_SLEEP_SERVE_ENABLED", "")
	ordinary := &DB{pool: db.pool, sleepRolloutTenant: true}
	setSources := func(sources ...string) {
		t.Helper()
		raw, err := json.Marshal(sleepGoProjection{From: date, Through: date, Sources: sources})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.SaveSettings(map[string]string{SleepGoProjectionSetting: string(raw)}); err != nil {
			t.Fatal(err)
		}
	}
	setSources("watch", "ring", "unconfirmed")
	checkOrdinary := func(minutes int) {
		t.Helper()
		bank, err := ordinary.ComputeBankForDate(ctx, "UTC", date)
		if err != nil {
			t.Fatal(err)
		}
		nights, err := ordinary.GetSleepSummary(date, date)
		if err != nil {
			t.Fatal(err)
		}
		if minutes < 0 && len(nights) != 0 {
			t.Fatalf("ordinary sleep still shows withdrawn night")
		}
		if minutes >= 0 && minutes != 420 && (len(nights) != 1 || math.Abs(nights[0].Total*60-float64(minutes)) > 0.01) {
			t.Fatalf("ordinary sleep summary does not show %d minutes", minutes)
		}
		hours, exists := bank.Components["sleep_total_h"]
		if minutes < 0 {
			if !slices.Contains(bank.Flags, "imputed_sleep") || exists {
				t.Fatalf("expected ordinary missing sleep, flags=%v, observed component=%v", bank.Flags, exists)
			}
		} else if !exists || math.Abs(hours*60-float64(minutes)) > 0.01 {
			t.Fatalf("ordinary sleep = %v hours, want %d minutes", hours, minutes)
		}
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO daily_scores(date,sleep_total,sleep_deep,sleep_rem,sleep_awake,calories)
		SELECT ($1::text::date+i)::text,7,1,1,0,500 FROM generate_series(-35,0) i`, date); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO energy_snapshots
		(ts_bucket,date,bank,drain_delta,restore_delta,formula_version,components)
		VALUES ($1,$2,51,1,2,2,'{}')`, day.Add(8*time.Hour), date); err != nil {
		t.Fatal(err)
	}
	stage := func(id, kind string, start time.Time, minutes int) sleepwire.Stage {
		return sleepwire.Stage{ID: id, Kind: kind, Start: start, End: start.Add(time.Duration(minutes) * time.Minute)}
	}
	makeCandidate := func(source, id string, date time.Time, revision int64, stages ...sleepwire.Stage) sleepwire.Candidate {
		return sleepwire.Candidate{ID: id, Zone: "UTC", Revision: revision, Stages: stages,
			Coverage: &sleepwire.Coverage{SourceID: source, Revision: revision, Start: date.AddDate(0, 0, -1).Add(12 * time.Hour),
				End: date.Add(12 * time.Hour), ObservedAt: date.Add(12*time.Hour + time.Duration(revision)*time.Minute)}}
	}
	submit := func(source string, candidate sleepwire.Candidate) (string, error) {
		t.Helper()
		wake, err := candidate.ValidateSource(source)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(candidate)
		if err != nil {
			t.Fatal(err)
		}
		return db.AcceptSleepRollout(ctx, SleepRolloutInput{SourceID: source, WakeDate: wake, Candidate: candidate, Raw: raw})
	}
	accept := func(source string, candidate sleepwire.Candidate, want string) {
		t.Helper()
		got, err := submit(source, candidate)
		if got != want || err != nil {
			t.Fatalf("acceptance got %q, %v, want %q", got, err, want)
		}
	}
	apply := func() {
		t.Helper()
		if ok, err := db.RunSleepRolloutNext(ctx, clock); err != nil || !ok {
			t.Fatalf("apply=%v err=%v", ok, err)
		}
	}
	for offset := 7; offset >= 1; offset-- {
		prior := day.AddDate(0, 0, -offset)
		accept("ring", makeCandidate("ring", fmt.Sprintf("history-%d", offset), prior, 1, stage("night", "asleep_unspecified", prior, 420)), "accepted")
		apply()
	}
	watch := makeCandidate("watch", "night", day, 1, stage("partial", "core", day, 120))
	ring := makeCandidate("ring", "night", day, 1, stage("main", "asleep_unspecified", day, 332),
		stage("nap", "asleep_unspecified", day.Add(8*time.Hour), 136))
	accept("watch", watch, "accepted")
	apply()
	accept("ring", ring, "accepted")
	apply()
	assertSelection := func(source, state string, total, additional, accounted int) int64 {
		t.Helper()
		var selection sleepSelection
		var gotSource string
		var generation int64
		var gotTotal, gotAdditional, gotAccounted int
		if err := db.pool.QueryRow(ctx, `SELECT COALESCE(d.candidate::jsonb->>0,''),d.episode_selection,
			COALESCE(d.sleep_seconds,-1)::int,COALESCE((d.episode_selection->>'additional_seconds')::int,-1),
			COALESCE((d.episode_selection->>'accounted_seconds')::int,-1),r.generation
			FROM sleep_rollout_daily d JOIN sleep_rollout_results r ON r.day=d.wake_date AND r.generation=d.generation
			WHERE d.wake_date=$1::text::date`, date).Scan(&gotSource, &selection, &gotTotal, &gotAdditional, &gotAccounted, &generation); err != nil {
			t.Fatal(err)
		}
		if gotSource != source || selection.State != state || gotTotal != total || gotAdditional != additional || gotAccounted != accounted {
			t.Fatalf("selection = %q %s (%d,%d,%d), want %q %s (%d,%d,%d)", gotSource, selection.State, gotTotal, gotAdditional, gotAccounted, source, state, total, additional, accounted)
		}
		return generation
	}
	assertSelection("ring", "measured", 332*60, 136*60, 468*60)
	checkOrdinary(420)
	setSources("watch", "ring")
	watch = makeCandidate("watch", "night", day, 2, stage("main", "core", day, 300))
	accept("watch", watch, "accepted")
	apply()
	previous := assertSelection("ring", "measured", 332*60, 136*60, 468*60)
	checkOrdinary(332)
	withdraw := func(source string, newRevision, oldRevision int64) sleepwire.Candidate {
		c := makeCandidate(source, "night", day, newRevision)
		c.Withdrawal = &sleepwire.Withdrawal{WakeDate: date, PreviousRevision: oldRevision}
		return c
	}
	if _, err := submit("ring", withdraw("ring", 3, 2)); !errors.Is(err, ErrSleepRolloutConflict) {
		t.Fatalf("withdrawal with wrong predecessor = %v", err)
	}
	retracted := withdraw("ring", 3, 1)
	incomplete := retracted
	partial := *incomplete.Coverage
	partial.Start = day
	incomplete.Coverage = &partial
	if _, err := submit("ring", incomplete); !errors.Is(err, ErrSleepRolloutConflict) {
		t.Fatalf("partial-window withdrawal = %v", err)
	}
	accept("ring", retracted, "accepted")
	// Abort the dependent write after candidate projection. The transaction
	// must preserve every already-visible result and leave the receipt queued.
	if _, err := db.pool.Exec(ctx, `ALTER TABLE sleep_rollout_results ADD CONSTRAINT fixture_no_new_generation CHECK (generation <= `+fmt.Sprint(previous)+`)`); err != nil {
		t.Fatal(err)
	}
	defer db.pool.Exec(ctx, `ALTER TABLE sleep_rollout_results DROP CONSTRAINT IF EXISTS fixture_no_new_generation`)
	if ok, err := db.RunSleepRolloutNext(ctx, clock); err == nil || ok {
		t.Fatalf("dependent error got %v, %v", ok, err)
	}
	assertSelection("ring", "measured", 332*60, 136*60, 468*60)
	if _, err := db.pool.Exec(ctx, `ALTER TABLE sleep_rollout_results DROP CONSTRAINT fixture_no_new_generation`); err != nil {
		t.Fatal(err)
	}
	apply()
	assertSelection("watch", "measured", 300*60, -1, 300*60)
	checkOrdinary(300)
	accept("ring", ring, "repeat")
	stale := makeCandidate("ring", "night", day, 2, stage("old", "core", day, 360))
	accept("ring", stale, "stale")
	accept("watch", withdraw("watch", 3, 2), "accepted")
	apply()
	assertSelection("", "confirmed_source_absence", -1, -1, -1)
	checkOrdinary(-1)
	record := insertTestRawRecord(t, db, "archived-sleep-ordinary-replay")
	if err := db.InsertPoints(record, []MetricPoint{{
		MetricName: "sleep_total", Date: date + " 00:00:00 +0000", Qty: 7, Units: "hr", Source: "old device",
	}}); err != nil {
		t.Fatal(err)
	}
	checkOrdinary(-1)
	legacyNights, err := db.GetSleepSummary(date, date)
	if err != nil || len(legacyNights) != 1 || legacyNights[0].Total != 7 {
		t.Fatal("legacy source input should still be present")
	}
	briefing, err := ordinary.GetHealthBriefing("en")
	if err != nil {
		t.Fatal(err)
	}
	if briefing.Sleep != nil && briefing.Sleep.LatestTotal != nil {
		t.Fatal("Go briefing exposed the withdrawn night")
	}
	history, err := ordinary.GetReadinessHistoryContext(ctx, 1)
	if err != nil || len(history) != 1 || history[0].Date != date {
		t.Fatalf("Go readiness history did not include the projected date: rows=%d err=%v", len(history), err)
	}
	var bank BankResult
	var readiness LocalShadowReadiness
	var snapshot float64
	if err := db.pool.QueryRow(ctx, `SELECT energy,readiness FROM sleep_rollout_results WHERE day=$1::text::date`, date).Scan(&bank, &readiness); err != nil {
		t.Fatal(err)
	}
	if readiness.Sleep == nil || readiness.Sleep.Total != nil || history[0].Score != readiness.Score || !slices.Contains(bank.Flags, "imputed_sleep") {
		t.Fatalf("withdrawal lost missing sleep semantics: readiness=%+v bank flags=%v", readiness.Sleep, bank.Flags)
	}
	if err := db.pool.QueryRow(ctx, `SELECT bank FROM energy_snapshots WHERE ts_bucket=$1`, day.Add(8*time.Hour)).Scan(&snapshot); err != nil || snapshot != 51 {
		t.Fatalf("observed energy = %v, %v", snapshot, err)
	}
	if _, handled, err := ordinary.refreshRolloutEnergyObservation(ctx, "UTC", func() time.Time { return day.Add(8 * time.Hour) }); err != nil || !handled {
		t.Fatalf("refresh with local Go projection: handled=%t err=%v", handled, err)
	}
	if err := db.pool.QueryRow(ctx, `SELECT bank FROM energy_snapshots WHERE ts_bucket=$1`, day.Add(8*time.Hour)).Scan(&snapshot); err != nil || snapshot != 51 {
		t.Fatal("refresh rewrote the observed snapshot")
	}
	var legacyHours float64
	if err := db.pool.QueryRow(ctx, `SELECT sleep_total FROM daily_scores WHERE date=$1`, date).Scan(&legacyHours); err != nil || legacyHours != 7 {
		t.Fatal("ordinary projection must retain underlying historical rows")
	}
	restore := makeCandidate("watch", "night", day, 4, stage("new", "core", day, 390))
	accept("watch", restore, "accepted")
	apply()
	checkOrdinary(390)
	outsider := makeCandidate("unverified-other", "other", day, 1, stage("fragment", "core", day, 45))
	accept("unverified-other", outsider, "accepted")
	apply()
	checkOrdinary(-1)
	if _, handled, err := ordinary.refreshRolloutEnergyObservation(ctx, "UTC", func() time.Time { return day.AddDate(0, 0, 50) }); err != nil || handled {
		t.Fatalf("unowned ordinary date should keep its existing refresh path: handled=%t err=%v", handled, err)
	}
	// The first source-bound revision can move an older legacy candidate.
	// Its old projection and dependent results must change in the same unit.
	earlier := day.AddDate(0, 0, -11)
	legacy := sleepwire.Candidate{ID: "moved", Zone: "UTC", Revision: 1, Stages: []sleepwire.Stage{stage("legacy", "core", earlier, 420)}}
	accept("watch", legacy, "accepted")
	apply()
	var before *float64
	if err := db.pool.QueryRow(ctx, `SELECT sleep_seconds FROM sleep_rollout_daily WHERE wake_date=$1::text::date`, earlier.Format(time.DateOnly)).Scan(&before); err != nil || before == nil {
		t.Fatalf("legacy projection = %v %v", before, err)
	}
	moved := makeCandidate("watch", "moved", day.AddDate(0, 0, -10), 2, stage("moved", "core", day.AddDate(0, 0, -10), 420))
	moved.Coverage.Start = earlier.Add(-12 * time.Hour)
	accept("watch", moved, "accepted")
	apply()
	var after *float64
	if err := db.pool.QueryRow(ctx, `SELECT d.sleep_seconds FROM sleep_rollout_daily d JOIN sleep_rollout_results r ON r.day=d.wake_date AND r.generation=d.generation
		WHERE d.wake_date=$1::text::date`, earlier.Format(time.DateOnly)).Scan(&after); err != nil || after != nil {
		t.Fatalf("legacy projection after move = %v %v", after, err)
	}
}
