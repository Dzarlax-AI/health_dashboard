package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/handler"
	"health-receiver/internal/health"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
	"health-receiver/internal/testdb"
)

type sleepRolloutRegistry map[string]*registry.User

func (r sleepRolloutRegistry) GetBySchema(_ context.Context, name string) (*registry.User, error) {
	if user := r[name]; user != nil {
		return user, nil
	}
	return nil, pgx.ErrNoRows
}
func (r sleepRolloutRegistry) GetByAPIKey(_ context.Context, key string) (*registry.User, error) {
	for _, user := range r {
		if user.APIKey == key {
			return user, nil
		}
	}
	return nil, pgx.ErrNoRows
}
func (r sleepRolloutRegistry) GetByUsername(ctx context.Context, name string) (*registry.User, error) {
	return r.GetBySchema(ctx, name)
}
func (r sleepRolloutRegistry) GetByEmail(context.Context, string) (*registry.User, error) {
	return nil, pgx.ErrNoRows
}
func (r sleepRolloutRegistry) GetAllGlobalSettings(context.Context) map[string]string {
	return nil
}

func newSleepRolloutFixture(t *testing.T) (*tenants.Manager, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	dsn := testdb.DSN(t)
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(config.ConnConfig.Host) || len(config.ConnConfig.Fallbacks) != 0 {
		t.Skip("server sleep rollout rehearsal requires a disposable local Unix socket")
	}
	admin, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	name := pgx.Identifier{testdb.SchemaName("sleep_rollout_server")}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP DATABASE "+name); err != nil {
			t.Error(err)
		}
	})
	cfg := config.Copy()
	cfg.ConnConfig.Database = strings.Trim(name, "\"")
	root, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := root.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	base := fmt.Sprintf("host=%s port=%d dbname=%s sslmode=disable", cfg.ConnConfig.Host, cfg.ConnConfig.Port, cfg.ConnConfig.Database)
	deriver := tenants.CredentialDeriver{Current: tenants.SecretVersion{Version: 1, Secret: []byte("synthetic-rollout-fixture-32-byte-secret")}}
	metadata := sleepRolloutRegistry{}
	for _, schema := range []string{"rollout_a", "rollout_b"} {
		id := uuid.New()
		role := tenants.TenantRoleName(id)
		password, err := deriver.Derive(id, role, 1)
		if err != nil {
			t.Fatal(err)
		}
		roleSQL := pgx.Identifier{role}.Sanitize()
		exec("CREATE ROLE " + roleSQL + " LOGIN PASSWORD '" + strings.ReplaceAll(password, "'", "''") + "'")
		t.Cleanup(func() {
			exec("DROP SCHEMA " + pgx.Identifier{schema}.Sanitize() + " CASCADE")
			exec("DROP OWNED BY " + roleSQL)
			exec("DROP ROLE " + roleSQL)
		})
		exec("CREATE SCHEMA " + pgx.Identifier{schema}.Sanitize() + " AUTHORIZATION " + roleSQL)
		db, err := storage.NewRestrictedTenant(ctx, base, role, password, schema)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.MigrateSchemaContract(); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := db.EnsureTenantIdentity(ctx, id, uuid.New()); err != nil {
			db.Close()
			t.Fatal(err)
		}
		db.Close()
		metadata[schema] = &registry.User{Username: schema, SchemaName: schema, APIKey: schema + "-key", TenantID: id, DBRole: role,
			DBCredentialVersion: 1, DBIsolationReady: true, ProvisioningState: registry.ProvisioningStateActive,
			SchemaContractVersion: storage.SchemaContractVersion, SchemaContractChecksum: storage.SchemaContractChecksum()}
	}
	mgr, err := tenants.NewIsolated(metadata, base, deriver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	return mgr, root
}

func TestSleepRolloutServerIntegration(t *testing.T) {
	ctx := context.Background()
	mgr, root := newSleepRolloutFixture(t)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := root.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	read := func(sql string, args ...any) string {
		t.Helper()
		var out string
		if err := root.QueryRow(ctx, sql, args...).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	a, _, _, ok := mgr.DBForAPIKey(ctx, "rollout_a-key")
	if !ok {
		t.Fatal("isolated profile did not resolve")
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	oldDay := today.AddDate(0, 0, -31)
	newDay := oldDay.AddDate(0, 0, 1)
	exec(`INSERT INTO rollout_a.daily_scores(date,sleep_total,sleep_core,sleep_deep,sleep_rem,sleep_awake,hrv_avg,rhr_avg,calories)
		SELECT (date ` + "'" + today.Format(time.DateOnly) + "'" + `+i)::text,8,6,1,1,0,50,57,550 FROM generate_series(-65,0) i`)
	observedAt := newDay.Add(23*time.Hour + 55*time.Minute)
	if err := a.UpsertEnergySnapshotAt(ctx, "UTC", observedAt, storage.BankResult{Bank: 73, FormulaVersion: 3}); err != nil {
		t.Fatal(err)
	}
	observed := func() string {
		return read(`SELECT to_jsonb(s)::text||':'||xmin::text FROM rollout_a.energy_snapshots s WHERE ts_bucket=$1`, observedAt)
	}
	originalObservation := observed()
	packetFor := func(wakeBase time.Time, source, id string, rev int, zone string, duration time.Duration) string {
		t.Helper()
		start := wakeBase.Add(16*time.Hour + 30*time.Minute)
		body, err := json.Marshal(map[string]any{"source_id": source, "candidate": map[string]any{
			"id": id, "revision": rev, "wake_zone": zone, "zero_stages": []string{"awake"},
			"stages": []map[string]any{
				{"id": "core", "kind": "core", "start_at": start, "end_at": start.Add(duration - 2*time.Hour)},
				{"id": "deep", "kind": "deep", "start_at": start.Add(duration - 2*time.Hour), "end_at": start.Add(duration - time.Hour)},
				{"id": "rem", "kind": "rem", "start_at": start.Add(duration - time.Hour), "end_at": start.Add(duration)},
			},
		}, "legacy_payload": map[string]any{"data": map[string]any{"metrics": []map[string]any{
			{"name": "sleep_total", "units": "hr", "data": []map[string]any{
				{"date": wakeBase.Format(time.DateOnly) + " 00:00:00 +0000", "qty": 6, "source": "Apple Watch"},
			}},
		}}}})
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	packet := func(rev int, zone string, duration time.Duration) string {
		return packetFor(oldDay, "watch", "night-1", rev, zone, duration)
	}
	muxFor := func(ctx context.Context, enabled bool) *http.ServeMux {
		runCtx, cancel := context.WithCancel(ctx)
		mux := http.NewServeMux()
		legacy := handler.New(mgr, func(*storage.DB, []string, bool) {}, health.HRZones{})
		t.Cleanup(func() {
			cancel()
			if err := legacy.Shutdown(context.Background()); err != nil {
				t.Error(err)
			}
		})
		registerHealthIngest(runCtx, mux, mgr, legacy, enabled)
		return mux
	}
	do := func(mux *http.ServeMux, method, route, key, body string, status int) string {
		t.Helper()
		req := httptest.NewRequest(method, route, strings.NewReader(body))
		req.Header.Set("X-API-Key", key)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, route, w.Code, w.Body.String())
		}
		return strings.TrimSpace(w.Body.String())
	}
	const route = "/health/sleep/v2"
	do(muxFor(ctx, false), "POST", route, "rollout_a-key", packet(1, "UTC", 6*time.Hour), 404)
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	mux := muxFor(workerCtx, true)
	do(mux, "POST", route, "rollout_a-key", packet(1, "UTC", 6*time.Hour), 404)
	if err := a.SaveSettings(map[string]string{storage.SleepRolloutModeSetting: "prepare", "timezone": "UTC", "sleep_rollout_source_priority": `["ring","watch"]`}); err != nil {
		t.Fatal(err)
	}
	do(mux, "POST", route, "rollout_b-key", packet(1, "UTC", 6*time.Hour), 404)
	do(mux, "POST", route, "wrong", packet(1, "UTC", 6*time.Hour), 401)
	if status := do(mux, "POST", route, "rollout_a-key", packet(1, "UTC", 6*time.Hour), 202); status != `{"status":"accepted"}` {
		t.Fatal(status)
	}
	poll := func(expected string, sql string, args ...any) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for {
			if got := read(sql, args...); got == expected {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("expected %q, got %q", expected, read(sql, args...))
			}
			time.Sleep(35 * time.Millisecond)
		}
	}
	poll("1", `SELECT count(*)::text FROM rollout_a.sleep_rollout_packets WHERE applied_at IS NOT NULL`)
	do(mux, "POST", route, "rollout_a-key", packet(1, "UTC", 6*time.Hour), 200)
	do(mux, "POST", route, "rollout_a-key", packet(1, "UTC", 7*time.Hour), 409)
	// An already preferred source occupies the destination under the old policy.
	do(mux, "POST", route, "rollout_a-key", packetFor(newDay, "ring", "ring-night", 1, "UTC", 7*time.Hour), 202)
	poll("2", `SELECT count(*)::text FROM rollout_a.sleep_rollout_packets WHERE applied_at IS NOT NULL`)
	if got := read(`SELECT sleep_seconds::int::text FROM rollout_a.sleep_rollout_daily WHERE wake_date=$1::date`, newDay); got != "25200" {
		t.Fatal(got)
	}
	if err := a.SaveSettings(map[string]string{"sleep_rollout_source_priority": `["watch","ring"]`}); err != nil {
		t.Fatal(err)
	}
	// Enqueue tomorrow before today's eligible correction to catch head-of-line blocking.
	tomorrow := today.AddDate(0, 0, 1)
	do(mux, "POST", route, "rollout_a-key", packetFor(today, "watch", "travel-tomorrow", 1, "Asia/Singapore", 6*time.Hour), 202)

	exec(`CREATE FUNCTION rollout_a.fail_rebuild() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'temporary synthetic failure'; END; $$;
		CREATE TRIGGER fail_rebuild BEFORE INSERT OR UPDATE ON rollout_a.sleep_rollout_results
		FOR EACH ROW EXECUTE FUNCTION rollout_a.fail_rebuild()`)
	do(mux, "POST", route, "rollout_a-key", packet(2, "Asia/Singapore", 6*time.Hour), 202)
	if ran, err := a.RunSleepRolloutNext(ctx, time.Now()); err == nil || ran {
		t.Fatalf("failure injection unexpectedly committed: %v, %v", ran, err)
	}
	if got := read(`SELECT count(*)::text FROM rollout_a.sleep_rollout_daily WHERE generation=4`); got != "0" {
		t.Fatal("partial transfer escaped failed transaction")
	}
	poll("1", `SELECT count(*)::text FROM rollout_a.sleep_rollout_packets WHERE revision=2 AND applied_at IS NULL`)
	exec("DROP TRIGGER fail_rebuild ON rollout_a.sleep_rollout_results")
	poll("3", `SELECT count(*)::text FROM rollout_a.sleep_rollout_packets WHERE applied_at IS NOT NULL`)
	owned := read(`SELECT string_agg(wake_date::text||'='||COALESCE(sleep_seconds::text,'missing'),',' ORDER BY wake_date)
		FROM rollout_a.sleep_rollout_daily`)
	if want := oldDay.Format(time.DateOnly) + "=missing," + newDay.Format(time.DateOnly) + "=21600"; owned != want {
		t.Fatalf("daily: got %s want %s", owned, want)
	}
	// The destination's final result, not just its row, reaches D+30.
	if got := read(`SELECT COALESCE(max(generation),0)::text FROM rollout_a.sleep_rollout_results WHERE day=$1::date`, today); got != "4" {
		t.Fatalf("destination D+30 generation: %s, want 4", got)
	}
	if got := read(`SELECT count(*)::text FROM rollout_a.sleep_rollout_packets WHERE candidate_id='travel-tomorrow' AND applied_at IS NULL`); got != "1" {
		t.Fatal("future receipt was not deferred")
	}
	if got := read(`SELECT count(*)::text FROM rollout_a.sleep_rollout_daily WHERE wake_date=$1::date`, tomorrow); got != "0" {
		t.Fatal("future receipt changed the vitrine before midnight")
	}
	b, _, _, ok := mgr.DBForAPIKey(ctx, "rollout_b-key")
	if !ok {
		t.Fatal("control tenant did not resolve")
	}
	exec(`INSERT INTO rollout_b.daily_scores SELECT * FROM rollout_a.daily_scores`)
	exec(`UPDATE rollout_b.daily_scores SET sleep_total=NULL,sleep_core=NULL,sleep_deep=NULL,sleep_rem=NULL,sleep_awake=NULL WHERE date=$1`, oldDay.Format(time.DateOnly))
	exec(`UPDATE rollout_b.daily_scores SET sleep_total=6,sleep_core=4,sleep_deep=1,sleep_rem=1,sleep_awake=0 WHERE date=$1`, newDay.Format(time.DateOnly))
	for _, day := range []time.Time{oldDay, newDay} {
		date := day.Format(time.DateOnly)
		exec(`TRUNCATE rollout_b.hourly_metrics`)
		exec(`INSERT INTO rollout_b.hourly_metrics(metric_name,hour,source,avg_val,sample_count,min_val,max_val)
			VALUES('step_count',$1,'synthetic',10,1,10,10)`, date+" 08")
		wantReadiness, err := b.GetHealthBriefing("en")
		if err != nil || wantReadiness == nil || wantReadiness.Date != date {
			t.Fatalf("independent readiness fixture for %s: %v", date, err)
		}
		var gotReadiness storage.LocalShadowReadiness
		if err := json.Unmarshal([]byte(read(`SELECT readiness::text FROM rollout_a.sleep_rollout_results WHERE day=$1::date AND generation=4`, day)), &gotReadiness); err != nil {
			t.Fatal(err)
		}
		if gotReadiness.Score != wantReadiness.ReadinessDisplayScore || gotReadiness.RawScore != wantReadiness.ReadinessRawScore ||
			gotReadiness.Band != wantReadiness.ReadinessBand || gotReadiness.Serving.Status != wantReadiness.ReadinessServing.Status {
			t.Fatalf("readiness for %s: got %d/%d %s %s, independent %d/%d %s %s", date,
				gotReadiness.RawScore, gotReadiness.Score, gotReadiness.Band, gotReadiness.Serving.Status,
				wantReadiness.ReadinessRawScore, wantReadiness.ReadinessDisplayScore, wantReadiness.ReadinessBand, wantReadiness.ReadinessServing.Status)
		}
		if sleep := gotReadiness.Sleep; sleep == nil || (day.Equal(oldDay) && sleep.Total != nil) ||
			(day.Equal(newDay) && (sleep.Total == nil || *sleep.Total != 6 || sleep.Awake == nil || *sleep.Awake != 0)) {
			t.Fatalf("sleep evidence for %s: %+v", date, sleep)
		}
		wantEnergy, err := b.ComputeBankForDate(ctx, "UTC", date)
		if err != nil {
			t.Fatal(err)
		}
		var gotEnergy storage.BankResult
		if err := json.Unmarshal([]byte(read(`SELECT energy::text FROM rollout_a.sleep_rollout_results WHERE day=$1::date AND generation=4`, day)), &gotEnergy); err != nil {
			t.Fatal(err)
		}
		if gotEnergy.State == "stale" || !reflect.DeepEqual(gotEnergy, wantEnergy) {
			t.Fatalf("EnergyBank for %s: got %+v, independent %+v", date, gotEnergy, wantEnergy)
		}
		t.Logf("%s: readiness %d/%d %s %s, EnergyBank %d %s; independent Go matched", date,
			gotReadiness.RawScore, gotReadiness.Score, gotReadiness.Band, gotReadiness.Serving.Status, gotEnergy.Bank, gotEnergy.State)
	}
	if got := read(`SELECT count(*)::text FROM rollout_b.sleep_rollout_packets`); got != "0" {
		t.Fatal(got)
	}
	if got := read(`SELECT count(*)::text FROM rollout_a.energy_snapshots`); got != "1" || observed() != originalObservation {
		t.Fatal("prepare wrote legacy snapshots")
	}
	if got := read(`SELECT sleep_total::text FROM rollout_a.daily_scores WHERE date=$1`, oldDay.Format(time.DateOnly)); got != "8" {
		t.Fatal("legacy path changed during prepare")
	}
	cancel()
	if err := a.SaveSettings(map[string]string{"timezone": "Pacific/Honolulu"}); err != nil {
		t.Fatal(err)
	}
	// Honolulu's new local date begins at 10:00 UTC, well after UTC midnight.
	localMidnight := tomorrow.Add(10 * time.Hour)
	if ran, err := a.RunSleepRolloutNext(ctx, localMidnight.Add(-time.Nanosecond)); err != nil || ran {
		t.Fatalf("future receipt ran before local midnight: %v %v", ran, err)
	}
	if ran, err := a.RunSleepRolloutNext(ctx, localMidnight); err != nil || !ran {
		t.Fatalf("future receipt unavailable on local date: %v %v", ran, err)
	}
	if got := read(`SELECT count(*)::text FROM rollout_a.sleep_rollout_results WHERE day=$1::date
		AND readiness->'sleep'->>'total_hours'='6' AND energy->>'State' IS NOT NULL`, tomorrow); got != "1" {
		t.Fatal("deferred receipt didn't prepare both metrics on its wake date")
	}
	t.Log("D+30 recalculated, changed priority selected watch over previously preferred ring, tomorrow receipt applied at Honolulu midnight")
	if err := a.SaveSettings(map[string]string{"timezone": "UTC"}); err != nil {
		t.Fatal(err)
	}

	pausedCtx, pause := context.WithCancel(ctx)
	pause()
	pausedMux := muxFor(pausedCtx, true)
	do(pausedMux, "POST", route, "rollout_a-key", packet(3, "Asia/Singapore", 7*time.Hour), 202)
	if err := a.SaveSettings(map[string]string{storage.SleepRolloutModeSetting: "off"}); err != nil {
		t.Fatal(err)
	}
	do(mux, "POST", route, "rollout_a-key", packet(4, "Asia/Singapore", 7*time.Hour), 404)
	do(mux, "GET", "/health", "rollout_a-key", "", 200)
	oldPacket := fmt.Sprintf(`{"data":{"metrics":[{"name":"step_count","units":"count","data":[{"date":"%s 07:00:00 +0000","qty":42,"source":"synthetic"}]}]}}`, today.Format(time.DateOnly))
	do(mux, "POST", "/health", "rollout_a-key", oldPacket, 200)
	poll("1", `SELECT count(*)::text FROM rollout_a.health_records WHERE processing_status='complete'`)
	if observed() != originalObservation {
		t.Fatal("disabling candidate preparation or accepting a legacy sync rewrote the observation")
	}
	if processed, err := a.RunSleepRolloutNext(ctx, time.Now()); err != nil || processed {
		t.Fatalf("disabled worker ran: %v %v", processed, err)
	}
	if got := read(`SELECT count(*)::text FROM rollout_a.sleep_rollout_packets WHERE applied_at IS NULL`); got != "1" {
		t.Fatal(got)
	}

	if err := a.SaveSettings(map[string]string{storage.SleepRolloutModeSetting: "prepare"}); err != nil {
		t.Fatal(err)
	}
	restart, restartCancel := context.WithCancel(ctx)
	defer restartCancel()
	_ = muxFor(restart, true)
	poll("0", `SELECT count(*)::text FROM rollout_a.sleep_rollout_packets WHERE applied_at IS NULL`)
	if got := read(`SELECT sleep_seconds::text FROM rollout_a.sleep_rollout_daily WHERE wake_date=$1::date`, newDay); got != "25200" {
		t.Fatal(got)
	}
	t.Logf("default off; two roles; %s cleared, %s moved; off held receipt and restarted worker applied it", oldDay.Format(time.DateOnly), newDay.Format(time.DateOnly))
}
