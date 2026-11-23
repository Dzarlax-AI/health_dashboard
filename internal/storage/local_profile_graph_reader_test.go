package storage

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// The full-profile harness supplies freshly built, isolated dbt relations and
// fixture-only legacy aggregates with intentionally distinct values. All methods
// below use the same entry points as normal application callers.
func TestInstalledProfileDailyReader(t *testing.T) {
	source, graph := os.Getenv("PROFILE_SOURCE_SCHEMA"), os.Getenv("PROFILE_GRAPH_SCHEMA")
	if graph == "" {
		t.Skip("requires the isolated full-profile dbt build")
	}
	ctx := context.Background()
	dsn := os.Getenv("DATABASE_URL")
	legacy, err := NewWithSchema(ctx, dsn, source)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	check := func(db *DB, wantHRV, wantRHR, wantSleep, wantSteps float64, wantRaw, wantDisplay int) {
		t.Helper()
		const date = "2026-01-01"
		autonomic, err := db.LoadAutonomicRows(date, date)
		if err != nil || len(autonomic) != 1 || autonomic[0].HRV == nil || *autonomic[0].HRV != wantHRV || autonomic[0].RHR == nil || *autonomic[0].RHR != wantRHR {
			t.Fatalf("autonomic rows = %+v, err=%v, want HRV %v, RHR %v", autonomic, err, wantHRV, wantRHR)
		}
		sleep, err := db.LoadSleepRows(date, date)
		if err != nil || len(sleep) != 1 || sleep[0].Total == nil || *sleep[0].Total != wantSleep {
			t.Fatalf("sleep rows = %+v, err=%v, want %v hours", sleep, err, wantSleep)
		}
		points, err := db.GetMetricData("resting_heart_rate", date+" 00:00", date+" 23:59", "day", "AVG")
		if err != nil || len(points) != 1 || points[0].Qty != wantRHR {
			t.Fatalf("day chart = %+v, err=%v, want %v", points, err, wantRHR)
		}
		// The public AI reader selects its latest hour. The historical briefing
		// uses this bounded reader so the January check also works on fixtures
		// whose current evidence window has already moved past January.
		public := db.GetRawMetrics()
		if public == nil || len(public.Daily) == 0 {
			t.Fatal("public briefing inputs missing")
		}
		raw := db.rawMetricsFromDailyScoresAt("2026-01-02", true)
		found := false
		if raw != nil {
			for _, d := range raw.Daily {
				if d.Date == date && d.HRV != nil && *d.HRV == wantHRV && d.Steps != nil && *d.Steps == wantSteps {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("briefing input missing %s HRV %v, steps %v", date, wantHRV, wantSteps)
		}
		bank, err := db.ComputeBankForDate(ctx, "UTC", date)
		if err != nil {
			t.Fatal(err)
		}
		if db.profileGraphSchema == "" {
			if bank.State != "fresh" || bank.Components["sleep_total_h"] != 9 || bank.Components["active_kcal"] != 246 {
				t.Fatalf("legacy Energy: %+v", bank)
			}
		} else if bank.State != "stale" || len(bank.Components) != 0 {
			t.Fatalf("graph Energy with missing December inputs: %+v", bank)
		}
		briefing, err := db.GetHealthBriefing("en")
		if err != nil || briefing == nil || briefing.ReadinessBand == "" || briefing.RawMetrics == nil || briefing.RawMetrics.LastDate != public.LastDate {
			t.Fatalf("Today briefing readiness missing for selected date %s: err=%v", public.LastDate, err)
		}
		history, err := db.GetReadinessHistoryContext(ctx, 1)
		if err != nil || len(history) != 1 || history[0].Score != 73 || history[0].Band == "" {
			t.Fatalf("cached readiness history %+v err=%v", history, err)
		}
		readinessProjection, err := db.morningReadinessProjection(ctx, raw)
		if err != nil {
			t.Fatal(err)
		}
		if readinessProjection.RawScore != wantRaw || readinessProjection.DisplayScore != wantDisplay || readinessProjection.Confidence != "provisional" {
			t.Fatalf("January 2 readiness %+v; want raw %d and provisionally displayed %d", readinessProjection, wantRaw, wantDisplay)
		}
		var readiness int
		var clock string
		if err := db.pool.QueryRow(ctx, `SELECT readiness,computed_at FROM daily_scores WHERE date=$1`, date).Scan(&readiness, &clock); err != nil || readiness != 77 || clock != "2026-01-02 06:00 UTC" {
			t.Fatalf("legacy-only fields: readiness=%d, clock=%q, err=%v", readiness, clock, err)
		}
	}
	check(legacy, 88, 111, 9, 901, 0, 0)
	candidate, err := NewLocalProfileGraphReader(ctx, dsn, source, graph)
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()
	check(candidate, 50, 60, 7, 135, 70, 65)
	// An actual NULL in the graph overrides even a non-null legacy reading.
	rows, err := candidate.LoadAutonomicRows("2026-01-02", "2026-01-02")
	if err != nil || len(rows) != 1 || rows[0].HRV != nil {
		t.Fatalf("missing graph HRV = %+v, err=%v", rows, err)
	}
	_, err = candidate.pool.Exec(ctx, `UPDATE settings SET value=value WHERE false`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "25006" {
		t.Fatalf("graph reader persistent write: want read-only denial, got %v", err)
	}
	peer := os.Getenv("PROFILE_PEER_SCHEMA")
	if peer == "" {
		t.Fatal("missing isolated peer schema")
	}
	foreign, err := NewLocalProfileGraphReader(ctx, dsn, source, peer)
	if err == nil {
		foreign.Close()
		t.Fatal("peer graph accepted by the local profile reader")
	}
	check(legacy, 88, 111, 9, 901, 0, 0)
}
