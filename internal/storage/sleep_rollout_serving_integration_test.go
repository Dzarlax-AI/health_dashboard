package storage

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"health-receiver/internal/health"
)

func TestSleepRolloutEnergyRefreshWaitsForRollback(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	t.Setenv("HEALTH_SLEEP_V2_ENABLED", "1")
	t.Setenv("HEALTH_SLEEP_SERVE_ENABLED", "1")
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	serving := &DB{pool: db.pool, sleepRolloutTenant: true}
	if _, err := db.pool.Exec(ctx, "DROP TABLE IF EXISTS __tenant_identity"); err != nil {
		t.Fatal(err)
	}
	defer db.pool.Exec(context.Background(), "DROP TABLE IF EXISTS __tenant_identity")
	tenant := uuid.New()
	if err := serving.EnsureTenantIdentity(ctx, tenant, uuid.New()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 30, 10, 5, 0, 0, time.UTC)
	date := now.Format(time.DateOnly)
	if _, err := db.pool.Exec(ctx, `INSERT INTO daily_scores(date,sleep_total,sleep_deep,sleep_rem,sleep_awake,calories)
		SELECT ($1::text::date+i)::text,8,1.5,1,0,500 FROM generate_series(-35,0) i`, date); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO sleep_rollout_daily(wake_date,generation,candidate,revision,sleep_seconds)
		VALUES($1::text::date,1,'["watch","night"]',1,7200)`, date); err != nil {
		t.Fatal(err)
	}
	defer db.pool.Exec(context.Background(), `DELETE FROM sleep_rollout_daily WHERE wake_date=$1::text::date`, date)
	publication := SleepRolloutPublication{Active: true, Tenant: tenant, From: date, Through: date}
	raw, err := json.Marshal(publication)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSettings(map[string]string{SleepRolloutModeSetting: "serve", sleepRolloutPublicationSetting: string(raw)}); err != nil {
		t.Fatal(err)
	}
	want, err := db.ComputeBankForDate(ctx, "UTC", date)
	if err != nil || want.State == "stale" {
		t.Fatalf("legacy fixture: %+v, %v", want, err)
	}

	rollback, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback.Rollback(context.Background())
	if err := lockSleepRollout(ctx, rollback); err != nil {
		t.Fatal(err)
	}
	var blocker int
	if err := rollback.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		bank    BankResult
		handled bool
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		bank, handled, err := serving.refreshRolloutEnergyObservation(ctx, "UTC", func() time.Time { return now })
		done <- outcome{bank, handled, err}
	}()
	for {
		select {
		case got := <-done:
			t.Fatalf("refresh did not wait for rollback: %+v", got)
		default:
		}
		var waiting bool
		if err := rollback.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks
			WHERE locktype='advisory' AND NOT granted AND $1=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Publish the same two-row transition as SwitchSleepRollout while the
	// refresh is blocked on the transaction fence.
	if _, err := rollback.Exec(ctx, `UPDATE settings SET value=CASE WHEN key=$1 THEN 'off'
		ELSE jsonb_set(value::jsonb,'{active}','false')::text END WHERE key IN ($1,$2)`,
		SleepRolloutModeSetting, sleepRolloutPublicationSetting); err != nil {
		t.Fatal(err)
	}
	if err := rollback.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || !got.handled || got.bank.Bank != want.Bank {
			t.Fatalf("refresh used the rolled-back revision: %+v, want %d", got, want.Bank)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var stored int
	if err := db.pool.QueryRow(ctx, "SELECT bank FROM energy_snapshots WHERE ts_bucket=$1", now).Scan(&stored); err != nil || stored != want.Bank {
		t.Fatalf("post-rollback slot: %d, %v, want %d", stored, err, want.Bank)
	}
}

func TestSleepRolloutCardPreservesLegacyNightOnlyDates(t *testing.T) {
	total := 6.0
	data := &health.RawMetrics{
		LastDate: "2026-01-30", ReadinessEvidence: &health.ReadinessEvidenceInput{},
		NightSleepByDate: []health.DatedValue{
			{Date: "2026-01-30", Val: 9},
			{Date: "2026-01-29", Val: 7},
			{Date: "2026-01-28", Val: 8},
		},
	}
	blocks := map[string]*LocalShadowSleep{
		"2026-01-30": {Total: &total},
		"2026-01-29": {},
	}
	applyLocalShadowSleep(data, blocks)
	if !slices.Equal(data.NightSleep, []float64{6, 8}) {
		t.Fatalf("unexpected preferred series: %v", data.NightSleep)
	}
	blocks["2026-01-30"] = &LocalShadowSleep{}
	applyLocalShadowSleep(data, blocks)
	if len(data.NightSleep) != 0 || len(data.Sleep) != 0 {
		t.Fatal("cleared current date reused a legacy sleep card")
	}
}

func TestSleepRolloutReplacementPreservesValidOrdinaryMetrics(t *testing.T) {
	const failed = `{"metrics":[{"name":"step_count","units":"count","data":[{"date":"2026-02-30","qty":1},{"date":"2026-02-28","qty":2}]}]}`
	const complete = `{"metrics":[{"name":"step_count","units":"count","data":[{"date":"2026-02-27","qty":1},{"date":"2026-02-28","qty":2}]}]}`
	const partial = `{"metrics":[{"name":"step_count","units":"count","data":[{"date":"2026-02-27","qty":1}]}]}`
	if !replacementCoversMixedMetrics(failed, complete) || replacementCoversMixedMetrics(failed, partial) {
		t.Fatal("audit did not distinguish complete and partial repairs")
	}
}
