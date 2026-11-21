package storage

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"health-receiver/internal/sleepstudy"
	"health-receiver/internal/sleepwire"
)

func TestSleepEpisodeStudyCoverageAndStageParity(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	if _, err := db.pool.Exec(ctx, "DROP TABLE IF EXISTS __tenant_identity; DELETE FROM sleep_rollout_candidates; DELETE FROM sleep_period_coverage"); err != nil {
		t.Fatal(err)
	}
	defer db.pool.Exec(ctx, "DROP TABLE IF EXISTS __tenant_identity; DELETE FROM sleep_rollout_candidates; DELETE FROM sleep_period_coverage")
	profile := uuid.New()
	if err := db.EnsureTenantIdentity(ctx, profile, uuid.New()); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 30, 1, 0, 0, 0, time.UTC)
	candidate := sleepwire.Candidate{ID: "night", Revision: 1, Zone: "UTC", Stages: []sleepwire.Stage{
		{ID: "detailed", Kind: "core", Start: start, End: start.Add(3 * time.Hour)},
		{ID: "awake", Kind: "awake", Start: start.Add(3 * time.Hour), End: start.Add(4 * time.Hour)},
		{ID: "second", Kind: "rem", Start: start.Add(4 * time.Hour), End: start.Add(8 * time.Hour)},
	}}
	raw, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	date := start.Format(time.DateOnly)
	id := `["source","night"]`
	if _, err = db.pool.Exec(ctx, `INSERT INTO sleep_rollout_candidates(profile,id,revision,wake_date,priority,snapshot)
	 VALUES($1,$2,1,$3::text::date,0,$4)`, profile, id, date, json.RawMessage(raw)); err != nil {
		t.Fatal(err)
	}
	read := func(expected bool) {
		tx, err := db.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		days, err := loadStudyCandidates(ctx, tx, date, date)
		if err != nil {
			t.Fatal(err)
		}
		day := days[date]
		if day == nil || len(day.candidates) != 1 {
			t.Fatalf("fixture day unavailable")
		}
		c := day.candidates[0]
		if c.profileCovered != expected || !c.sleepBridge || c.episode.Covered {
			t.Fatalf("raw coverage=%v projected coverage=%v bridge=%v", c.profileCovered, c.episode.Covered, c.sleepBridge)
		}
		window := &sleepstudy.Window{Start: start.Add(-time.Hour), End: start.Add(9 * time.Hour), MinAsleep: 4 * time.Hour}
		if got := studyRecordedWakeOnly(day, window).Main; (got == id) != expected {
			t.Fatalf("selection with source coverage %v = %q", expected, got)
		}
		match, err := studySQLParity(ctx, tx, id, studySleepBlock(c))
		if err != nil || !match {
			t.Fatalf("SQL parity = %v, %v", match, err)
		}
	}
	read(false)
	end := time.Date(2026, 1, 30, 12, 0, 0, 0, time.UTC)
	if _, err = db.pool.Exec(ctx, `INSERT INTO sleep_period_coverage(wake_date,source_epoch,capture_completeness,coverage_generation,
	 covered_interval_start,covered_interval_end,observed_at,input_hash) VALUES($1,'fixture','complete','fixture',$2,$3,$3,'fixture')`, date, end.AddDate(0, 0, -1), end); err != nil {
		t.Fatal(err)
	}
	read(true)
}
