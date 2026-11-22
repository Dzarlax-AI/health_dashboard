package storage

import (
	"os"
	"testing"
	"time"
)

func TestPreferredSleepSourceReaders(t *testing.T) {
	if os.Getenv("SLEEP_DOCKER_TOKEN") == "" {
		t.Skip("synthetic sleep runner required")
	}
	db, release := testDB(t)
	defer release()
	ctx := t.Context()
	record := insertTestRawRecord(t, db, "source-reader-consistency")
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	_, err = tx.Exec(ctx, `insert into metric_points(health_record_id,metric_name,date,qty,source,quality)
		select $1,metric,daytime,hours,source,'ok' from (values
		('2026-03-02 23:00:00 +0000',7,'Apple Watch'),('2026-03-03 23:00:00 +0000',7,'Apple Watch'),
		('2026-03-02 22:00:00 +0000',7,'RingConn'),('2026-03-03 23:10:00 +0000',1,'RingConn')
		) f(daytime,hours,source) cross join (values ('sleep_core'),('sleep_total')) m(metric)`, record)
	if err != nil {
		t.Fatal(err)
	}
	const day = "2026-03-03"
	for _, tc := range []struct {
		preference, source string
		wake               float64
	}{
		{"", "Apple Watch", 6}, {"RingConn", "RingConn", 5}, {"ringconn", "Apple Watch", 6},
	} {
		if err := unit.SaveSettings(map[string]string{PreferredSleepSourceSetting: tc.preference}); err != nil {
			t.Fatal(err)
		}
		wake, onset, imputed, ok, err := unit.WakeTimeForDateContext(ctx, day, time.UTC)
		if err != nil || !ok || imputed || wake != tc.wake || onset != 23 {
			t.Fatalf("preference %q: wake %v onset %v imputed %v ok %v error %v", tc.preference, wake, onset, imputed, ok, err)
		}
		probe, err := unit.WakeCandidateVariantsForDate(day, time.UTC)
		if err != nil || probe.SelectedSource != tc.source || probe.SleepTotalEnd.Hour() != int(tc.wake) || probe.DetailedSessionEnd.Hour() != int(tc.wake) {
			t.Fatalf("preference %q: probe %+v error %v", tc.preference, probe, err)
		}
		days, err := unit.LoadSleepArchitectureDays(day, "2026-03-04")
		if err != nil || days[day].Source != tc.source || days[day].AsleepHours != 7 || days["2026-03-04"].Source != "Apple Watch" {
			t.Fatalf("preference %q: architecture %+v error %v", tc.preference, days, err)
		}
	}
	// A missing settings projection on an otherwise readable tenant must
	// suppress the result. Renaming inside this fixture transaction rolls back.
	if _, err := tx.Exec(ctx, "alter table settings rename to settings_unavailable"); err != nil {
		t.Fatal(err)
	}
	checkError := func(name string, call func(*DB) bool) {
		t.Helper()
		nested, err := tx.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer nested.Rollback(ctx)
		testUnit := &DB{pool: &cacheTransaction{Tx: nested, ctx: ctx}}
		if !call(testUnit) {
			t.Fatalf("%s used a default after a settings read failure", name)
		}
	}
	checkError("wake", func(d *DB) bool { _, _, err := d.loadWakeCandidateContext(ctx, day, time.UTC); return err != nil })
	checkError("per-date wake", func(d *DB) bool { _, _, _, _, err := d.WakeTimeForDateContext(ctx, day, time.UTC); return err != nil })
	checkError("probe", func(d *DB) bool { _, err := d.WakeCandidateVariantsForDate(day, time.UTC); return err != nil })
	checkError("architecture", func(d *DB) bool { _, err := d.LoadSleepArchitectureDays(day, day); return err != nil })
}
