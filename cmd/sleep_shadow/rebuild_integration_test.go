package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/sleepshadow"
	"health-receiver/internal/storage"
	"health-receiver/internal/testdb"
)

func useIsolatedRebuildDatabase(t *testing.T, ctx context.Context) {
	t.Helper()
	config, err := pgxpool.ParseConfig(testdb.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	connection := config.ConnConfig
	if !filepath.IsAbs(connection.Host) || len(connection.Fallbacks) != 0 {
		t.Fatal("full rebuild test requires a single local PostgreSQL Unix socket")
	}
	admin, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	name := testdb.SchemaName("shadow_full_rebuild")
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+identifier); err != nil {
			t.Error(err)
		}
	})
	dsn := url.URL{
		Scheme: "postgresql", Host: "localhost", Path: "/" + name,
		User:     url.UserPassword(connection.User, connection.Password),
		RawQuery: url.Values{"host": {connection.Host}, "port": {strconv.Itoa(int(connection.Port))}, "sslmode": {"disable"}}.Encode(),
	}
	t.Setenv("READINESS_TEST_DSN", dsn.String())
}

func TestLocalRebuildAllProfiles(t *testing.T) {
	ctx := context.Background()
	useIsolatedRebuildDatabase(t, ctx)
	const version = "rebuild-test-v1"
	const oldDay, newDay, today = "2026-03-03", "2026-03-04", "2026-03-10"
	now := time.Date(2026, time.March, 10, 8, 7, 0, 0, time.UTC)
	type tenant struct {
		db          *storage.DB
		pool        *pgxpool.Pool
		schema      string
		profile     string
		observed    string
		sleepBefore string
	}
	var tenants []tenant
	bindings := make(map[string]string)
	var shadowPool *pgxpool.Pool
	exec := func(pool *pgxpool.Pool, sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	read := func(pool *pgxpool.Pool, sql string, args ...any) string {
		t.Helper()
		var value string
		if err := pool.QueryRow(ctx, sql, args...).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	observedDigest := func(tenant tenant) string {
		t.Helper()
		return read(tenant.pool, `SELECT jsonb_agg(to_jsonb(s)||jsonb_build_object('xmin',s.xmin::text) ORDER BY ts_bucket)::text
			FROM energy_snapshots s WHERE 'observed_fixture'=ANY(flags)`)
	}
	sleepDigest := func(profile string) string {
		t.Helper()
		return read(shadowPool, `SELECT COALESCE(jsonb_agg(to_jsonb(d) ORDER BY wake_date)::text,'[]')
			FROM sleep_shadow.daily d WHERE profile=$1`, profile)
	}

	for i, profile := range []string{"a-fallback", "b-gap", "c-short"} {
		db, pool, shared, schema := newLocalShadowTestDB(t)
		shadowPool = shared
		bindings[profile] = schema
		entry := tenant{db: db, pool: pool, schema: schema, profile: profile}
		if err := db.SaveSettings(map[string]string{"timezone": "Europe/Belgrade"}); err != nil {
			t.Fatal(err)
		}
		from := "2026-02-12"
		if i == 2 {
			from = "2026-03-08"
		}
		exec(pool, `INSERT INTO daily_scores(date,hrv_avg,rhr_avg,sleep_total,sleep_deep,sleep_rem,sleep_awake,steps,calories,sustained_hr_load)
			SELECT d::date::text,50,57,8,1.5,1.5,0,4000,550,1.5
			FROM generate_series($1::date,$2::date,interval '1 day') d`, from, today)
		handler, err := sleepshadow.New(ctx, shared)
		if err != nil {
			t.Fatal(err)
		}
		post := func(id string, rev, priority int, zone, begin string) {
			t.Helper()
			start, err := time.Parse(time.RFC3339, begin)
			if err != nil {
				t.Fatal(err)
			}
			stages := []map[string]any{
				{"id": id + "1", "kind": "core", "start_at": start, "end_at": start.Add(3 * time.Hour)},
				{"id": id + "2", "kind": "deep", "start_at": start.Add(3 * time.Hour), "end_at": start.Add(4 * time.Hour)},
				{"id": id + "3", "kind": "rem", "start_at": start.Add(4 * time.Hour), "end_at": start.Add(6 * time.Hour)},
			}
			body, err := json.Marshal(map[string]any{"id": id, "revision": rev, "priority": priority, "wake_zone": zone,
				"stages": stages, "zero_stages": []string{"awake"}})
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/profiles/"+profile+"/v2/sleep", strings.NewReader(string(body))))
			if w.Code != 200 || w.Body.String() != "{\"status\":\"applied\"}\n" {
				t.Fatalf("seed %s: %d %s", profile, w.Code, w.Body.String())
			}
		}
		switch i {
		case 0, 1:
			if i == 0 {
				post("fallback", 1, 1, "Europe/Belgrade", oldDay+"T00:00:00Z")
			}
			post("original", 1, 2, "Europe/Belgrade", newDay+"T00:00:00Z")
			post("travel", 1, 0, "Europe/Belgrade", oldDay+"T16:30:00Z")
			post("travel", 2, 0, "Asia/Singapore", oldDay+"T16:30:00Z")
		case 2:
			post("short", 1, 0, "Europe/Belgrade", today+"T00:00:00Z")
		}
		entry.sleepBefore = sleepDigest(profile)
		observations := []time.Time{
			time.Date(2026, time.March, 3, 8, 10, 0, 0, time.UTC),
			time.Date(2026, time.March, 3, 22, 55, 0, 0, time.UTC),
		}
		if i == 2 {
			observations = append(observations, now)
		}
		for _, ts := range observations {
			if err := db.UpsertEnergySnapshotAt(ctx, "Europe/Belgrade", ts,
				storage.BankResult{Bank: 77, FormulaVersion: 1, Flags: []string{"observed_fixture"}}); err != nil {
				t.Fatal(err)
			}
		}
		entry.observed = observedDigest(entry)
		tenants = append(tenants, entry)
	}

	first, second, short := tenants[0], tenants[1], tenants[2]
	exec(shadowPool, `UPDATE sleep_shadow.daily SET stage_conflict=NULL WHERE profile=$1 AND candidate='travel'`, first.profile)
	legacyRow := func() string {
		return read(shadowPool, `SELECT (to_jsonb(d)||jsonb_build_object('xmin',d.xmin::text))::text
			FROM sleep_shadow.daily d WHERE profile=$1 AND candidate='travel'`, first.profile)
	}
	beforeInvalid := legacyRow()
	exec(shadowPool, "ALTER TABLE sleep_shadow.rebuild_checkpoint DROP COLUMN input_fingerprint")
	if _, err := sleepshadow.RebuildAll(ctx, shadowPool, map[string]string{first.profile: first.schema}, version, now); err == nil {
		t.Fatal("incomplete local bindings were accepted")
	}
	if legacyRow() != beforeInvalid || read(shadowPool, `SELECT EXISTS (
		SELECT 1 FROM pg_catalog.pg_attribute WHERE attrelid='sleep_shadow.rebuild_checkpoint'::regclass
		AND attname='input_fingerprint' AND NOT attisdropped)::text`) != "false" {
		t.Fatal("a rejected manifest changed legacy data or installed a migration")
	}
	for _, entry := range tenants {
		exec(shadowPool, "DELETE FROM sleep_shadow.daily WHERE profile=$1", entry.profile)
	}
	exec(second.pool, `CREATE FUNCTION fail_rebuild_energy() RETURNS trigger LANGUAGE plpgsql AS $body$
		BEGIN RAISE EXCEPTION 'synthetic rebuild failure'; END; $body$;
		CREATE TRIGGER fail_rebuild_energy BEFORE INSERT ON energy_snapshots FOR EACH ROW
		WHEN (NEW.date='2026-03-04') EXECUTE FUNCTION fail_rebuild_energy()`)
	results, err := sleepshadow.RebuildAll(ctx, shadowPool, bindings, version, now)
	if err == nil || !strings.Contains(err.Error(), "synthetic rebuild failure") || len(results) != 3 ||
		!results[0].Complete || results[1].Complete || !results[2].Complete {
		t.Fatalf("partial rebuild: %+v, %v", results, err)
	}
	if cursor := read(shadowPool, `SELECT phase||':'||next_date::text FROM sleep_shadow.rebuild_checkpoint
		WHERE profile=$1`, second.profile); cursor != "derived:"+newDay {
		t.Fatalf("unexpected resume point: %s", cursor)
	}
	if failedDay := read(shadowPool, `SELECT count(*)::text FROM sleep_shadow.rebuilt_readiness
		WHERE profile=$1 AND local_date=$2::text::date`, second.profile, newDay); failedDay != "0" {
		t.Fatal("failed date became partially visible")
	}
	derivedDigest := func(entry tenant, through string) string {
		t.Helper()
		sleep := read(shadowPool, `SELECT jsonb_agg(to_jsonb(r)||jsonb_build_object('xmin',r.xmin::text) ORDER BY local_date)::text
			FROM sleep_shadow.rebuilt_readiness r WHERE profile=$1 AND local_date<=$2::text::date`, entry.profile, through)
		energy := read(entry.pool, `SELECT jsonb_agg(to_jsonb(s)||jsonb_build_object('xmin',s.xmin::text) ORDER BY ts_bucket)::text
			FROM energy_snapshots s WHERE date<=$1`, through)
		return sleep + energy
	}
	aFinished, bThrough, cFinished := derivedDigest(first, today), derivedDigest(second, oldDay), derivedDigest(short, today)
	exec(second.pool, "DROP TRIGGER fail_rebuild_energy ON energy_snapshots")
	results, err = sleepshadow.RebuildAll(ctx, shadowPool, bindings, version, now)
	if err != nil || !results[0].Complete || !results[1].Complete || !results[2].Complete ||
		results[0].DerivedDates != 0 || results[1].SleepDates != 0 || results[1].DerivedDates != 7 || results[2].DerivedDates != 0 {
		t.Fatalf("resumed rebuild: %+v %v", results, err)
	}
	if aFinished != derivedDigest(first, today) || bThrough != derivedDigest(second, oldDay) || cFinished != derivedDigest(short, today) {
		t.Fatal("resume rewrote an already committed date or a completed profile")
	}
	for _, entry := range tenants {
		if observedDigest(entry) != entry.observed || sleepDigest(entry.profile) != entry.sleepBefore {
			t.Fatalf("observations or sleep projection diverged for %s", entry.profile)
		}
		bank := read(shadowPool, `SELECT state FROM sleep_shadow.energy_current WHERE profile=$1 AND local_date=$2::text::date`, entry.profile, today)
		if (entry.profile == short.profile) != (bank == "stale") {
			t.Fatalf("unexpected current state for %s: %s", entry.profile, bank)
		}
		var rebuilt storage.LocalShadowReadiness
		payload := read(shadowPool, `SELECT result::text FROM sleep_shadow.rebuilt_readiness
			WHERE profile=$1 AND local_date=$2::text::date`, entry.profile, today)
		live, err := entry.db.GetLocalShadowReadiness(ctx, entry.profile, today)
		if err != nil || json.Unmarshal([]byte(payload), &rebuilt) != nil || !reflect.DeepEqual(&rebuilt, live) {
			t.Fatalf("readiness output differs from live computation for %s", entry.profile)
		}
	}
	bFinished := derivedDigest(second, today)
	results, err = sleepshadow.RebuildAll(ctx, shadowPool, bindings, version, now)
	if err != nil || results[1].DerivedDates != 0 || bFinished != derivedDigest(second, today) ||
		aFinished != derivedDigest(first, today) || cFinished != derivedDigest(short, today) {
		t.Fatalf("finished rebuild was not idempotent: %+v %v", results, err)
	}
	manifest := filepath.Join(t.TempDir(), "bindings.json")
	payload, err := json.Marshal(bindings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, payload, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLEEP_SHADOW_DATABASE_URL", testdb.DSN(t))
	if err := run([]string{"-rebuild-all", manifest, "-rebuild-version", version}, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	if bFinished != derivedDigest(second, today) || aFinished != derivedDigest(first, today) || cFinished != derivedDigest(short, today) {
		t.Fatal("command repeated a finished rebuild")
	}
	swapped := map[string]string{first.profile: second.schema, second.profile: first.schema, short.profile: short.schema}
	if _, err := sleepshadow.RebuildAll(ctx, shadowPool, swapped, version, now); err == nil ||
		!strings.Contains(err.Error(), "recorded schema binding") {
		t.Fatalf("swapped schema bindings were not rejected: %v", err)
	}
	exec(shadowPool, `INSERT INTO sleep_shadow.pending (profile,candidate,revision,wake_date,snapshot)
		SELECT profile,id,revision,wake_date,snapshot FROM sleep_shadow.candidate WHERE profile=$1 AND id='travel'`, second.profile)
	results, err = sleepshadow.RebuildAll(ctx, shadowPool, bindings, version, now)
	if err == nil || !strings.Contains(err.Error(), "sleep packets are pending") || results[1].Complete ||
		bFinished != derivedDigest(second, today) || cFinished != derivedDigest(short, today) {
		t.Fatalf("unresolved pending result: %+v %v", results, err)
	}
	exec(shadowPool, "DELETE FROM sleep_shadow.pending WHERE profile=$1", second.profile)

	correction := read(shadowPool, `SELECT jsonb_set(snapshot,'{revision}','3')::text FROM sleep_shadow.candidate
		WHERE profile=$1 AND id='travel'`, first.profile)
	handler, err := sleepshadow.New(ctx, shadowPool)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/profiles/"+first.profile+"/v2/sleep", strings.NewReader(correction)))
	if w.Code != http.StatusOK || w.Body.String() != "{\"status\":\"applied\"}\n" {
		t.Fatalf("correction between runs: %d %s", w.Code, w.Body.String())
	}
	results, err = sleepshadow.RebuildAll(ctx, shadowPool, bindings, version, now)
	if err != nil || results[0].DerivedDates != 27 || results[1].DerivedDates != 0 || results[2].DerivedDates != 0 ||
		observedDigest(first) != first.observed || bFinished != derivedDigest(second, today) {
		t.Fatalf("changed sleep did not invalidate only the affected profile: %+v %v", results, err)
	}

	const previousDay = "2026-03-09"
	prefixes := []string{derivedDigest(first, previousDay), derivedDigest(second, previousDay), derivedDigest(short, previousDay)}
	results, err = sleepshadow.RebuildAll(ctx, shadowPool, bindings, version, now.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	for i, entry := range tenants {
		if results[i].DerivedDates != 2 || prefixes[i] != derivedDigest(entry, previousDay) || observedDigest(entry) != entry.observed {
			t.Fatalf("midnight extension rewrote the completed history for %s: %+v", entry.profile, results[i])
		}
	}
	t.Log("three tenant schemas completed; failed profile resumed on 2026-03-04; seven observations preserved")

	nextDay := now.AddDate(0, 0, 1)
	measureDrain := func() string {
		return read(first.pool, `SELECT drain_delta::text FROM energy_snapshots WHERE date=$1 AND 'backfilled'=ANY(flags)`, today)
	}
	beforeActivity := measureDrain()
	exec(first.pool, "UPDATE daily_scores SET calories=2200 WHERE date=$1", today)
	if err := short.db.SaveSettings(map[string]string{"energy.alpha": "0.1"}); err != nil {
		t.Fatal(err)
	}
	results, err = sleepshadow.RebuildAll(ctx, shadowPool, bindings, version, nextDay)
	if err != nil || results[0].DerivedDates != 28 || results[1].DerivedDates != 0 || results[2].DerivedDates != 4 ||
		measureDrain() == beforeActivity || observedDigest(first) != first.observed || observedDigest(short) != short.observed {
		t.Fatalf("changes in activity and settings were not isolated to their profiles: %+v %v", results, err)
	}
	t.Logf("same version after a change of inputs recalculated drain %s -> %s", beforeActivity, measureDrain())

	// A change committed while the profile is being rebuilt must be
	// detected before that run reports the profile completed.
	if err := first.db.SaveSettings(map[string]string{"energy.alpha": "0.08"}); err != nil {
		t.Fatal(err)
	}
	exec(first.pool, `CREATE FUNCTION change_rebuild_input() RETURNS trigger LANGUAGE plpgsql AS $body$
		BEGIN
			UPDATE daily_scores SET calories=1500 WHERE date='2026-03-10' AND calories=2200;
			RETURN NEW;
		END; $body$;
		CREATE TRIGGER change_rebuild_input BEFORE INSERT ON energy_snapshots FOR EACH ROW
		WHEN (NEW.date='2026-03-04') EXECUTE FUNCTION change_rebuild_input()`)
	results, err = sleepshadow.RebuildAll(ctx, shadowPool, bindings, version, nextDay)
	if err != nil || !results[0].Complete || results[0].DerivedDates != 56 ||
		results[1].DerivedDates != 0 || results[2].DerivedDates != 0 || observedDigest(first) != first.observed {
		t.Fatalf("input change during rebuild was not reconciled before completion: %+v %v", results, err)
	}
	exec(first.pool, "DROP TRIGGER change_rebuild_input ON energy_snapshots")
	t.Log("input change during the run restarted only the affected profile")

	if err := first.db.SaveSettings(map[string]string{"energy.alpha": "0.09"}); err != nil {
		t.Fatal(err)
	}
	freeSlot := nextDay.Add(5 * time.Minute).Truncate(5 * time.Minute)
	exec(first.pool, `INSERT INTO daily_scores(date,hrv_avg,rhr_avg,sleep_total,sleep_deep,sleep_rem,sleep_awake,steps,calories,sustained_hr_load)
		VALUES ('2026-03-11',50,57,8,1.5,1.5,0,4000,550,1.5)`)
	exec(first.pool, `CREATE TABLE rebuild_setting_changes (day text PRIMARY KEY);
		CREATE FUNCTION revert_rebuild_setting() RETURNS trigger LANGUAGE plpgsql AS $body$
		BEGIN
			INSERT INTO rebuild_setting_changes VALUES (TG_ARGV[0]) ON CONFLICT DO NOTHING;
			IF FOUND THEN
				UPDATE settings SET value=TG_ARGV[1]
				WHERE key='energy.alpha';
			END IF;
			RETURN NEW;
		END; $body$;
		CREATE TRIGGER revert_rebuild_setting BEFORE INSERT ON energy_snapshots FOR EACH ROW
		WHEN (NEW.date='2026-03-04') EXECUTE FUNCTION revert_rebuild_setting('2026-03-04','0.18');
		CREATE TABLE rebuild_observation_failure (enabled boolean);
		INSERT INTO rebuild_observation_failure VALUES (true);
		CREATE FUNCTION fail_unstable_observation() RETURNS trigger LANGUAGE plpgsql AS $body$
		BEGIN
			IF NEW.drain_delta <> 50 OR EXISTS (
				SELECT 1 FROM energy_snapshots WHERE date='2026-03-05' AND 'backfilled'=ANY(flags) AND drain_delta<>50
			) THEN RAISE EXCEPTION 'unstable observation'; END IF;
			IF EXISTS (SELECT 1 FROM rebuild_observation_failure WHERE enabled)
			THEN RAISE EXCEPTION 'synthetic observation publication failure'; END IF;
			RETURN NEW;
		END; $body$;
		CREATE TRIGGER fail_unstable_observation BEFORE INSERT ON energy_snapshots FOR EACH ROW
		WHEN (NEW.date='2026-03-11') EXECUTE FUNCTION fail_unstable_observation()`)
	exec(shadowPool, "CREATE TRIGGER revert_rebuild_setting BEFORE INSERT ON sleep_shadow.rebuilt_readiness FOR EACH ROW "+
		"WHEN (NEW.profile='a-fallback' AND NEW.local_date='2026-03-11') EXECUTE FUNCTION "+
		pgx.Identifier{first.schema, "revert_rebuild_setting"}.Sanitize()+"('2026-03-11','0.09')")
	results, err = sleepshadow.RebuildAll(ctx, shadowPool, bindings, version, freeSlot)
	exec(shadowPool, "DROP TRIGGER revert_rebuild_setting ON sleep_shadow.rebuilt_readiness")
	exec(first.pool, "DROP TRIGGER revert_rebuild_setting ON energy_snapshots")
	revertedDrain := read(first.pool, `SELECT drain_delta::text FROM energy_snapshots
		WHERE date='2026-03-05' AND 'backfilled'=ANY(flags)`)
	if err == nil || !strings.Contains(err.Error(), "synthetic observation publication failure") ||
		results[0].Complete || results[0].DerivedDates != 56 ||
		results[1].DerivedDates != 0 || results[2].DerivedDates != 0 || revertedDrain != "50" ||
		read(first.pool, "SELECT count(*)::text FROM rebuild_setting_changes") != "2" ||
		read(first.pool, "SELECT value FROM settings WHERE key='energy.alpha'") != "0.09" ||
		read(first.pool, "SELECT count(*)::text FROM energy_snapshots WHERE ts_bucket=$1", freeSlot) != "0" ||
		read(shadowPool, "SELECT observation_pending::text FROM sleep_shadow.rebuild_checkpoint WHERE profile=$1", first.profile) != "true" ||
		observedDigest(first) != first.observed {
		t.Fatalf("unstable or failed publication left mixed results: drain=%s, %+v %v", revertedDrain, results, err)
	}
	exec(first.pool, "DELETE FROM rebuild_observation_failure")
	results, err = sleepshadow.RebuildAll(ctx, shadowPool, bindings, version, freeSlot)
	if err != nil || !results[0].Complete || results[0].DerivedDates != 0 {
		t.Fatalf("retry did not resume only the pending observation: %+v %v", results, err)
	}
	snapshotDigest := func() string {
		return read(first.pool, `SELECT to_jsonb(s)::text||':'||s.xmin::text FROM energy_snapshots s WHERE ts_bucket=$1`, freeSlot)
	}
	beforeRepeat := snapshotDigest()
	if drain := read(first.pool, `SELECT drain_delta::text FROM energy_snapshots WHERE ts_bucket=$1`, freeSlot); drain != "50" {
		t.Fatalf("first observation in the free slot used the unstable input: drain=%s", drain)
	}
	results, err = sleepshadow.RebuildAll(ctx, shadowPool, bindings, version, freeSlot)
	if err != nil || !results[0].Complete || results[0].DerivedDates != 0 || snapshotDigest() != beforeRepeat {
		t.Fatalf("repeating the stable run rewrote the observed slot: %+v %v", results, err)
	}
	t.Logf("late reversion repeated only the affected profile; March 5 drain=%s; free slot drain=50 after publication-only retry", revertedDrain)

	if err := first.db.SaveSettings(map[string]string{"timezone": "UTC"}); err != nil {
		t.Fatal(err)
	}
	results, err = sleepshadow.RebuildAll(ctx, shadowPool, bindings, version, freeSlot)
	if err != nil || !results[0].Complete || results[0].DerivedDates != 28 {
		t.Fatalf("rebuild after timezone change: %+v %v", results, err)
	}
	if buckets := read(first.pool, `SELECT COALESCE(string_agg(to_char(ts_bucket AT TIME ZONE 'UTC','HH24:MI'),','),'')
		FROM energy_snapshots WHERE date='2026-03-05' AND 'backfilled'=ANY(flags)`); buckets != "23:55" ||
		observedDigest(first) != first.observed || snapshotDigest() != beforeRepeat {
		t.Fatalf("timezone change left stale synthetic buckets or rewrote observations: buckets=%s", buckets)
	}
	t.Log("timezone change replaced synthetic 22:55 UTC with 23:55 UTC and preserved observed rows")

	allObservations := func() string {
		return read(first.pool, `SELECT jsonb_agg(to_jsonb(s)||jsonb_build_object('xmin',s.xmin::text) ORDER BY ts_bucket)::text
			FROM energy_snapshots s WHERE ('backfilled'=ANY(flags)) IS NOT TRUE`)
	}
	observationsBeforeDateLine := allObservations()
	historicalBuckets := func() string {
		return read(first.pool, `SELECT COALESCE(string_agg(date||'='||
			to_char(ts_bucket AT TIME ZONE 'UTC','YYYY-MM-DD HH24:MI'), ',' ORDER BY date, ts_bucket),'')
			FROM energy_snapshots WHERE date BETWEEN '2026-03-05' AND '2026-03-08' AND 'backfilled'=ANY(flags)`)
	}
	for _, scenario := range []struct {
		zone, expected string
	}{
		{"Pacific/Kiritimati", "2026-03-05=2026-03-05 09:55,2026-03-06=2026-03-06 09:55," +
			"2026-03-07=2026-03-07 09:55,2026-03-08=2026-03-08 09:55"},
		{"Pacific/Honolulu", "2026-03-05=2026-03-06 09:55,2026-03-06=2026-03-07 09:55," +
			"2026-03-07=2026-03-08 09:55,2026-03-08=2026-03-09 09:55"},
	} {
		if err := first.db.SaveSettings(map[string]string{"timezone": scenario.zone}); err != nil {
			t.Fatal(err)
		}
		results, err = sleepshadow.RebuildAll(ctx, shadowPool, bindings, version, freeSlot)
		if err != nil || !results[0].Complete || results[0].DerivedDates == 0 {
			t.Fatalf("rebuild in %s: %+v %v", scenario.zone, results, err)
		}
		if buckets := historicalBuckets(); buckets != scenario.expected || allObservations() != observationsBeforeDateLine {
			t.Fatalf("%s wanted %s and unchanged observations, got %q", scenario.zone, scenario.expected, buckets)
		}
	}
	honoluluBuckets := historicalBuckets()
	results, err = sleepshadow.RebuildAll(ctx, shadowPool, bindings, version, freeSlot)
	if err != nil || !results[0].Complete || results[0].DerivedDates != 0 || historicalBuckets() != honoluluBuckets ||
		allObservations() != observationsBeforeDateLine {
		t.Fatalf("repeating the date-line rebuild rewrote history: %+v %v", results, err)
	}
	t.Logf("Kiritimati to Honolulu kept one synthetic EOD per date and preserved observations on retry: %s", honoluluBuckets)

	rawDB, rawPool, _, rawSchema := newLocalShadowTestDB(t)
	const rawProfile = "d-raw-only"
	bindings[rawProfile] = rawSchema
	exec(shadowPool, "INSERT INTO sleep_shadow.profile(id) VALUES($1)", rawProfile)
	exec(rawPool, `WITH rec AS (INSERT INTO health_records DEFAULT VALUES RETURNING id)
		INSERT INTO metric_points(health_record_id,metric_name,date,qty,units,source,quality)
		SELECT id,'heart_rate_variability','2026-03-09 08:00:00 +0000',51,'ms','fixture','ok' FROM rec`)
	rawHRV := func() string {
		return read(shadowPool, `SELECT c->>'value' FROM sleep_shadow.rebuilt_readiness r
			CROSS JOIN LATERAL jsonb_array_elements(r.result->'readiness_serving'->'components') c
			WHERE r.profile=$1 AND r.local_date='2026-03-09' AND c->>'metric'='heart_rate_variability'`, rawProfile)
	}
	results, err = sleepshadow.RebuildAll(ctx, shadowPool, bindings, version, nextDay)
	if err != nil || len(results) != 4 || results[3].DerivedDates != 3 || rawHRV() != "51" {
		t.Fatalf("raw-only profile was not rebuilt: %+v %v", results, err)
	}
	exec(rawPool, "UPDATE metric_points SET qty=66")
	results, err = sleepshadow.RebuildAll(ctx, shadowPool, bindings, version, nextDay)
	if err != nil || results[0].DerivedDates != 0 || results[1].DerivedDates != 0 || results[2].DerivedDates != 0 ||
		results[3].DerivedDates != 3 || rawHRV() != "66" {
		t.Fatalf("changed raw HRV did not refresh only the affected profile: %+v %v", results, err)
	}
	raw := tenant{pool: rawPool, profile: rawProfile}
	if err := rawDB.UpsertEnergySnapshotAt(ctx, "UTC", now.Add(-time.Hour),
		storage.BankResult{Bank: 77, FormulaVersion: 1, Flags: []string{"observed_fixture"}}); err != nil {
		t.Fatal(err)
	}
	raw.observed = observedDigest(raw)
	exec(rawPool, `UPDATE metric_points SET date='2026-03-10 08:00:00 +0000'`)
	results, err = sleepshadow.RebuildAll(ctx, shadowPool, bindings, version, nextDay)
	if err != nil || results[3].DerivedDates != 2 || read(shadowPool, `SELECT min(local_date)::text
		FROM sleep_shadow.rebuilt_readiness WHERE profile=$1`, rawProfile) != today {
		t.Fatalf("moving the earliest raw point kept the old prefix: %+v %v", results, err)
	}
	if err := rawDB.UpsertEnergySnapshotAt(ctx, "UTC", time.Date(2026, time.March, 9, 23, 55, 0, 0, time.UTC),
		storage.BankResult{Bank: 42, FormulaVersion: 1, Flags: []string{"backfilled"}}); err != nil {
		t.Fatal(err)
	}
	exec(rawPool, "DELETE FROM metric_points")
	if err := rawDB.SaveSettings(map[string]string{"timezone": "Pacific/Honolulu"}); err != nil {
		t.Fatal(err)
	}
	results, err = sleepshadow.RebuildAll(ctx, shadowPool, bindings, version, nextDay)
	if err != nil || !results[3].Complete || results[3].DerivedDates != 0 ||
		read(shadowPool, "SELECT count(*)::text FROM sleep_shadow.rebuilt_readiness WHERE profile=$1", rawProfile) != "0" ||
		read(shadowPool, "SELECT count(*)::text FROM sleep_shadow.energy_current WHERE profile=$1", rawProfile) != "0" ||
		read(rawPool, "SELECT count(*)::text FROM energy_snapshots WHERE 'backfilled'=ANY(flags)") != "0" ||
		observedDigest(raw) != raw.observed {
		t.Fatalf("deleting the last raw point across a timezone date rollback left stale results or changed an observation: %+v %v", results, err)
	}
	done := read(shadowPool, "SELECT xmin::text FROM sleep_shadow.rebuild_checkpoint WHERE profile=$1", rawProfile)
	results, err = sleepshadow.RebuildAll(ctx, shadowPool, bindings, version, nextDay)
	if err != nil || !results[3].Complete || results[3].DerivedDates != 0 ||
		read(shadowPool, "SELECT xmin::text FROM sleep_shadow.rebuild_checkpoint WHERE profile=$1", rawProfile) != done ||
		observedDigest(raw) != raw.observed {
		t.Fatalf("repeating the empty run rewrote state: %+v %v", results, err)
	}
	for _, entry := range tenants {
		if observedDigest(entry) != entry.observed {
			t.Fatalf("later rebuilds rewrote observations for %s", entry.profile)
		}
	}
	// A tenant may mutate its own inputs without direct rights to the shared
	// revision table or another tenant's data.
	role := testdb.SchemaName("shadow_writer")
	quotedRole := pgx.Identifier{role}.Sanitize()
	exec(shadowPool, "CREATE ROLE "+quotedRole)
	t.Cleanup(func() {
		if _, err := shadowPool.Exec(context.Background(), "DROP OWNED BY "+quotedRole); err != nil {
			t.Error(err)
		}
		if _, err := shadowPool.Exec(context.Background(), "DROP ROLE "+quotedRole); err != nil {
			t.Error(err)
		}
	})
	exec(shadowPool, "GRANT USAGE ON SCHEMA "+pgx.Identifier{first.schema}.Sanitize()+" TO "+quotedRole)
	exec(shadowPool, "GRANT INSERT ON "+pgx.Identifier{first.schema, "settings"}.Sanitize()+" TO "+quotedRole)
	beforeRevision := read(shadowPool, "SELECT revision::text FROM sleep_shadow.rebuild_input_revision WHERE tenant_schema=$1", first.schema)
	tx, err := shadowPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+quotedRole); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO "+pgx.Identifier{first.schema, "settings"}.Sanitize()+" (key,value) VALUES ('energy.test_permission','1')"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if read(shadowPool, "SELECT revision::text FROM sleep_shadow.rebuild_input_revision WHERE tenant_schema=$1", first.schema) == beforeRevision {
		t.Fatal("tenant write failed to advance the input revision")
	}
	tx, err = shadowPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+quotedRole); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SELECT * FROM sleep_shadow.rebuild_input_revision"); err == nil {
		t.Fatal("tenant could read shared revisions")
	}
	_ = tx.Rollback(ctx)
	tx, err = shadowPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+quotedRole); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO "+pgx.Identifier{second.schema, "settings"}.Sanitize()+" (key,value) VALUES ('energy.test_permission','1')"); err == nil {
		t.Fatal("tenant could write another schema")
	}
	_ = tx.Rollback(ctx)
	t.Log("deleting the final raw point across a timezone date rollback cleared its projections and preserved observations")

	exact := strings.Repeat("m", 63)
	alias := exact + "_alias"
	exec(shadowPool, "CREATE SCHEMA "+pgx.Identifier{exact}.Sanitize())
	for _, table := range []string{"settings", "daily_scores", "energy_snapshots", "metric_points"} {
		exec(shadowPool, "CREATE TABLE "+pgx.Identifier{exact, table}.Sanitize()+
			" (LIKE "+pgx.Identifier{first.schema, table}.Sanitize()+" INCLUDING ALL)")
	}
	exec(shadowPool, "INSERT INTO sleep_shadow.profile(id) VALUES('e-exact'),('f-truncates')")
	bindings["e-exact"], bindings["f-truncates"] = exact, alias
	if _, err := sleepshadow.RebuildAll(ctx, shadowPool, bindings, version, nextDay); err == nil ||
		!strings.Contains(err.Error(), "under that exact name") {
		t.Fatalf("truncated schema alias was not rejected: %v", err)
	}
	if marker := read(shadowPool, `SELECT count(*)::text FROM sleep_shadow.rebuild_checkpoint
		WHERE profile IN ('e-exact','f-truncates')`); marker != "0" {
		t.Fatal("invalid alias wrote a checkpoint")
	}
	if _, err := decodeRebuildBindings([]byte(`{"duplicate":"one","duplicate":"two"}`)); err == nil {
		t.Fatal("duplicate manifest keys were not rejected")
	}
}
