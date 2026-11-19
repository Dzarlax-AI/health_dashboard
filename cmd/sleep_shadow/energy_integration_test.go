package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"health-receiver/internal/health"
	"health-receiver/internal/sleepshadow"
	"health-receiver/internal/storage"
)

func TestLocalEnergyBankAfterSleepSync(t *testing.T) {
	db, pool, shadowPool, schema := newLocalShadowTestDB(t)
	ctx := context.Background()
	const tz = "Europe/Belgrade"
	const oldDay, newDay, lastDay = "2026-01-26", "2026-01-27", "2026-02-16"
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSettings(map[string]string{"timezone": tz}); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO daily_scores(date,sleep_total,sleep_deep,sleep_rem,sleep_awake,calories,sustained_hr_load)
		SELECT (date '2026-01-01'+i)::text,7+((i*9)%13)*0.125,0.875+((i*7)%8)*0.125,
		       1+((i*3)%7)*0.125,((i*5)%7)*0.0625,485+((i*31)%450),1.5
		FROM generate_series(0,60) s(i)`)
	exec(`UPDATE daily_scores SET sleep_total=6,sleep_deep=0.5,sleep_rem=0.5,sleep_awake=0 WHERE date=$1`, oldDay)
	exec(`UPDATE daily_scores SET sleep_total=8,sleep_deep=1.5,sleep_rem=1.5,sleep_awake=0 WHERE date=$1`, newDay)

	at := func(day, clock string) time.Time {
		t.Helper()
		stamp, err := time.ParseInLocation("2006-01-02 15:04", day+" "+clock, loc)
		if err != nil {
			t.Fatal(err)
		}
		return stamp
	}
	const today = "2026-03-10"
	now := at(today, "09:07").UTC()
	exec(`INSERT INTO daily_scores(date,sleep_total,sleep_deep,sleep_rem,sleep_awake,calories,sustained_hr_load)
		SELECT ($1::date+i)::text,8,1.5,1.5,0,550,1.5 FROM generate_series(-20,0) s(i)
		ON CONFLICT (date) DO UPDATE SET sleep_total=8,sleep_deep=1.5,sleep_rem=1.5,
			sleep_awake=0,calories=550,sustained_hr_load=1.5`, today)
	observedDates := [][2]string{{oldDay, "09:05"}, {newDay, "16:15"}, {"2026-01-28", "23:55"},
		{today, "07:30"}, {today, "08:55"}}
	for i, item := range observedDates {
		err := db.UpsertEnergySnapshotAt(ctx, tz, at(item[0], item[1]), storage.BankResult{
			Bank: 50 + i, TodayDrain: 10, TodayRestore: 40, FormulaVersion: storage.DefaultEnergyConfig().FormulaVersion,
			Flags: []string{"observed_fixture"}, Components: map[string]float64{"observed": float64(i)},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	digest := func(observedOnly bool) string {
		t.Helper()
		var out string
		err := pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(s)||
			jsonb_build_object('physical_xmin',s.xmin::text) ORDER BY ts_bucket)::text,'[]')
			FROM energy_snapshots s WHERE NOT $1 OR 'observed_fixture'=ANY(flags)`, observedOnly).Scan(&out)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	observed := digest(true)

	profile := uuid.NewString()
	cleanupShadowProfiles(t, shadowPool, profile)
	refresh := localEnergyRefresh(schema, profile, func() time.Time { return now })
	start := func() http.Handler {
		t.Helper()
		handler, err := sleepshadow.NewWithDailyRefresh(ctx, shadowPool, refresh)
		if err != nil {
			t.Fatal(err)
		}
		return withLocalEnergyBriefing(handler, db, schema, profile, func() time.Time { return now })
	}
	handler := start()
	post := func(target, packet, status string, code int) {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("POST", "/profiles/"+target+"/v2/sleep", strings.NewReader(packet)))
		if response.Code != code || (status != "" && !strings.Contains(response.Body.String(), `"status":"`+status+`"`)) {
			t.Fatalf("sync: %d %s", response.Code, response.Body.String())
		}
	}
	packet := func(id string, revision, priority int, zone, start string, widths []float64, kinds []string, zeroAwake bool) string {
		t.Helper()
		left, err := time.Parse(time.RFC3339, start)
		if err != nil {
			t.Fatal(err)
		}
		stages := make([]map[string]any, 0, len(widths))
		for i, width := range widths {
			right := left.Add(time.Duration(width * float64(time.Hour)))
			stages = append(stages, map[string]any{"id": fmt.Sprintf("%s%d", id, i), "kind": kinds[i], "start_at": left, "end_at": right})
			left = right
		}
		candidate := map[string]any{"id": id, "revision": revision, "priority": priority, "wake_zone": zone, "stages": stages}
		if zeroAwake {
			candidate["zero_stages"] = []string{"awake"}
		}
		raw, err := json.Marshal(candidate)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	staged := []string{"core", "deep", "rem"}
	aStart := "2026-01-26T16:30:00Z"
	a := packet("A", 1, 0, tz, aStart, []float64{5, 0.5, 0.5}, staged, true)
	b := packet("B", 1, 1, tz, "2026-01-25T23:00:00Z", []float64{4, 1.5, 1.5}, staged, true)
	c := packet("C", 1, 2, tz, "2026-01-26T22:00:00Z", []float64{5, 1.5, 1.5}, staged, true)
	post(profile, b, "applied", 200)
	post(profile, c, "applied", 200)
	post(profile, a, "applied", 200)
	type snapshot struct {
		bank, drain, restore, version int
		flags                         []string
		components                    map[string]float64
	}
	readBucket := func(bucket time.Time) snapshot {
		t.Helper()
		var row snapshot
		err := pool.QueryRow(ctx, `SELECT bank,drain_delta,restore_delta,formula_version,flags,components
			FROM energy_snapshots WHERE ts_bucket=$1`, bucket).
			Scan(&row.bank, &row.drain, &row.restore, &row.version, &row.flags, &row.components)
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	readSnapshot := func(day string) snapshot {
		t.Helper()
		return readBucket(at(day, "23:55"))
	}
	if old, new := readSnapshot(oldDay), readSnapshot(newDay); old.bank != 9 || new.bank != 32 {
		t.Fatalf("unexpected pre-transfer EnergyBank: %d, %d", old.bank, new.bank)
	}
	if digest(true) != observed {
		t.Fatal("initial sync touched observed snapshots")
	}

	before := digest(false)
	trigger := pgx.Identifier{"energy_failure_" + uuid.NewString()}.Sanitize()
	exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $test$
		BEGIN
			IF NEW.date = '2026-01-27' AND 'backfilled'=ANY(NEW.flags) THEN
				RAISE EXCEPTION 'synthetic EnergyBank write failure';
			END IF;
			RETURN NEW;
		END; $test$;
		CREATE TRIGGER %s BEFORE INSERT ON energy_snapshots FOR EACH ROW EXECUTE FUNCTION %s()`, trigger, trigger, trigger))
	moved := packet("A", 2, 0, "Asia/Singapore", aStart, []float64{5, 0.5, 0.5}, staged, true)
	post(profile, moved, "", 500)
	var pending int
	var wakeDate string
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM sleep_shadow.pending WHERE profile=$1),
		(SELECT wake_date::text FROM sleep_shadow.daily WHERE profile=$1 AND candidate='A')`, profile).Scan(&pending, &wakeDate); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || wakeDate != oldDay || digest(false) != before {
		t.Fatal("failure did not roll back sleep and EnergyBank together")
	}
	exec(fmt.Sprintf("DROP TRIGGER %s ON energy_snapshots; DROP FUNCTION %s()", trigger, trigger))
	newer := packet("A", 3, 0, "Asia/Singapore", aStart, []float64{5, 0.5, 0.5}, staged, true)
	for _, incompatible := range []*sleepshadow.DailyRefresh{
		nil,
		localEnergyRefresh(schema, uuid.NewString(), time.Now),
		localEnergyRefresh(schema+"_other", profile, time.Now),
	} {
		handler, err = sleepshadow.NewWithDailyRefresh(ctx, shadowPool, incompatible)
		if err != nil {
			t.Fatal(err)
		}
		post(profile, moved, "refresh_unavailable", http.StatusServiceUnavailable)
		post(profile, newer, "refresh_unavailable", http.StatusServiceUnavailable)
		if incompatible == nil {
			post(uuid.NewString(), b, "applied", http.StatusOK)
		}
		var queuedSchema, oldWinner, newWinner string
		if err := pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM sleep_shadow.pending WHERE profile=$1),
			(SELECT refresh_schema FROM sleep_shadow.pending WHERE profile=$1 AND candidate='A' AND revision=2),
			(SELECT candidate FROM sleep_shadow.daily WHERE profile=$1 AND wake_date=$2::text::date),
			(SELECT candidate FROM sleep_shadow.daily WHERE profile=$1 AND wake_date=$3::text::date)`,
			profile, oldDay, newDay).Scan(&pending, &queuedSchema, &oldWinner, &newWinner); err != nil {
			t.Fatal(err)
		}
		if pending != 1 || queuedSchema != schema || oldWinner != "A" || newWinner != "C" || digest(false) != before {
			t.Fatal("restart without the required EnergyBank binding did not preserve the pending transfer and both projections")
		}
	}
	otherDB, otherPool, _, otherSchema := newLocalShadowTestDB(t)
	if err := otherDB.SaveSettings(map[string]string{"timezone": tz}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"daily_scores", "energy_snapshots"} {
		exec("INSERT INTO " + pgx.Identifier{otherSchema, table}.Sanitize() +
			" SELECT * FROM " + pgx.Identifier{schema, table}.Sanitize())
	}
	otherEnergy := func() string {
		t.Helper()
		var out string
		if err := otherPool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(s)||
			jsonb_build_object('physical_xmin',s.xmin::text) ORDER BY ts_bucket)::text
			FROM energy_snapshots s`).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	beforeOther := otherEnergy()
	exec("UPDATE sleep_shadow.pending SET refresh_schema=NULL WHERE profile=$1", profile)
	for _, binding := range []*sleepshadow.DailyRefresh{
		nil,
		localEnergyRefresh(otherSchema, profile, func() time.Time { return now }),
		refresh,
	} {
		handler, err = sleepshadow.NewWithDailyRefresh(ctx, shadowPool, binding)
		if err != nil {
			t.Fatal(err)
		}
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM sleep_shadow.pending WHERE profile=$1 AND candidate='A'
			AND revision=2 AND refresh_schema IS NULL)`, profile).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if !waiting || digest(false) != before || otherEnergy() != beforeOther {
			t.Fatal("an unconfirmed legacy transfer was consumed or changed EnergyBank")
		}
		post(profile, moved, "refresh_unavailable", http.StatusServiceUnavailable)
	}
	if count, err := sleepshadow.ConfirmLegacyPending(ctx, shadowPool, profile, schema); err != nil || count != 1 {
		t.Fatalf("confirm legacy binding: count=%d, err=%v", count, err)
	}
	if count, err := sleepshadow.ConfirmLegacyPending(ctx, shadowPool, profile, schema); err != nil || count != 0 {
		t.Fatalf("repeat legacy binding: count=%d, err=%v", count, err)
	}
	if _, err := sleepshadow.ConfirmLegacyPending(ctx, shadowPool, profile, otherSchema); err == nil {
		t.Fatal("confirmation accepted a schema conflicting with the confirmed binding")
	}
	handler, err = sleepshadow.NewWithDailyRefresh(ctx, shadowPool, localEnergyRefresh(otherSchema, profile, func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	post(profile, moved, "refresh_unavailable", http.StatusServiceUnavailable)
	if digest(false) != before || otherEnergy() != beforeOther {
		t.Fatal("confirmation or startup with the other schema changed EnergyBank")
	}
	handler = start()
	if old, new := readSnapshot(oldDay), readSnapshot(newDay); old.bank != 27 || new.bank != 4 || old.restore != 60 || new.restore != 45 {
		t.Fatalf("pending transfer did not recover after restart: %+v %+v", old, new)
	}
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM sleep_shadow.pending WHERE profile=$1),
		(SELECT wake_date::text FROM sleep_shadow.daily WHERE profile=$1 AND candidate='A')`, profile).Scan(&pending, &wakeDate); err != nil {
		t.Fatal(err)
	}
	if pending != 0 || wakeDate != newDay || digest(true) != observed {
		t.Fatal("recovery left pending work or modified observed snapshots")
	}
	after := digest(false)
	post(profile, moved, "repeat", 200)
	post(uuid.NewString(), a, "applied", 200)
	if digest(false) != after || otherEnergy() != beforeOther {
		t.Fatal("repeat or sync for an unrelated profile rewrote snapshots")
	}

	// With the same corrected sleep in the legacy inputs, the unchanged
	// ComputeBankForDate method is the snapshot oracle.
	exec(`UPDATE daily_scores SET sleep_total=7,sleep_deep=1.5,sleep_rem=1.5 WHERE date=$1`, oldDay)
	exec(`UPDATE daily_scores SET sleep_total=6,sleep_deep=0.5,sleep_rem=0.5 WHERE date=$1`, newDay)
	for day := at(oldDay, "00:00"); day.Format(time.DateOnly) <= lastDay; day = day.AddDate(0, 0, 1) {
		date := day.Format(time.DateOnly)
		if date == "2026-01-28" {
			continue // The observed 23:55 snapshot owns the bucket.
		}
		want, err := db.ComputeBankForDate(ctx, tz, date)
		if err != nil {
			t.Fatal(err)
		}
		got := readSnapshot(date)
		if got.bank != want.Bank || got.drain != want.TodayDrain || got.restore != want.TodayRestore ||
			got.version != want.FormulaVersion || !slices.Contains(got.flags, "backfilled") {
			t.Fatalf("snapshot differs from Go on %s: %+v vs %+v", date, got, want)
		}
	}

	// An authoritative empty date and an unobserved stage both need imputation.
	aloneStart := "2026-01-29T16:30:00Z"
	post(profile, packet("alone", 1, 0, tz, aloneStart, []float64{5, 0.5, 0.5}, staged, true), "applied", 200)
	post(profile, packet("alone", 2, 0, "Asia/Singapore", aloneStart, []float64{5, 0.5, 0.5}, staged, true), "applied", 200)
	gap := readSnapshot("2026-01-29")
	if _, hasTotal := gap.components["sleep_total_h"]; hasTotal || !slices.Contains(gap.flags, "imputed_sleep") {
		t.Fatalf("an empty date reused legacy sleep: %+v", gap)
	}
	post(profile, packet("A", 3, 0, "Asia/Singapore", aStart, []float64{5, 0.5, 0.5}, staged, false), "applied", 200)
	if got := readSnapshot(newDay); !slices.Contains(got.flags, "imputed_sleep") || got.components["sleep_total_h"] != 6 {
		t.Fatalf("unknown Awake was treated as zero: %+v", got)
	}
	post(profile, packet("A", 4, 0, "Asia/Singapore", aStart, []float64{5, 0.5, 0.5}, staged, true), "applied", 200)
	if got := readSnapshot(newDay); slices.Contains(got.flags, "imputed_sleep") || got.bank != 4 {
		t.Fatalf("confirmed zero Awake did not use recorded stages: %+v", got)
	}
	t.Run("current day snapshots are insert only", func(t *testing.T) {
		packetStart := at(today, "00:30").Format(time.RFC3339)
		firstBucket := at(today, "09:05")
		snapshotDigest := func(bucket time.Time) string {
			t.Helper()
			var out string
			err := pool.QueryRow(ctx, `SELECT (to_jsonb(s)||
				jsonb_build_object('physical_xmin',s.xmin::text))::text
				FROM energy_snapshots s WHERE s.ts_bucket=$1 AND s.date=$2`, bucket, today).Scan(&out)
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
		post(profile, packet("today", 1, 0, tz, packetStart, []float64{5, 0.5, 0.5}, staged, true), "applied", 200)
		first := readBucket(firstBucket)
		if first.components["sleep_total_h"] != 6 || slices.Contains(first.flags, "backfilled") ||
			slices.Contains(first.flags, "imputed_sleep") {
			t.Fatalf("first intraday snapshot did not use the published night: %+v", first)
		}
		firstRow, allRows := snapshotDigest(firstBucket), digest(false)
		post(profile, packet("today", 2, 0, tz, packetStart, []float64{5, 1, 1}, staged, true), "applied", 200)
		var publishedHours float64
		if err := pool.QueryRow(ctx, `SELECT sleep_seconds/3600 FROM sleep_shadow.daily
			WHERE profile=$1 AND wake_date=$2::text::date`, profile, today).Scan(&publishedHours); err != nil {
			t.Fatal(err)
		}
		if publishedHours != 7 || digest(false) != allRows {
			t.Fatal("the updated night did not publish independently of the occupied snapshot bucket")
		}

		now = at(today, "09:12").UTC()
		next := packet("today", 3, 0, tz, packetStart, []float64{5, 1.25, 1.25}, staged, true)
		post(profile, next, "applied", 200)
		secondBucket := at(today, "09:10")
		second := readBucket(secondBucket)
		if second.components["sleep_total_h"] != 7.5 || slices.Contains(second.flags, "backfilled") ||
			slices.Contains(second.flags, "imputed_sleep") || first.restore == second.restore {
			t.Fatalf("next free bucket did not record the revised sleep: first=%+v second=%+v", first, second)
		}
		snapshotDigest(secondBucket)
		if digest(true) != observed || snapshotDigest(firstBucket) != firstRow {
			t.Fatal("a later sync rewrote an existing observed snapshot")
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM energy_snapshots WHERE date=$1`, today).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 4 {
			t.Fatalf("expected exactly two new intraday snapshots, got %d total", count)
		}
		allRows = digest(false)
		now = at(today, "09:17").UTC()
		post(profile, next, "repeat", 200)
		if digest(false) != allRows {
			t.Fatal("a repeat created a snapshot in a new bucket")
		}
		t.Logf("09:05 sleep=6h bank=%d restore=%d, 09:10 sleep=7.5h bank=%d restore=%d",
			first.bank, first.restore, second.bank, second.restore)
	})

	t.Run("late sleep updates current bank without rewriting the bucket", func(t *testing.T) {
		yesterday := at(today, "00:00").AddDate(0, 0, -1).Format(time.DateOnly)
		nightStart := at(yesterday, "00:30").Format(time.RFC3339)
		// The last night still has complete data. Yesterday is the eighth
		// scattered gap until its stages are backfilled.
		exec(`UPDATE daily_scores SET sleep_awake=NULL WHERE date IN
			(SELECT ($1::date-i)::text FROM unnest(ARRAY[1,3,5,7,9,11,12,13]) AS g(i))`, today)
		occupied := at(today, "09:20")
		now = occupied
		coarse := func(revision int) string {
			return packet("yesterday", revision, 0, tz, nightStart, []float64{7}, []string{"asleep_unspecified"}, false)
		}
		readBriefing := func() health.BriefingResponse {
			t.Helper()
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/health-briefing?lang=ru", nil))
			var briefing health.BriefingResponse
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &briefing) != nil || briefing.Date != today {
				t.Fatalf("briefing: %d %s", w.Code, w.Body.String())
			}
			return briefing
		}
		rowDigest := func(query string, args ...any) string {
			t.Helper()
			var row string
			if err := pool.QueryRow(ctx, query, args...).Scan(&row); err != nil {
				t.Fatal(err)
			}
			return row
		}
		archived := func() string {
			return rowDigest(`SELECT (to_jsonb(s)||jsonb_build_object('xmin',s.xmin::text))::text
				FROM energy_snapshots s WHERE ts_bucket=$1`, occupied)
		}
		current := func() string {
			return rowDigest(`SELECT (to_jsonb(s)||jsonb_build_object('xmin',s.xmin::text))::text
				FROM sleep_shadow.energy_current s WHERE profile=$1 AND tenant_schema=$2`, profile, schema)
		}

		post(profile, coarse(1), "applied", http.StatusOK)
		if got := readBriefing(); got.EnergyBankStatus != "stale" || got.EnergyBank != nil || got.EnergyBankCurrent != nil {
			t.Fatalf("expected unavailable current bank: %+v", got.EnergyBank)
		}
		saved := archived()
		now = at(today, "09:22")
		late := packet("yesterday", 2, 0, tz, nightStart, []float64{5, 1, 1}, staged, true)
		post(profile, late, "applied", http.StatusOK)
		recovered := readBriefing()
		if recovered.EnergyBankStatus != "" || recovered.EnergyBankCurrent == nil || *recovered.EnergyBankCurrent == 0 {
			t.Fatalf("late stages did not restore a number: status=%q bank=%v", recovered.EnergyBankStatus, recovered.EnergyBankCurrent)
		}
		if archived() != saved || !slices.Contains(readBucket(occupied).flags, "energy_stale") {
			t.Fatal("late stages rewrote the occupied stale snapshot")
		}
		allRows, savedCurrent := digest(false), current()
		now = at(today, "09:27")
		post(profile, late, "repeat", http.StatusOK)
		if digest(false) != allRows || current() != savedCurrent {
			t.Fatal("repeat changed history or the current result")
		}
		post(profile, packet("A", 5, 0, "Asia/Singapore", aStart, []float64{5, 0.5, 0.5}, staged, true), "applied", http.StatusOK)
		if current() != savedCurrent || archived() != saved {
			t.Fatal("a correction outside the 21-day window changed current EnergyBank")
		}

		// The new bucket now holds a usable bank. A correction can hide the
		// current value again without replacing that bucket's observation.
		post(profile, packet("yesterday", 3, 0, tz, nightStart, []float64{5, 1, 1}, staged, true), "applied", http.StatusOK)
		occupied = at(today, "09:25")
		saved = archived()
		now = at(today, "09:28")
		post(profile, coarse(4), "applied", http.StatusOK)
		if got := readBriefing(); got.EnergyBankStatus != "stale" || got.EnergyBankCurrent != nil || got.EnergyBank != nil ||
			archived() != saved || slices.Contains(readBucket(occupied).flags, "energy_stale") || digest(true) != observed {
			t.Fatal("reverse transition failed or rewrote the historical observation")
		}
		t.Logf("09:22 late stages restored %d in the occupied interval; repeat left both stores unchanged", *recovered.EnergyBankCurrent)

		beforeDisabled, history := current(), digest(false)
		handler, err = sleepshadow.New(ctx, shadowPool)
		if err != nil {
			t.Fatal(err)
		}
		now = at(today, "09:32")
		whileDisabled := packet("yesterday", 5, 0, tz, nightStart, []float64{5, 1, 1}, staged, true)
		post(profile, whileDisabled, "applied", http.StatusOK)
		if current() != beforeDisabled || digest(false) != history {
			t.Fatal("disabled EnergyBank changed its current result or history")
		}
		now = at(today, "09:35")
		handler = start()
		restored := readBriefing()
		if restored.EnergyBankStatus != "" || restored.EnergyBankCurrent == nil ||
			*restored.EnergyBankCurrent != *recovered.EnergyBankCurrent || current() == beforeDisabled {
			t.Fatalf("startup did not recover the bank after sleep changed while disabled: status=%q number=%v", restored.EnergyBankStatus, restored.EnergyBankCurrent)
		}
		recoveredCurrent := current()
		now = at(today, "09:40")
		handler = start()
		post(profile, whileDisabled, "repeat", http.StatusOK)
		if current() != recoveredCurrent || digest(false) != history {
			t.Fatal("restart or exact repeat changed the recovered bank or rewrote snapshot history")
		}
		t.Logf("09:35 re-enable restored %d after disabled ingest without writing a snapshot", *restored.EnergyBankCurrent)

		// A transfer issued while the writer is disabled must not change the
		// current projection when its generations are all outside the window.
		handler, err = sleepshadow.New(ctx, shadowPool)
		if err != nil {
			t.Fatal(err)
		}
		post(profile, packet("A", 6, 0, "Asia/Singapore", aStart, []float64{5, 0.5, 0.5}, staged, true), "applied", http.StatusOK)
		handler = start()
		if current() != recoveredCurrent || digest(false) != history {
			t.Fatal("starting after an out-of-window correction changed EnergyBank")
		}

		// A projection written by the previous local schema has no saved
		// generations. The next start upgrades that row once.
		exec(`UPDATE sleep_shadow.energy_current SET sleep_generations=NULL WHERE profile=$1 AND tenant_schema=$2`, profile, schema)
		handler = start()
		var recorded bool
		if err := pool.QueryRow(ctx, `SELECT sleep_generations ? $3 FROM sleep_shadow.energy_current
			WHERE profile=$1 AND tenant_schema=$2`, profile, schema, yesterday).Scan(&recorded); err != nil || !recorded {
			t.Fatalf("startup did not record the window for a pre-update current row: %v", err)
		}
		migrated := current()
		handler = start()
		if current() != migrated || digest(false) != history {
			t.Fatal("startup after upgrading the current row rewrote EnergyBank")
		}
	})
}
