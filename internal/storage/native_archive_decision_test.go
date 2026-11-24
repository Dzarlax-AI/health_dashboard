package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The operator supplies SELECTs compiled by dbt for this restored tenant. All
// captures, flags and worker keys are rolled back. No provider or scheduler runs.
func TestNativeArchiveRetrospectiveDecisionReplay(t *testing.T) {
	dir := os.Getenv("HEALTH_ARCHIVE_DECISION_SQL")
	if dir == "" {
		t.Skip("requires compiled worker SQL and an offline archive")
	}
	if !filepath.IsAbs(os.Getenv("PGHOST")) {
		t.Fatal("archive replay requires a local PostgreSQL socket")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, "")
	if err != nil {
		t.Fatal("open archive")
	}
	defer pool.Close()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal("begin archive replay")
	}
	defer tx.Rollback(context.Background())
	var private bool
	if err = tx.QueryRow(ctx, "select inet_server_addr() is null and not rolsuper and not rolcreatedb from pg_roles where rolname=current_user").Scan(&private); err != nil || !private {
		t.Fatal("requires a restricted local archive connection")
	}
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	var day, zone string
	err = tx.QueryRow(ctx, "select date,coalesce((select value from settings where key='timezone'),'UTC') from daily_scores where date<=current_date::text and (hrv_avg is not null or sleep_total is not null) order by date desc limit 1").Scan(&day, &zone)
	if err != nil {
		t.Fatal("no eligible archive date")
	}
	if _, err = tx.Exec(ctx, "insert into settings(key,value) values($1,'true'),($2,'false') on conflict(key) do update set value=excluded.value", SettingDecisionCapture, SettingDecisionResultReads); err != nil {
		t.Fatal("initialize local capture")
	}
	if _, err = unit.computeReadinessHistoryAt(ctx, 1, day); err != nil {
		t.Fatal("history replay")
	}
	raw := unit.rawMetricsFromDailyScoresAt(day, true)
	if raw == nil {
		t.Fatal("missing same-day inputs")
	}
	projection, err := unit.morningReadinessProjection(ctx, raw)
	if err != nil {
		t.Fatal("morning replay")
	}
	if _, err = unit.ComputeBankForDate(ctx, zone, day); err != nil {
		t.Fatal("energy replay")
	}
	brief, err := unit.computeBriefingSQL(ctx, raw, "en", &projection)
	if err != nil {
		t.Fatal("reconstruct local briefing")
	}
	if err = unit.applyHistoricalEnergySnapshot(ctx, brief, day, "en"); err != nil {
		t.Fatal("read retained energy")
	}
	if _, err = unit.liveDailyDecision(ctx, day, brief); err != nil {
		t.Fatal("today replay")
	}
	if _, err = tx.Exec(ctx, "insert into hdbt_i_decisions(metric,decision_key,input_hash) select metric,decision_key,input_hash from hdbt_decision_inputs where captured_at>=transaction_timestamp() on conflict do nothing"); err != nil {
		t.Fatal("select new decision keys")
	}
	for _, check := range []struct{ file, metric string }{
		{"hdbt_readiness_decisions", "readiness"}, {"worker_energy_decisions", "energy_bank"}, {"worker_today_decisions", "today_decision"},
	} {
		b, err := os.ReadFile(filepath.Join(dir, check.file+".sql"))
		if err != nil {
			t.Fatal("missing compiled model", check.file)
		}
		q := "with candidate as (" + strings.TrimSuffix(strings.TrimSpace(string(b)), ";") + ") " +
			"select count(*),count(*) filter(where (c.result-'Trace') is not distinct from j.result) from hdbt_decision_inputs j left join candidate c using(metric,decision_key,input_hash,formula_version) where j.metric=$1 and j.captured_at>=transaction_timestamp()"
		var recorded, matched int
		if err = tx.QueryRow(ctx, q, check.metric).Scan(&recorded, &matched); err != nil {
			t.Fatal("query compiled model", check.file)
		}
		t.Logf("metric=%s recorded=%d matched=%d", check.metric, recorded, matched)
		if recorded == 0 || matched != recorded {
			t.Errorf("replay differs or has no receipts for %s", check.metric)
		}
	}
}
