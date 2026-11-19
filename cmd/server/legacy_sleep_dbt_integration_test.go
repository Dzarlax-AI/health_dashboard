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
	accept := func(schema string, body []byte) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/health", strings.NewReader(string(body)))
		req.Header.Set("X-API-Key", schema+"-key")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s sync: %d %s", schema, w.Code, w.Body.String())
		}
	}
	awaitSync := func() {
		t.Helper()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	post := func(schema string, body []byte) {
		t.Helper()
		accept(schema, body)
		awaitSync()
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
	checkParity := func() int {
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
		if briefA.ReadinessServing == nil || len(briefA.ReadinessServing.Components) == 0 ||
			!reflect.DeepEqual(briefA.ReadinessServing, briefB.ReadinessServing) ||
			!reflect.DeepEqual(briefA.ReadinessComponents, briefB.ReadinessComponents) {
			t.Fatalf("readiness serving components mismatch: %+v vs %+v", briefA.ReadinessServing, briefB.ReadinessServing)
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
		var latestBank int
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
			if offset < 0 {
				continue
			}
			for i, view := range []struct {
				brief *health.BriefingResponse
				snap  *storage.LatestEnergySnapshot
				bank  storage.BankResult
			}{{briefA, snapA, bankA}, {briefB, snapB, bankB}} {
				if view.snap.Bank != view.bank.Bank || view.snap.DrainDelta != view.bank.TodayDrain ||
					view.snap.RestoreDelta != view.bank.TodayRestore || view.snap.FormulaVersion != view.bank.FormulaVersion {
					t.Fatalf("%s: persisted snapshot %+v disagrees with current calculation %+v", schemas[i], view.snap, view.bank)
				}
				if view.brief.EnergyBank == nil || view.brief.EnergyBank.Current != max(0, min(100, view.snap.Bank)) {
					t.Fatalf("%s: briefing bank %+v disagrees with persisted snapshot %+v", schemas[i], view.brief.EnergyBank, view.snap)
				}
			}
			latestBank = snapA.Bank
			t.Logf("today readiness=%d (%s) calculated and persisted bank=%d, recovery=%d, drain=%d, state=%s",
				briefA.ReadinessScore, briefA.ReadinessBand, snapA.Bank, snapA.RestoreDelta, snapA.DrainDelta, bankA.State)
		}
		return latestBank
	}
	post("rollout_b", first)
	post("rollout_a", first)
	initialBank := checkParity()
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
	accepted := func(id int) string {
		return read(`SELECT coalesce(max(md5(payload::text)), '') FROM rollout_a.health_records
			WHERE processing_status='complete' AND id=$1`, id)
	}
	snapshots := func() string {
		return read(`SELECT string_agg(to_jsonb(s)::text || ':' || xmin::text, '|' ORDER BY ts_bucket)
			FROM rollout_a.energy_snapshots s`)
	}
	firstDigest, snapshotsBefore := accepted(1), snapshots()
	correction := payload(
		metric("sleep_analysis", []map[string]any{staged(day(0), "00:00:00", "Apple Watch", 5.5)}),
		metric("sleep_total", []map[string]any{point(day(-1), "RingConn", 7.25)}),
		metric("sleep_unspecified", []map[string]any{point(day(-1), "RingConn", 7.25)}),
	)

	// Hold the first daily write after the worker selected dbt. Updating the
	// setting while PostgreSQL reports the waiter gives a deterministic boundary.
	const syncBarrier = 280641
	if _, err := root.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION rollout_a.block_dbt_daily_write() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
		  IF current_query() LIKE '%%legacy_sleep_input AS%%' THEN
		    PERFORM pg_advisory_xact_lock(%d::bigint);
		  END IF;
		  RETURN NEW;
		END $$;
		CREATE TRIGGER block_dbt_daily_write BEFORE INSERT OR UPDATE ON rollout_a.daily_scores
		FOR EACH ROW EXECUTE FUNCTION rollout_a.block_dbt_daily_write()`, syncBarrier)); err != nil {
		t.Fatal(err)
	}
	lock, err := root.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.Background())
	if _, err := lock.Exec(ctx, `SELECT pg_advisory_xact_lock($1::bigint)`, int64(syncBarrier)); err != nil {
		t.Fatal(err)
	}
	accept("rollout_a", correction)
	waitDeadline := time.Now().Add(5 * time.Second)
	for read(`SELECT count(*)::text FROM pg_locks WHERE database =
		(SELECT oid FROM pg_database WHERE datname = current_database()) AND locktype='advisory'
		AND classid=0 AND objid=$1 AND objsubid=1 AND NOT granted`, syncBarrier) != "1" {
		if time.Now().After(waitDeadline) {
			t.Fatal("dbt daily writer did not reach the rollback barrier")
		}
		time.Sleep(10 * time.Millisecond)
	}
	correctionDigest := accepted(2)
	if firstDigest == "" || correctionDigest == "" {
		t.Fatal("both /health packets must be durable before rollback")
	}
	if err := dbs["rollout_a"].SaveSettings(map[string]string{storage.LegacySleepDBTSetting: "false"}); err != nil {
		t.Fatal(err)
	}
	if accepted(1) != firstDigest || accepted(2) != correctionDigest || snapshots() != snapshotsBefore {
		t.Fatal("rollback changed accepted packets or snapshots while dbt was waiting")
	}
	if err := lock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	awaitSync()
	post("rollout_b", correction)
	if got := checkParity(); got == initialBank {
		t.Fatalf("expected the correction to change the bank from %d", initialBank)
	}
	if got := dailySleep("rollout_a"); got != "5.5/0.75/-,7.25/-/7.25,7/0.75/-,6.5/-/6.5,8/1/-" || got != dailySleep("rollout_b") {
		t.Fatalf("in-flight dbt pass did not finish both dates: %s", got)
	}
	if got := probe("rollout_a"); got != "7/0" {
		t.Fatalf("both writes in the active pass should retain dbt, got %s", got)
	}

	next := payload(metric("sleep_analysis", []map[string]any{staged(day(0), "00:00:00", "Apple Watch", 6)}))
	post("rollout_a", next)
	post("rollout_b", next)
	checkParity()
	if got := dailySleep("rollout_a"); got != "6/0.75/-,7.25/-/7.25,7/0.75/-,6.5/-/6.5,8/1/-" || got != dailySleep("rollout_b") {
		t.Fatalf("next correction did not reach legacy daily output: %s", got)
	}
	if got := probe("rollout_a"); got != "7/1" {
		t.Fatalf("expected the next sync to use the Go writer after rollback, got %s", got)
	}
	if accepted(1) != firstDigest || accepted(2) != correctionDigest || accepted(3) == "" {
		t.Fatal("rollback lost or modified accepted raw packets")
	}

	for _, schema := range schemas {
		if observed(schema) != original[schema] {
			t.Fatalf("historical observed snapshot was rewritten for %s", schema)
		}
	}
}
