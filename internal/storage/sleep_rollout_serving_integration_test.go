package storage

import (
	"context"
	"encoding/json"
	"errors"
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
	const legacySleepHours = 8
	if got := want.Components["sleep_total_h"]; got != legacySleepHours {
		t.Fatalf("legacy fixture sleep input: %v", got)
	}

	// A history snapshot must not queue more per-day queries while another
	// active request owns the per-tenant calculation slot.
	serving.sleepReadinessHistoryMu.Lock()
	_, historyErr := serving.GetReadinessHistoryContext(ctx, MaxReadinessHistoryDays)
	serving.sleepReadinessHistoryMu.Unlock()
	if !errors.Is(historyErr, ErrReadinessHistoryBusy) {
		t.Fatalf("concurrent history was not rejected: %v", historyErr)
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
	refreshCtx, cancelRefresh := context.WithCancel(ctx)
	done := make(chan outcome, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		bank, handled, err := serving.refreshRolloutEnergyObservation(refreshCtx, "UTC", func() time.Time { return now })
		done <- outcome{bank, handled, err}
	}()
	defer func() {
		cancelRefresh()
		_ = rollback.Rollback(context.Background())
		<-joined
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
		if got.err != nil || !got.handled || got.bank.Bank != want.Bank ||
			got.bank.Components["sleep_total_h"] != legacySleepHours {
			t.Fatalf("refresh used the rolled-back revision: %+v, want %d", got, want.Bank)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var stored int
	var sleepHours *float64
	err = db.pool.QueryRow(ctx, `SELECT bank,(components->>'sleep_total_h')::float8
		FROM energy_snapshots WHERE ts_bucket=$1`, now).Scan(&stored, &sleepHours)
	if err != nil || stored != want.Bank || sleepHours == nil || *sleepHours != legacySleepHours {
		t.Fatalf("post-rollback slot: %d, sleep %v, %v; want bank %d and legacy sleep", stored, sleepHours, err, want.Bank)
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

func TestSleepRolloutBriefingOmitsUnmeasuredStages(t *testing.T) {
	total, oldStage, zero, rem := 8.0, 1.5, 0.0, 2.0
	const date = "2026-01-30"
	ls := health.GetStrings("en")
	for _, tc := range []struct {
		name       string
		deep, rem  *float64
		wantLabels bool
	}{
		{name: "coarse or conflicted"},
		{name: "explicit zero and measured REM", deep: &zero, rem: &rem, wantLabels: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := health.RawMetrics{
				LastDate: date, ReadinessEvidence: &health.ReadinessEvidenceInput{},
				Daily: []health.DailyHealthMetrics{
					{Date: "2026-01-29", Sleep: &total, Deep: &oldStage, REM: &oldStage},
					{Date: "2026-01-28", Sleep: &total, Deep: &oldStage, REM: &oldStage},
					{Date: "2026-01-27", Sleep: &total, Deep: &oldStage, REM: &oldStage},
				},
			}
			applyLocalShadowSleep(&data, map[string]*LocalShadowSleep{date: {Total: &total, Deep: tc.deep, REM: tc.rem}})
			briefing := health.ComputeBriefing(data, "en")
			sleep := slices.IndexFunc(briefing.Sections, func(section health.BriefingSection) bool {
				return section.Key == "sleep"
			})
			if sleep < 0 {
				t.Fatal("published duration disappeared")
			}
			var deepText, remText string
			for _, detail := range briefing.Sections[sleep].Details {
				switch detail.Label {
				case ls["lbl_deep_sleep"]:
					deepText = detail.Value
				case ls["lbl_rem"]:
					remText = detail.Value
				}
			}
			if tc.wantLabels {
				if deepText != "0% of total" || remText != "25% of total" {
					t.Fatalf("unexpected published stages: %q, %q", deepText, remText)
				}
			} else if deepText != "" || remText != "" {
				t.Fatalf("reused an older stage: %q, %q", deepText, remText)
			}
		})
	}
}

func TestSleepRolloutReadinessHistoryNormalizesDays(t *testing.T) {
	for _, day := range []int{-1, 0, MaxReadinessHistoryDays + 1} {
		if got := ReadinessHistoryDays(day); got != DefaultReadinessHistoryDays {
			t.Fatalf("days %d returned %d", day, got)
		}
	}
	for _, day := range []int{1, DefaultReadinessHistoryDays, MaxReadinessHistoryDays} {
		if got := ReadinessHistoryDays(day); got != day {
			t.Fatalf("days %d returned %d", day, got)
		}
	}
}
