package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"health-receiver/internal/handler"
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

func TestLegacySleepDBTSyncParityAndRollback(t *testing.T) {
	t.Setenv("HEALTH_LEGACY_SLEEP_DBT_ENABLED", "1")
	t.Setenv("REPORT_TZ", "UTC")
	mgr, root := newSleepRolloutFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	day := func(offset int) string { return today.AddDate(0, 0, offset).Format(time.DateOnly) }
	read := func(sql string, args ...any) string {
		t.Helper()
		var result string
		if err := root.QueryRow(ctx, sql, args...).Scan(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	schemas := []string{"rollout_a", "rollout_b"}
	dbs := make(map[string]*storage.DB)
	derived := storage.NewTodayDerivedStateCoordinator(storage.NewEnergyV2Orchestrator())
	refresh := make(map[*storage.DB]func([]string, bool))
	observed := func(schema string) string {
		return read(`SELECT to_jsonb(s)::text || ':' || xmin::text FROM `+schema+
			`.energy_snapshots s WHERE ts_bucket=$1`, today.Add(-10*time.Hour))
	}
	original := make(map[string]string)
	for _, schema := range schemas {
		db, _, _, ok := mgr.DBForAPIKey(ctx, schema+"-key")
		if !ok {
			t.Fatalf("could not open isolated tenant %s", schema)
		}
		dbs[schema] = db
		if err := db.SaveSettings(map[string]string{"timezone": "UTC"}); err != nil {
			t.Fatal(err)
		}
		if _, err := root.Exec(ctx, `INSERT INTO `+schema+`.daily_scores
			(date,sleep_total,sleep_core,sleep_deep,sleep_rem,sleep_awake,hrv_avg,rhr_avg,calories)
			SELECT ($1::date+i)::text,8,5.8,1,1.2,0.3,55,57,550 FROM generate_series(-65,0) i`, day(0)); err != nil {
			t.Fatal(err)
		}
		role := read(`SELECT quote_ident(pg_get_userbyid(nspowner)) FROM pg_namespace WHERE nspname=$1`, schema)
		if _, err := root.Exec(ctx, `CREATE TABLE `+schema+`.sleep_writer_probe (dbt boolean NOT NULL);
			GRANT INSERT ON `+schema+`.sleep_writer_probe TO `+role+`;
			CREATE FUNCTION `+schema+`.record_sleep_writer() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
			  IF current_query() LIKE '%sleep_picked_complete AS%' THEN
			    INSERT INTO `+schema+`.sleep_writer_probe VALUES (current_query() LIKE '%legacy_sleep_input AS%');
			  END IF;
			  RETURN NEW;
			END $$;
			CREATE TRIGGER record_sleep_writer AFTER INSERT OR UPDATE ON `+schema+`.daily_scores
			FOR EACH ROW EXECUTE FUNCTION `+schema+`.record_sleep_writer()`); err != nil {
			t.Fatal(err)
		}
		if err := db.UpsertEnergySnapshotAt(ctx, "UTC", today.Add(-10*time.Hour), storage.BankResult{Bank: 73, FormulaVersion: 3}); err != nil {
			t.Fatal(err)
		}
		original[schema] = observed(schema)
		refresh[db] = makeTodayDerivedStateRefresh(ctx, db, schema, storage.NotifyConfig{}, derived, func() storage.AIConfig { return storage.AIConfig{} })
	}
	if err := dbs["rollout_a"].SaveSettings(map[string]string{storage.LegacySleepDBTSetting: "true"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	done := make(chan error, 1)
	ingest := handler.New(mgr, func(db *storage.DB, dates []string, ready bool) {
		refresh[db](dates, ready)
		if !ready {
			done <- fmt.Errorf("inline cache failed for %v", dates)
			return
		}
		done <- derived.WaitForIdle(ctx, db)
	}, health.HRZones{})
	defer func() {
		if err := ingest.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	ingest.Register(mux)
	post := func(schema string, body []byte) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/health", strings.NewReader(string(body)))
		req.Header.Set("X-API-Key", schema+"-key")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s sync: %d %s", schema, w.Code, w.Body.String())
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	metric := func(name string, data []map[string]any) map[string]any {
		return map[string]any{"name": name, "units": "hr", "data": data}
	}
	staged := func(date, clock, source string, total float64) map[string]any {
		return map[string]any{"date": date + " " + clock + " +0000", "source": source, "totalSleep": total,
			"core": total - 2, "rem": 1.25, "deep": 0.75, "awake": 0.5}
	}
	point := func(date, source string, qty float64) map[string]any {
		return map[string]any{"date": date + " 00:00:00 +0000", "source": source, "qty": qty}
	}
	payload := func(metrics ...map[string]any) []byte {
		t.Helper()
		body, err := json.Marshal(map[string]any{"data": map[string]any{"metrics": metrics}})
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	first := payload(
		metric("sleep_analysis", []map[string]any{
			staged(day(0), "00:00:00", "Apple Watch", 10),
			staged(day(-2), "00:00:00", "Apple Watch Ultra", 7),
			staged(day(-2), "02:00:00", "Apple Watch Ultra", 2.5),
			staged(day(-4), "00:00:00", "RingConn", 7),
		}),
		metric("sleep_total", []map[string]any{
			point(day(0), "RingConn", 6), point(day(-1), "RingConn", 7),
			point(day(-2), "RingConn", 7.5), point(day(-3), "iPhone", 6.5),
			point(day(-4), "Apple Watch", 7.5),
		}),
		metric("sleep_unspecified", []map[string]any{
			point(day(0), "RingConn", 6), point(day(-1), "RingConn", 7),
			point(day(-2), "RingConn", 7.5), point(day(-3), "iPhone", 6.5),
		}),
	)
	checkParity := func() {
		t.Helper()
		a, b := dbs["rollout_a"], dbs["rollout_b"]
		briefA, err := a.GetHealthBriefing("en")
		if err != nil {
			t.Fatal(err)
		}
		briefB, err := b.GetHealthBriefing("en")
		if err != nil {
			t.Fatal(err)
		}
		if briefA.ReadinessScore != briefB.ReadinessScore || briefA.ReadinessToday != briefB.ReadinessToday ||
			briefA.ReadinessBand != briefB.ReadinessBand || briefA.ReadinessTodayBand != briefB.ReadinessTodayBand ||
			!reflect.DeepEqual(briefA.SleepQuality, briefB.SleepQuality) ||
			briefA.EnergyBankStatus != briefB.EnergyBankStatus || !reflect.DeepEqual(briefA.EnergyBank, briefB.EnergyBank) {
			t.Fatalf("live briefing mismatch for %s", day(0))
		}
		if briefA.EnergyBank == nil {
			t.Fatal("live briefing has no current bank")
		}
		snapA, err := a.GetLatestEnergySnapshotForDate(ctx, day(0))
		if err != nil {
			t.Fatal(err)
		}
		snapB, err := b.GetLatestEnergySnapshotForDate(ctx, day(0))
		if err != nil || snapA == nil || !reflect.DeepEqual(snapA, snapB) {
			t.Fatalf("live EnergyBank snapshot mismatch: %+v vs %+v; %v", snapA, snapB, err)
		}
		histA, err := a.GetReadinessHistory(5)
		if err != nil {
			t.Fatal(err)
		}
		histB, err := b.GetReadinessHistory(5)
		if err != nil || len(histA) != 5 || !reflect.DeepEqual(histA, histB) {
			t.Fatalf("readiness history mismatch: %v vs %v; %v", histA, histB, err)
		}
		for offset := -4; offset <= 0; offset++ {
			date := day(offset)
			bankA, err := a.ComputeBankForDate(ctx, "UTC", date)
			if err != nil {
				t.Fatal(err)
			}
			bankB, err := b.ComputeBankForDate(ctx, "UTC", date)
			if err != nil || bankA.State == "stale" || !reflect.DeepEqual(bankA, bankB) {
				t.Fatalf("%s bank mismatch: %+v vs %+v; %v", date, bankA, bankB, err)
			}
			if offset == 0 {
				t.Logf("today readiness=%d (%s) bank=%d recovery=%d state=%s", briefA.ReadinessScore,
					briefA.ReadinessBand, bankA.Bank, bankA.TodayRestore, bankA.State)
			}
		}
	}
	post("rollout_b", first)
	post("rollout_a", first)
	checkParity()
	probe := func(schema string) string {
		return read(`SELECT count(*) FILTER (WHERE dbt)::text || '/' ||
			count(*) FILTER (WHERE NOT dbt)::text FROM ` + schema + `.sleep_writer_probe`)
	}
	if a, b := probe("rollout_a"), probe("rollout_b"); a != "5/0" || b != "0/5" {
		t.Fatalf("expected active dbt and control Go writers, got %s and %s", a, b)
	}
	dailySleep := func(schema string) string {
		return read(`SELECT string_agg(concat_ws('/',sleep_total,coalesce(sleep_deep::text,'-'),
			coalesce(sleep_unspecified::text,'-')), ',' ORDER BY date DESC)
			FROM `+schema+`.daily_scores WHERE date BETWEEN $1 AND $2`, day(-4), day(0))
	}
	if got := dailySleep("rollout_a"); got != "6/-/6,7/-/7,7/0.75/-,6.5/-/6.5,8/1/-" || got != dailySleep("rollout_b") {
		t.Fatalf("unexpected sleep blocks: %s", got)
	}
	if got := read(`SELECT string_agg(DISTINCT source,',' ORDER BY source) FROM rollout_a.hourly_metrics WHERE metric_name='sleep_total'`); got != "Apple Watch,Apple Watch Ultra,RingConn,iPhone" {
		t.Fatalf("unexpected source provenance: %s", got)
	}
	accepted := func() string {
		return read(`SELECT string_agg(id::text || ':' || md5(payload::text), ',' ORDER BY id)
			FROM rollout_a.health_records WHERE processing_status='complete' AND id=1`)
	}
	snapshots := func() string {
		return read(`SELECT string_agg(to_jsonb(s)::text || ':' || xmin::text, '|' ORDER BY ts_bucket)
			FROM rollout_a.energy_snapshots s`)
	}
	before, snapshotsBefore := accepted(), snapshots()
	if err := dbs["rollout_a"].SaveSettings(map[string]string{storage.LegacySleepDBTSetting: "false"}); err != nil {
		t.Fatal(err)
	}
	checkParity()
	if snapshots() != snapshotsBefore {
		t.Fatal("turning off dbt rewrote existing EnergyBank snapshots")
	}
	correction := payload(metric("sleep_analysis", []map[string]any{
		staged(day(0), "00:00:00", "Apple Watch", 5.5),
	}))
	post("rollout_a", correction)
	post("rollout_b", correction)
	checkParity()
	if got := dailySleep("rollout_a"); got != "5.5/0.75/-,7/-/7,7/0.75/-,6.5/-/6.5,8/1/-" {
		t.Fatalf("correction after rollback did not reach legacy daily output: %s", got)
	}
	if got := probe("rollout_a"); got != "5/1" {
		t.Fatalf("expected the next sync to use the Go writer after rollback, got %s", got)
	}
	if before == "" || accepted() != before {
		t.Fatal("rollback lost or modified the accepted raw packet")
	}
	for _, schema := range schemas {
		if observed(schema) != original[schema] {
			t.Fatalf("historical observed snapshot was rewritten for %s", schema)
		}
	}
}
