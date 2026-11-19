package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/sleepshadow"
	"health-receiver/internal/storage"
)

// The queue, route and legacy input adapter belong to this rehearsal. The
// shadow handler, full rebuild, daily SQL and Go metric readers are real.
func TestLocalSleepRolloutRehearsal(t *testing.T) {
	ctx := context.Background()
	useIsolatedRebuildDatabase(t, ctx)
	const tz, pilot, today = "Europe/Belgrade", "pilot", "2026-03-10"
	const from, firstNight, secondNight = "2026-02-04", "2026-03-08", "2026-03-09"
	const version = "rollout-rehearsal-v1"
	now := time.Date(2026, 3, 10, 8, 7, 0, 0, time.UTC)
	type tenant struct {
		db     *storage.DB
		pool   *pgxpool.Pool
		schema string
	}
	oldDB, oldPool, shared, oldSchema := newLocalShadowTestDB(t)
	exec := func(pool *pgxpool.Pool, sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	read := func(pool *pgxpool.Pool, sql string, args ...any) string {
		t.Helper()
		var out string
		if err := pool.QueryRow(ctx, sql, args...).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	seed := func(target tenant, start string) {
		t.Helper()
		if err := target.db.SaveSettings(map[string]string{"timezone": tz, "energy.alpha": "0.09"}); err != nil {
			t.Fatal(err)
		}
		if start == "" {
			return
		}
		exec(target.pool, `INSERT INTO daily_scores(date,hrv_avg,rhr_avg,sleep_total,sleep_core,sleep_deep,sleep_rem,sleep_awake,steps,calories)
			SELECT d::date::text,50,57,8,5,1.5,1.5,0,4000,550 FROM generate_series($1::date,$2::date,interval '1 day') d`, start, today)
	}
	putSleep := func(pool *pgxpool.Pool, date string, core, deep, rem float64) {
		t.Helper()
		exec(pool, `UPDATE daily_scores SET sleep_total=$2::float8+$3::float8+$4::float8,sleep_core=$2,sleep_deep=$3,sleep_rem=$4,sleep_awake=0 WHERE date=$1`, date, core, deep, rem)
	}
	editAlpha := func(db *storage.DB, alpha string) {
		t.Helper()
		if err := db.SaveSettings(map[string]string{"energy.alpha": alpha}); err != nil {
			t.Fatal(err)
		}
	}

	exec(shared, `CREATE SCHEMA rollout_fixture;
		CREATE TABLE rollout_fixture.queue (
			id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, packet text NOT NULL,
			legacy_date text NOT NULL, legacy_hours double precision NOT NULL, processed_on text
		);
		CREATE TABLE rollout_fixture.route (
			profile text PRIMARY KEY, active text NOT NULL, fence bigint
		)`)
	handler, err := sleepshadow.New(ctx, shared)
	if err != nil {
		t.Fatal(err)
	}
	makeTenant := func(start string) tenant {
		t.Helper()
		db, pool, _, schema := newLocalShadowTestDB(t)
		result := tenant{db, pool, schema}
		seed(result, start)
		return result
	}
	targets := map[string]tenant{
		pilot: makeTenant(from), "short": makeTenant(firstNight), "empty": makeTenant(""),
	}
	expected := map[string]tenant{pilot: makeTenant(from), "short": makeTenant(firstNight)}
	expectedLegacy := makeTenant(from)
	seed(tenant{db: oldDB, pool: oldPool}, from)
	bindings := make(map[string]string)
	for profile, target := range targets {
		bindings[profile] = target.schema
		exec(shared, "INSERT INTO sleep_shadow.profile(id) VALUES($1)", profile)
	}
	exec(shared, "INSERT INTO rollout_fixture.route VALUES($1,'legacy',NULL)", pilot)
	target := targets[pilot]

	packet := func(id string, revision, priority int, zone, start string, core, deep, rem float64) string {
		t.Helper()
		left, err := time.Parse(time.RFC3339, start)
		if err != nil {
			t.Fatal(err)
		}
		var stages []map[string]any
		for i, kind := range []string{"core", "deep", "rem"} {
			right := left.Add(time.Duration([]float64{core, deep, rem}[i] * float64(time.Hour)))
			stages = append(stages, map[string]any{"id": fmt.Sprintf("%s-%d", id, i), "kind": kind, "start_at": left, "end_at": right})
			left = right
		}
		body, err := json.Marshal(map[string]any{"id": id, "revision": revision, "priority": priority,
			"wake_zone": zone, "stages": stages, "zero_stages": []string{"awake"}})
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	post := func(h http.Handler, body, want string) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/profiles/"+pilot+"/v2/sleep", strings.NewReader(body)))
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != fmt.Sprintf("{\"status\":%q}", want) {
			t.Fatalf("sync: %d %s", w.Code, w.Body.String())
		}
	}
	const start = "2026-03-08T16:30:00Z"
	post(handler, packet("B", 1, 1, tz, "2026-03-07T23:00:00Z", 4, 1.5, 1.5), "applied")
	post(handler, packet("C", 1, 2, tz, "2026-03-08T23:00:00Z", 5, 1.5, 1.5), "applied")
	post(handler, packet("A", 1, 0, tz, start, 5, 0.5, 0.5), "applied")
	putSleep(expected[pilot].pool, firstNight, 5, 0.5, 0.5)
	putSleep(oldPool, firstNight, 5, 0.5, 0.5)
	putSleep(expectedLegacy.pool, firstNight, 5, 0.5, 0.5)
	putSleep(expectedLegacy.pool, secondNight, 6.5, 0.5, 0.5)

	observe := func(at time.Time, bank int) {
		t.Helper()
		if err := target.db.UpsertEnergySnapshotAt(ctx, tz, at, storage.BankResult{
			Bank: bank, FormulaVersion: storage.DefaultEnergyConfig().FormulaVersion, Flags: []string{"observed_fixture"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Local 23:55 on March 7 plus the occupied current slot.
	observe(time.Date(2026, 3, 7, 22, 55, 0, 0, time.UTC), 71)
	observe(now, 72)
	observations := func(pool *pgxpool.Pool, physical bool) string {
		return read(pool, `SELECT COALESCE(jsonb_agg(to_jsonb(s)||
			CASE WHEN $1 THEN jsonb_build_object('xmin',s.xmin::text) ELSE '{}'::jsonb END
			ORDER BY ts_bucket)::text,'[]')
			FROM energy_snapshots s WHERE ('backfilled'=ANY(flags)) IS NOT TRUE`, physical)
	}
	copyObservationsToLegacy := func() {
		t.Helper()
		exec(oldPool, `INSERT INTO energy_snapshots SELECT * FROM `+
			pgx.Identifier{target.schema, "energy_snapshots"}.Sanitize()+
			` WHERE ('backfilled'=ANY(flags)) IS NOT TRUE ON CONFLICT (ts_bucket) DO NOTHING`)
	}
	copyObservationsToLegacy()
	originalObservations := observations(target.pool, true)

	rebuild := func(want map[string]int) {
		t.Helper()
		results, err := sleepshadow.RebuildAll(ctx, shared, bindings, version, now)
		if err != nil || len(results) != len(bindings) {
			t.Fatalf("fleet rebuild: %+v %v", results, err)
		}
		for _, r := range results {
			days, exact := want[r.Profile]
			if !r.Complete || (exact && r.DerivedDates != days) {
				t.Fatalf("fleet rebuild: %+v", results)
			}
		}
	}
	rebuild(map[string]int{pilot: 35, "short": 3, "empty": 0})
	if got := read(shared, `SELECT count(*)::text FROM sleep_shadow.rebuilt_readiness WHERE profile='empty'`); got != "0" {
		t.Fatal(got)
	}
	// The production-shaped shadow triggers are installed by RebuildAll.
	// Include the rehearsal's separate legacy input schema in the same fence.
	exec(shared, "INSERT INTO sleep_shadow.rebuild_input_revision (tenant_schema,revision) VALUES($1,0)", oldSchema)
	for _, table := range []string{"settings", "daily_scores", "metric_points", "hourly_metrics", "subjective_checkins"} {
		qualified := pgx.Identifier{oldSchema, table}.Sanitize()
		if read(shared, "SELECT (to_regclass($1) IS NOT NULL)::text", qualified) == "true" {
			exec(shared, `CREATE TRIGGER rollout_legacy_input AFTER INSERT OR UPDATE OR DELETE ON `+qualified+
				` FOR EACH ROW EXECUTE FUNCTION sleep_shadow.bump_rebuild_input_revision();
				CREATE TRIGGER rollout_legacy_truncate AFTER TRUNCATE ON `+qualified+
				` FOR EACH STATEMENT EXECUTE FUNCTION sleep_shadow.bump_rebuild_input_revision()`)
		}
	}

	readiness := func(db *storage.DB, profile, date string) *storage.LocalShadowReadiness {
		t.Helper()
		r, err := db.GetLocalShadowReadiness(ctx, profile, date)
		if err != nil || r == nil {
			t.Fatalf("readiness %s/%s: %+v %v", profile, date, r, err)
		}
		return r
	}
	compareReadiness := func(got, want *storage.LocalShadowReadiness) {
		t.Helper()
		if got.RawScore != want.RawScore || got.Score != want.Score || got.Band != want.Band ||
			got.Serving.Status != want.Serving.Status || got.Serving.Reason != want.Serving.Reason {
			t.Fatalf("readiness %s: got %d/%d %s %s, want %d/%d %s %s", got.Date, got.RawScore, got.Score, got.Band,
				got.Serving.Status, want.RawScore, want.Score, want.Band, want.Serving.Status)
		}
	}
	bank := func(db *storage.DB, date string) storage.BankResult {
		t.Helper()
		result, err := db.ComputeBankForDate(ctx, tz, date)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	compareEnergy := func(profile, date string, reference *storage.DB) {
		t.Helper()
		want := bank(reference, date)
		pool := targets[profile].pool
		var exists bool
		var gotBank, drain, restore, formula int
		var flags []string
		var components map[string]float64
		if date == today {
			var state string
			err := shared.QueryRow(ctx, `SELECT state,bank,drain_delta,restore_delta,formula_version,flags
				FROM sleep_shadow.energy_current WHERE profile=$1 AND local_date=$2::date`, profile, date).
				Scan(&state, &gotBank, &drain, &restore, &formula, &flags)
			if err != nil || state != want.State {
				t.Fatalf("current %s: state=%s want=%s %v", profile, state, want.State, err)
			}
		} else {
			if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM energy_snapshots WHERE date=$1 AND 'observed_fixture'=ANY(flags))`, date).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			// Only the March 7 observed fixture occupies a historical EOD slot.
			if want.State == "stale" || exists {
				if count := read(pool, `SELECT count(*)::text FROM energy_snapshots WHERE date=$1 AND 'backfilled'=ANY(flags)`, date); count != "0" {
					t.Fatalf("unexpected synthetic EOD %s/%s", profile, date)
				}
				return
			}
			err := pool.QueryRow(ctx, `SELECT bank,drain_delta,restore_delta,formula_version,array_remove(flags,'backfilled'),components
				FROM energy_snapshots WHERE date=$1 AND 'backfilled'=ANY(flags)`, date).
				Scan(&gotBank, &drain, &restore, &formula, &flags, &components)
			if err != nil {
				t.Fatalf("EOD %s/%s: %v", profile, date, err)
			}
			if count := read(pool, `SELECT count(*)::text FROM energy_snapshots WHERE date=$1 AND 'backfilled'=ANY(flags)`, date); count != "1" {
				t.Fatal(count)
			}
			if len(components) != len(want.Components) {
				t.Fatalf("EOD components %s/%s", profile, date)
			}
			for key, value := range want.Components {
				if fact, ok := components[key]; !ok || math.Abs(fact-value) > 1e-9*math.Max(1, math.Abs(value)) {
					t.Fatalf("EOD component %s/%s/%s", profile, date, key)
				}
			}
		}
		if gotBank != want.Bank || drain != want.TodayDrain || restore != want.TodayRestore || formula != want.FormulaVersion ||
			strings.Join(flags, ",") != strings.Join(want.Flags, ",") {
			t.Fatalf("energy %s/%s: bank=%d drain=%d restore=%d version=%d flags=%v vs %+v", profile, date, gotBank, drain, restore, formula, flags, want)
		}
	}
	checkHistory := func(profile, startDate string, saved bool) int {
		t.Helper()
		start, err := time.Parse(time.DateOnly, startDate)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for date := start; date.Format(time.DateOnly) <= today; date = date.AddDate(0, 0, 1) {
			day := date.Format(time.DateOnly)
			got := readiness(targets[profile].db, profile, day)
			if saved {
				var stored storage.LocalShadowReadiness
				payload := read(shared, `SELECT result::text FROM sleep_shadow.rebuilt_readiness
					WHERE profile=$1 AND local_date=$2::date`, profile, day)
				if err := json.Unmarshal([]byte(payload), &stored); err != nil || !reflect.DeepEqual(got, &stored) {
					t.Fatalf("saved readiness %s/%s: %v", profile, day, err)
				}
			}
			compareReadiness(got, readiness(expected[profile].db, "legacy-reference", day))
			compareEnergy(profile, day, expected[profile].db)
			count++
		}
		return count
	}
	t.Logf("full rebuild accepted 3 profiles: pilot %d dates, short %d dates, empty 0 dates",
		checkHistory(pilot, from, true), checkHistory("short", firstNight, true))

	// The following helper operations stand in for the future queue and
	// rollout controller, with an actual database transaction for publication.
	enqueue := func(body, legacyDate string, legacyHours float64) string {
		return read(shared, `INSERT INTO rollout_fixture.queue(packet,legacy_date,legacy_hours) VALUES($1,$2,$3) RETURNING id::text`,
			body, legacyDate, legacyHours)
	}
	fence := func() string {
		return read(shared, `UPDATE rollout_fixture.route SET fence=(SELECT max(id) FROM rollout_fixture.queue)
			WHERE profile='pilot' RETURNING fence::text`)
	}
	active := func() string { return read(shared, "SELECT active FROM rollout_fixture.route WHERE profile='pilot'") }
	processed := func(id string) string {
		return read(shared, "SELECT COALESCE(processed_on,'queued') FROM rollout_fixture.queue WHERE id=$1::text::bigint", id)
	}
	applyLegacy := func(id string) {
		day := read(shared, "SELECT legacy_date FROM rollout_fixture.queue WHERE id=$1::text::bigint", id)
		exec(oldPool, `UPDATE daily_scores SET sleep_total=q.legacy_hours,sleep_core=q.legacy_hours-1,
			sleep_deep=0.5,sleep_rem=0.5,sleep_awake=0 FROM rollout_fixture.queue q WHERE q.id=$1::text::bigint AND date=$2`, id, day)
	}
	process := func(id string, h http.Handler, status string) {
		t.Helper()
		if processed(id) != "queued" {
			t.Fatal("queue receipt was already applied")
		}
		allowed := read(shared, `SELECT (r.fence IS NULL OR q.id<=r.fence)::text FROM rollout_fixture.route r
			CROSS JOIN rollout_fixture.queue q WHERE r.profile='pilot' AND q.id=$1::text::bigint`, id)
		if allowed != "true" {
			t.Fatal("queue receipt escaped the fence")
		}
		route := active()
		if route == "legacy" {
			applyLegacy(id)
		}
		if h != nil {
			post(h, read(shared, "SELECT packet FROM rollout_fixture.queue WHERE id=$1::text::bigint", id), status)
		}
		exec(shared, "UPDATE rollout_fixture.queue SET processed_on=$2 WHERE id=$1::text::bigint", id, route)
	}
	type marker struct{ fence, source, input, schemaOID, legacyInput, legacyOID int64 }
	const markerSQL = `SELECT c.fence,p.source_revision,r.revision,n.oid::bigint,l.revision,ln.oid::bigint FROM rollout_fixture.route c
		JOIN sleep_shadow.profile p ON p.id=c.profile JOIN sleep_shadow.rebuild_input_revision r ON r.tenant_schema=$1
		JOIN pg_catalog.pg_namespace n ON n.nspname=r.tenant_schema
		JOIN sleep_shadow.rebuild_input_revision l ON l.tenant_schema=$2
		JOIN pg_catalog.pg_namespace ln ON ln.nspname=l.tenant_schema WHERE c.profile=$3`
	capture := func() marker {
		t.Helper()
		var m marker
		err := shared.QueryRow(ctx, markerSQL, target.schema, oldSchema, pilot).
			Scan(&m.fence, &m.source, &m.input, &m.schemaOID, &m.legacyInput, &m.legacyOID)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	switchRoute := func(previous, next string, verified marker) bool {
		t.Helper()
		changed := false
		err := pgx.BeginFunc(ctx, shared, func(tx pgx.Tx) error {
			var current marker
			err := tx.QueryRow(ctx, markerSQL+` AND c.active=$4 AND NOT EXISTS (
				SELECT 1 FROM rollout_fixture.queue WHERE id<=c.fence AND processed_on IS NULL
			) FOR UPDATE OF c,p,r,l`, target.schema, oldSchema, pilot, previous).
				Scan(&current.fence, &current.source, &current.input, &current.schemaOID, &current.legacyInput, &current.legacyOID)
			if err == pgx.ErrNoRows {
				return nil
			}
			if err != nil || current != verified {
				return err
			}
			_, err = tx.Exec(ctx, "UPDATE rollout_fixture.route SET active=$1,fence=NULL WHERE profile=$2", next, pilot)
			changed = err == nil
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return changed
	}

	beforeID := enqueue(packet("A", 2, 0, "Asia/Singapore", start, 5, 0.5, 0.5), firstNight, 6)
	if fence() != beforeID {
		t.Fatal("wrong forward fence")
	}
	afterID := enqueue(packet("A", 3, 0, "Asia/Singapore", start, 6, 1, 0.5), secondNight, 7.5)
	if switchRoute("legacy", "shadow", capture()) {
		t.Fatal("forward switch left a pending receipt below the fence")
	}
	process(beforeID, handler, "applied")
	if processed(beforeID) != "legacy" || processed(afterID) != "queued" {
		t.Fatal("forward queue partition")
	}
	putSleep(expected[pilot].pool, firstNight, 4, 1.5, 1.5)
	putSleep(expected[pilot].pool, secondNight, 5, 0.5, 0.5)
	rebuild(map[string]int{pilot: 35, "short": 0, "empty": 0})
	verifyShadow := func() marker {
		t.Helper()
		verified := capture()
		checkHistory(pilot, from, true)
		return verified
	}
	verified := verifyShadow()
	editAlpha(target.db, "0.10")
	if switchRoute("legacy", "shadow", verified) || active() != "legacy" || processed(afterID) != "queued" {
		t.Fatal("forward switch accepted stale verification")
	}
	editAlpha(expected[pilot].db, "0.10")
	rebuild(map[string]int{pilot: 35, "short": 0, "empty": 0})
	verified = verifyShadow()
	if !switchRoute("legacy", "shadow", verified) {
		t.Fatal("forward switch refused verified state")
	}
	live, err := sleepshadow.NewWithDailyRefresh(ctx, shared, localEnergyRefresh(target.schema, pilot, func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	process(afterID, live, "applied")
	putSleep(expected[pilot].pool, secondNight, 6, 1, 0.5)
	checkHistory(pilot, from, false)
	for _, day := range []string{firstNight, secondNight, today} {
		r := readiness(target.db, pilot, day)
		b := bank(expected[pilot].db, day)
		t.Logf("shadow after switch %s: readiness=%d %s, bank=%d %s", day, r.Score, r.Band, b.Bank, b.State)
	}
	beforeRepeat := read(shared, "SELECT source_revision::text FROM sleep_shadow.profile WHERE id=$1", pilot)
	post(live, read(shared, "SELECT packet FROM rollout_fixture.queue WHERE id=$1::text::bigint", afterID), "repeat")
	if read(shared, "SELECT source_revision::text FROM sleep_shadow.profile WHERE id=$1", pilot) != beforeRepeat ||
		processed(afterID) != "shadow" || observations(target.pool, true) != originalObservations {
		t.Fatal("shadow repeat rewrote data or an observation")
	}
	t.Logf("forward fence %s: legacy receipt=%s, shadow receipt=%s, stale token rejected; current EnergyBank=%d",
		beforeID, beforeID, afterID, bank(expected[pilot].db, today).Bank)

	// Roll back after a pilot-time observation was written. Rebuild the
	// legacy projection out of its own inputs before moving the route back.
	collision := time.Date(2026, 3, 9, 22, 55, 0, 0, time.UTC)
	freeSlot := collision.AddDate(0, 0, -1)
	observe(collision, 73)
	beforeRollback := observations(target.pool, true)
	wantObservations := observations(target.pool, false)
	copyObservationsToLegacy()
	legacyObservations := observations(oldPool, true)
	if read(oldPool, "SELECT count(*)::text FROM energy_snapshots WHERE ts_bucket=$1", freeSlot) != "0" {
		t.Fatal("legacy repair fixture needs a free historical slot")
	}
	if fence() != afterID {
		t.Fatal("wrong rollback fence")
	}
	finalID := enqueue(packet("A", 4, 0, "Asia/Singapore", start, 7, 1, 0.5), secondNight, 8.5)
	applyLegacy(afterID)
	prepareLegacy := func(alpha string) {
		t.Helper()
		editAlpha(oldDB, alpha)
		editAlpha(expectedLegacy.db, alpha)
		for _, db := range []*storage.DB{oldDB, expectedLegacy.db} {
			if err := db.RepairHistoricalDependencies(ctx, []string{firstNight, secondNight}, tz, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	checkLegacySlots := func(pool *pgxpool.Pool) {
		t.Helper()
		want := bank(expectedLegacy.db, firstNight)
		if got := read(pool, `SELECT date||':'||bank||':'||drain_delta||':'||restore_delta
			FROM energy_snapshots WHERE ts_bucket=$1 AND 'backfilled'=ANY(flags)`, freeSlot); got != fmt.Sprintf("%s:%d:%d:%d", firstNight, want.Bank, want.TodayDrain, want.TodayRestore) {
			t.Fatalf("free EOD slot: got %s, Go reference %+v", got, want)
		}
		if got := read(pool, `SELECT date||':'||bank FROM energy_snapshots
			WHERE ts_bucket=$1 AND ('backfilled'=ANY(flags)) IS NOT TRUE`, collision); got != secondNight+":73" {
			t.Fatalf("occupied EOD slot: got %s", got)
		}
		if observations(pool, false) != wantObservations {
			t.Fatal("legacy reader is missing an observation or fields changed")
		}
	}
	verifyLegacy := func() marker {
		t.Helper()
		verified := capture()
		for _, day := range []string{firstNight, secondNight, today} {
			compareReadiness(readiness(oldDB, "legacy-reference", day), readiness(expectedLegacy.db, "legacy-reference", day))
			got, want := bank(oldDB, day), bank(expectedLegacy.db, day)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("legacy EnergyBank %s: got %+v, independent Go reference %+v", day, got, want)
			}
		}
		checkLegacySlots(oldPool)
		return verified
	}
	prepareLegacy("0.10")
	verified = verifyLegacy()
	editAlpha(oldDB, "0.11")
	if switchRoute("shadow", "legacy", verified) || active() != "shadow" || processed(finalID) != "queued" {
		t.Fatal("rollback accepted stale verification after a legacy input change")
	}
	prepareLegacy("0.11")
	verified = verifyLegacy()
	if !switchRoute("shadow", "legacy", verified) {
		t.Fatal("rollback refused verified state")
	}
	activeHistory := func() *pgxpool.Pool {
		t.Helper()
		switch active() {
		case "shadow":
			return target.pool
		case "legacy":
			return oldPool
		default:
			t.Fatal("unknown active history route")
			return nil
		}
	}
	checkLegacySlots(activeHistory())
	process(finalID, nil, "")
	checkLegacySlots(activeHistory())
	if active() != "legacy" || processed(finalID) != "legacy" || observations(target.pool, true) != beforeRollback ||
		observations(activeHistory(), true) != legacyObservations ||
		read(oldPool, "SELECT sleep_total::text FROM daily_scores WHERE date=$1", secondNight) != "8.5" ||
		read(shared, "SELECT source_revision::text FROM sleep_shadow.profile WHERE id=$1", pilot) != beforeRepeat ||
		read(shared, "SELECT count(*)::text FROM rollout_fixture.queue WHERE processed_on IS NULL") != "0" ||
		read(shared, "SELECT count(*)::text FROM rollout_fixture.queue") != "3" {
		t.Fatal("rollback lost a receipt, left queue work or changed an observation")
	}
	t.Logf("rollback fence %s: stale legacy token rejected; Go reference matched both dates and today; receipt %s drained after switch", afterID, finalID)
	t.Logf("active legacy history kept three observations including EOD %s=%d; rebuilt free EOD %s=%d", secondNight, 73, firstNight, bank(expectedLegacy.db, firstNight).Bank)
}
