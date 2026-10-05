package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/health"
	"health-receiver/internal/testdb"
)

func TestCacheMaintenanceStateStrictDecode(t *testing.T) {
	valid := `{"version":1,"completed_identity":"","target_identity":"","phase":"","from_date":"","next_date":"","end_date":"","generation":0,"dirty":null,"foreground_dates":null}`
	state, err := decodeCacheMaintenanceState(valid)
	if err != nil {
		t.Fatalf("decode valid state: %v", err)
	}
	if state.Dirty == nil || state.ForegroundDates == nil {
		t.Fatalf("state maps were not normalized: %#v", state)
	}
	for _, raw := range []string{
		"{broken",
		strings.TrimSuffix(valid, "}") + `,"unknown":true}`,
		valid + ` {"version":1}`,
		`{"version":1,"completed_identity":"","target_identity":"sha256:x","from_date":"","next_date":"2026-01-01","end_date":"2026-01-02","generation":0,"dirty":{},"foreground_dates":{}}`,
		strings.Replace(valid, `"phase":""`, `"phase":"unknown"`, 1),
	} {
		if _, err := decodeCacheMaintenanceState(raw); err == nil {
			t.Errorf("decodeCacheMaintenanceState(%q) succeeded, want strict error", raw)
		}
	}
}

func TestCacheMaintenanceJournalTransitionsFenceGenerations(t *testing.T) {
	state := emptyCacheMaintenanceState()
	if err := beginCacheMaintenanceState(&state, "identity-a", "2026-01-01", "2026-01-03"); err != nil {
		t.Fatal(err)
	}
	if state.Phase != CacheMaintenancePhaseAggregates || state.FromDate != "2026-01-01" || state.NextDate != "2026-01-01" || state.EndDate != "2026-01-03" {
		t.Fatalf("begin state = %#v", state)
	}
	if err := markCacheDirtyState(&state, []string{"2026-01-02"}); err != nil {
		t.Fatal(err)
	}
	state.ForegroundDates["2026-01-02"] = true
	captured := map[string]uint64{"2026-01-02": state.Dirty["2026-01-02"]}
	if err := markCacheDirtyState(&state, []string{"2026-01-02"}); err != nil {
		t.Fatal(err)
	}
	if state.Dirty["2026-01-02"] == captured["2026-01-02"] {
		t.Fatal("repeated dirty date did not advance its generation")
	}
	for date, generation := range captured {
		if state.Dirty[date] == generation {
			delete(state.Dirty, date)
		}
	}
	if state.Dirty["2026-01-02"] == 0 || !state.ForegroundDates["2026-01-02"] {
		t.Fatalf("new dirty generation or foreground protection was lost: %#v", state)
	}

	if err := advanceCacheMaintenanceState(&state, "2026-01-02"); err != nil {
		t.Fatal(err)
	}
	if state.FromDate != "2026-01-01" || state.NextDate != "2026-01-02" {
		t.Fatalf("advance discarded start date or failed cursor: %#v", state)
	}
	if err := beginCacheMaintenanceState(&state, "identity-a", "2026-01-01", "2026-01-03"); err != nil {
		t.Fatal(err)
	}
	if state.NextDate != "2026-01-02" {
		t.Fatalf("same-identity begin reset resumable cursor: %#v", state)
	}
	if err := advanceCacheMaintenanceState(&state, ""); err != nil {
		t.Fatal(err)
	}
	if state.Phase != CacheMaintenancePhaseBaseline || state.NextDate != state.FromDate {
		t.Fatalf("aggregate phase did not precede baseline: %#v", state)
	}
	for _, wantPhase := range []string{
		CacheMaintenancePhaseSustained,
		CacheMaintenancePhaseRecovery,
		CacheMaintenancePhasePassive,
		CacheMaintenancePhaseAcute,
		CacheMaintenancePhaseChronic,
		CacheMaintenancePhaseDerived,
		CacheMaintenancePhaseComplete,
	} {
		if err := advanceCacheMaintenanceState(&state, ""); err != nil {
			t.Fatal(err)
		}
		if state.Phase != wantPhase {
			t.Fatalf("phase = %q, want %q", state.Phase, wantPhase)
		}
	}
	if err := finishCacheMaintenanceState(&state); err == nil {
		t.Fatal("finish succeeded with dirty dates")
	}
	state.Dirty = map[string]uint64{}
	if err := finishCacheMaintenanceState(&state); err != nil {
		t.Fatal(err)
	}
	if state.CompletedIdentity != "identity-a" || state.TargetIdentity != "" || len(state.ForegroundDates) != 0 {
		t.Fatalf("finish did not publish/clear state correctly: %#v", state)
	}
}

func TestBeginCacheMaintenanceBaselinesUnknownIdentityAndResetsProtectionOnChange(t *testing.T) {
	state := emptyCacheMaintenanceState()
	if err := markCacheDirtyState(&state, []string{"2026-02-01"}); err != nil {
		t.Fatal(err)
	}
	if err := beginCacheMaintenanceState(&state, "identity-a", "2026-01-01", "2026-02-28"); err != nil {
		t.Fatal(err)
	}
	if state.TargetIdentity != "identity-a" || state.NextDate != "2026-01-01" || !state.ForegroundDates["2026-02-01"] {
		t.Fatalf("unknown identity did not start a protected full range: %#v", state)
	}
	state.TargetIdentity = ""
	state.CompletedIdentity = "identity-a"
	state.FromDate, state.NextDate, state.EndDate = "", "", ""
	state.ForegroundDates = map[string]bool{"2025-01-01": true, "2026-02-01": true}
	state.Dirty = map[string]uint64{"2026-02-01": state.Generation}
	if err := beginCacheMaintenanceState(&state, "identity-a", "2026-01-01", "2026-02-28"); err != nil {
		t.Fatal(err)
	}
	if state.TargetIdentity != "" || state.NextDate != "" {
		t.Fatalf("same completed identity with dirty-only work started history: %#v", state)
	}
	state.CompletedIdentity = "identity-other"
	state.TargetIdentity = "identity-old"
	state.Phase = CacheMaintenancePhaseAggregates
	state.FromDate = "2026-01-01"
	state.NextDate = "2026-02-01"
	state.EndDate = "2026-02-28"
	if err := beginCacheMaintenanceState(&state, "identity-new", "2026-01-01", "2026-02-28"); err != nil {
		t.Fatal(err)
	}
	if state.TargetIdentity != "identity-new" || state.Phase != CacheMaintenancePhaseAggregates || state.NextDate != "2026-01-01" {
		t.Fatalf("identity switch did not restart the cursor: %#v", state)
	}
	if len(state.ForegroundDates) != 1 || !state.ForegroundDates["2026-02-01"] {
		t.Fatalf("identity switch preserved stale foreground dates: %#v", state.ForegroundDates)
	}
}

func TestCacheMaintenanceIdentityIncludesCurrentContracts(t *testing.T) {
	one := CacheMaintenanceIdentity()
	if !strings.HasPrefix(one, "sha256:") || len(one) != len("sha256:")+64 {
		t.Fatalf("identity = %q, want sha256 hex", one)
	}
	if two := CacheMaintenanceIdentity(); two != one {
		t.Fatalf("identity changed without contract change: %q != %q", one, two)
	}
}

func TestCacheMaintenanceJournalDatabaseRoundTripAndIsolation(t *testing.T) {
	dsn := testdb.DSN(t)
	left, cleanupLeft := newCacheMaintenanceTestDB(t, dsn, "cache_journal_left")
	defer cleanupLeft()
	right, cleanupRight := newCacheMaintenanceTestDB(t, dsn, "cache_journal_right")
	defer cleanupRight()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state, err := left.LoadCacheMaintenance(ctx)
	if err != nil {
		t.Fatalf("load absent journal: %v", err)
	}
	if state.TargetIdentity != "" || state.CompletedIdentity != "" || len(state.Dirty) != 0 {
		t.Fatalf("initial journal state = %#v", state)
	}

	state, err = left.BeginCacheMaintenance(ctx, "2026-01-01", "2026-01-03")
	if err != nil {
		t.Fatalf("begin journal: %v", err)
	}
	if state.TargetIdentity != CacheMaintenanceIdentity() {
		t.Fatalf("target identity = %q", state.TargetIdentity)
	}
	if _, err := left.AdvanceCacheMaintenance(ctx, "2026-01-02"); err != nil {
		t.Fatalf("advance journal: %v", err)
	}
	state, err = left.BeginCacheMaintenance(ctx, "2025-01-01", "2026-01-03")
	if err != nil {
		t.Fatalf("resume journal: %v", err)
	}
	if state.NextDate != "2026-01-02" || state.FromDate != "2026-01-01" {
		t.Fatalf("resume reset cursor/start date: %#v", state)
	}
	if err := left.MarkCacheDirty(ctx, []string{"2026-01-02", "2026-01-02"}); err != nil {
		t.Fatalf("mark dirty: %v", err)
	}
	captured, err := left.CaptureCacheDirty(ctx, nil)
	if err != nil {
		t.Fatalf("capture all dirty: %v", err)
	}
	if len(captured) != 1 || captured["2026-01-02"] == 0 {
		t.Fatalf("captured generations = %#v", captured)
	}
	tx, err := left.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin marker rollback fixture: %v", err)
	}
	if err := left.MarkCacheDirtyTx(ctx, tx, []string{"2026-01-03"}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("mark transaction-owned dirty date: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback marker fixture: %v", err)
	}
	rolledBack, err := left.CaptureCacheDirty(ctx, []string{"2026-01-03"})
	if err != nil {
		t.Fatalf("capture rolled-back marker: %v", err)
	}
	if len(rolledBack) != 0 {
		t.Fatalf("transaction rollback left a dirty marker: %#v", rolledBack)
	}
	if err := left.MarkCacheDirty(ctx, []string{"2026-01-02"}); err != nil {
		t.Fatalf("remark dirty: %v", err)
	}
	if err := left.CompleteCacheDirty(ctx, captured); err != nil {
		t.Fatalf("complete old generation: %v", err)
	}
	state, err = left.LoadCacheMaintenance(ctx)
	if err != nil {
		t.Fatalf("load after generation completion: %v", err)
	}
	if state.Dirty["2026-01-02"] == captured["2026-01-02"] || !state.ForegroundDates["2026-01-02"] {
		t.Fatalf("new generation/protection lost: %#v", state)
	}
	if _, err := right.LoadCacheMaintenance(ctx); err != nil {
		t.Fatalf("load second tenant journal: %v", err)
	}
	rightState, err := right.LoadCacheMaintenance(ctx)
	if err != nil {
		t.Fatalf("reload second tenant journal: %v", err)
	}
	if rightState.TargetIdentity != "" || len(rightState.Dirty) != 0 {
		t.Fatalf("journal leaked across tenant schemas: %#v", rightState)
	}

	if _, err := left.pool.Exec(ctx, `UPDATE settings SET value = '{broken' WHERE key = $1`, cacheMaintenanceSettingKey); err != nil {
		t.Fatalf("corrupt journal fixture: %v", err)
	}
	if _, err := left.LoadCacheMaintenance(ctx); err == nil {
		t.Fatal("corrupt stored journal loaded without error")
	}
}

func TestImportCommitMarksCoverageDatesDirty(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	session := beginTestXMLImport(t, db, "cache-maintenance-import-marker")
	if err := session.AddPoints([]MetricPoint{{
		MetricName: "heart_rate",
		Units:      "count/min",
		Date:       "2026-07-01 10:00:00 +0200",
		Qty:        61,
		Source:     "Apple Watch",
	}}); err != nil {
		t.Fatalf("stage point: %v", err)
	}
	if _, err := session.Commit(); err != nil {
		t.Fatalf("commit import: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state, err := db.LoadCacheMaintenance(ctx)
	if err != nil {
		t.Fatalf("load cache journal: %v", err)
	}
	if state.Dirty["2026-07-01"] == 0 {
		t.Fatalf("committed import did not leave a dirty coverage date: %#v", state.Dirty)
	}
}

func TestCacheMaintenanceRunnerResumesPhaseAndSkipsCleanDateScan(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	today := db.Today()
	state, err := db.BeginCacheMaintenance(ctx, today, today)
	if err != nil {
		t.Fatalf("begin maintenance: %v", err)
	}
	for i := 0; i < 7; i++ {
		state, err = db.AdvanceCacheMaintenance(ctx, "")
		if err != nil {
			t.Fatalf("advance maintenance to derived phase: %v", err)
		}
	}
	if state.Phase != CacheMaintenancePhaseDerived || state.NextDate != today {
		t.Fatalf("prepared state = %#v, want derived cursor at today", state)
	}

	originalPool := db.pool
	probe := &cacheDateRangeProbePool{storagePool: originalPool}
	db.pool = probe
	defer func() { db.pool = originalPool }()
	finishCalls := 0
	finish := func(dates []string) error {
		finishCalls++
		if len(dates) != 0 {
			t.Errorf("unexpected dirty dates at finish: %v", dates)
		}
		return nil
	}
	if err := db.RunCacheMaintenance(ctx, "UTC", finish); err != nil {
		t.Fatalf("resume maintenance runner: %v", err)
	}
	if probe.rangeQueries != 0 {
		t.Fatalf("same-target phase resume scanned source range %d times", probe.rangeQueries)
	}
	state, err = db.LoadCacheMaintenance(ctx)
	if err != nil {
		t.Fatalf("load completed maintenance: %v", err)
	}
	if state.TargetIdentity != "" || state.CompletedIdentity != CacheMaintenanceIdentity() {
		t.Fatalf("maintenance was not completed: %#v", state)
	}

	probe.rangeQueries = 0
	if err := db.RunCacheMaintenance(ctx, "UTC", finish); err != nil {
		t.Fatalf("run already-completed maintenance: %v", err)
	}
	if probe.rangeQueries != 0 {
		t.Fatalf("completed clean maintenance scanned source range %d times", probe.rangeQueries)
	}
	if finishCalls != 2 {
		t.Fatalf("finish callback count = %d, want one per run", finishCalls)
	}
}

func TestRepairHistoricalDependenciesMatchesWriterMajorMaintenance(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	today := time.Now().UTC().Truncate(24 * time.Hour)
	affected := today.AddDate(0, 0, -17).Format(isoDate)
	routineFrom, _, ok := readinessRedesignRoutineWindow([]string{affected}, today)
	if !ok {
		t.Fatal("affected date was rejected by readiness window")
	}
	routineFromTime, _ := time.Parse(isoDate, routineFrom)
	repairFrom := routineFromTime.AddDate(0, 0, -(health.ChronicLoadForwardWindowDays + 3 - readinessRedesignRoutineLookbackDays)).Format(isoDate)
	fullFrom := today.AddDate(0, 0, -55).Format(isoDate)
	if repairFrom != today.AddDate(0, 0, -34).Format(isoDate) {
		t.Fatalf("C-17 dependency repair starts %s, want C-34", repairFrom)
	}

	// Keep enough dated input for the causal lookbacks and forward labels.
	if _, err := db.pool.Exec(ctx, `
		INSERT INTO daily_scores (date, hrv_avg, rhr_avg, sleep_total, sleep_deep, sleep_rem, sleep_core, sleep_awake, steps, exercise_min, calories, sustained_hr_load, stress_flags)
		SELECT d::date::text, 52 + (extract(doy from d)::int % 9), 54 + (extract(doy from d)::int % 5),
		       7.1, 1.3, 1.7, 3.8, 0.3, 7000, 35, 500, 0, '{}'
		  FROM generate_series($1::date, $2::date + 17, interval '1 day') AS d
	`, fullFrom, today.Format(isoDate)); err != nil {
		t.Fatalf("seed daily scores: %v", err)
	}
	missingForwardNight := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -14).Format(isoDate)
	if _, err := db.pool.Exec(ctx, `DELETE FROM daily_scores WHERE date=$1`, missingForwardNight); err != nil {
		t.Fatalf("remove one forward night while retaining later observations: %v", err)
	}
	if err := db.UpsertSourceEpoch(SourceEpoch{EpochID: InitialSourceEpoch, StartDate: "2014-01-01", Kind: SourceEpochKindIngest, DetectedBy: DetectedByManual, Confirmed: true}); err != nil {
		t.Fatalf("seed initial source epoch: %v", err)
	}
	db.EnsureEnergySnapshotsTable()
	for _, liveDate := range []string{today.AddDate(0, 0, -2).Format(isoDate), today.AddDate(0, 0, -1).Format(isoDate)} {
		for _, clock := range []string{"12:30", "23:55"} {
			ts, parseErr := time.ParseInLocation("2006-01-02 15:04", liveDate+" "+clock, time.UTC)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			if _, err := db.pool.Exec(ctx, `INSERT INTO energy_snapshots (ts_bucket,date,bank,drain_delta,restore_delta,formula_version,components,flags) VALUES ($1,$2,11,1,2,2,'{}','{}')`, ts, liveDate); err != nil {
				t.Fatalf("seed live Energy snapshot %s %s: %v", liveDate, clock, err)
			}
		}
	}

	loc, err := time.LoadLocation("UTC")
	if err != nil {
		t.Fatal(err)
	}
	for date := fullFrom; date <= today.Format(isoDate); {
		if err := db.upsertBaselineHROvernightForDateContext(ctx, date, loc); err != nil {
			t.Fatalf("writer-major baseline %s: %v", date, err)
		}
		if _, err := db.upsertSustainedHRLoadForDateContext(ctx, date, loc); err != nil {
			t.Fatalf("writer-major sustained load %s: %v", date, err)
		}
		date, err = addDay(date)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, writer := range []struct {
		name string
		run  func(string, string) (int, error)
	}{
		{"recovery", db.BackfillRecoveryStabilitySnapshots},
		{"passive", db.BackfillPassiveEfficiencySnapshots},
		{"acute", db.BackfillAcuteRiskSnapshots},
		{"chronic", db.BackfillChronicLoadSnapshots},
	} {
		if _, err := writer.run(fullFrom, today.Format(isoDate)); err != nil {
			t.Fatalf("writer-major %s: %v", writer.name, err)
		}
	}
	for date := fullFrom; date <= today.Format(isoDate); {
		if err := db.rebuildMaintenanceDependencies(ctx, date, "UTC"); err != nil {
			t.Fatalf("writer-major derived %s: %v", date, err)
		}
		date, err = addDay(date)
		if err != nil {
			t.Fatal(err)
		}
	}

	wantTargets := readinessMaintenanceDigest(t, ctx, db, "target_snapshots", repairFrom)
	wantFeatures := readinessMaintenanceDigest(t, ctx, db, "feature_snapshots", repairFrom)
	wantBaselines := readinessMaintenanceDigest(t, ctx, db, "naive_baselines", repairFrom)
	wantEnergy := energyMaintenanceDigest(t, ctx, db, repairFrom)
	var regeneratedEnergyRows int
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM energy_snapshots WHERE date >= $1 AND 'backfilled'=ANY(flags)`, repairFrom).Scan(&regeneratedEnergyRows); err != nil {
		t.Fatalf("count reference synthetic Energy rows: %v", err)
	}
	if regeneratedEnergyRows == 0 {
		t.Fatal("reference writer-major pass produced no synthetic Energy snapshots")
	}
	for _, table := range []string{"target_snapshots", "feature_snapshots", "naive_baselines"} {
		if _, err := db.pool.Exec(ctx, `DELETE FROM `+table+` WHERE date >= $1`, repairFrom); err != nil {
			t.Fatalf("clear %s repair window: %v", table, err)
		}
	}
	if _, err := db.pool.Exec(ctx, `DELETE FROM energy_snapshots WHERE date >= $1 AND 'backfilled'=ANY(flags)`, repairFrom); err != nil {
		t.Fatalf("remove synthetic Energy rows before repair: %v", err)
	}
	if err := db.RepairHistoricalDependencies(ctx, []string{affected}, "UTC", today); err != nil {
		t.Fatalf("repair C-17 dependency suffix: %v", err)
	}
	if got := readinessMaintenanceDigest(t, ctx, db, "target_snapshots", repairFrom); got != wantTargets {
		t.Fatalf("target snapshots differ between writer-major and repair paths: %s", firstJSONDifference(wantTargets, got))
	}
	if got := readinessMaintenanceDigest(t, ctx, db, "feature_snapshots", repairFrom); got != wantFeatures {
		t.Fatalf("feature snapshots differ between writer-major and repair paths: %s", firstJSONDifference(wantFeatures, got))
	}
	if got := readinessMaintenanceDigest(t, ctx, db, "naive_baselines", repairFrom); got != wantBaselines {
		t.Fatalf("naive baselines differ between writer-major and repair paths: %s", firstJSONDifference(wantBaselines, got))
	}
	if got := energyMaintenanceDigest(t, ctx, db, repairFrom); got != wantEnergy {
		t.Fatalf("Energy snapshots differ between writer-major and repair paths: %s", firstJSONDifference(wantEnergy, got))
	}
	var candidateEligible, mature bool
	if err := db.pool.QueryRow(ctx, `SELECT eligible, (data_coverage->>'candidate_2of3_window_mature')::boolean FROM target_snapshots WHERE date=$1 AND sub_score=$2 AND target_kind=$3`, affected, SubScoreRecoveryStability, TargetKindRolling3dCandidate2of3).Scan(&candidateEligible, &mature); err != nil {
		t.Fatalf("read missing-night candidate target: %v", err)
	}
	if !candidateEligible || !mature {
		t.Fatalf("candidate target with one missing forward night eligible=%v mature=%v; want both true because later observations remain", candidateEligible, mature)
	}
	var firstDateCount int
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM target_snapshots WHERE date=$1`, repairFrom).Scan(&firstDateCount); err != nil {
		t.Fatalf("verify widened first repair date: %v", err)
	}
	if firstDateCount == 0 {
		t.Fatalf("repair did not include widened C-34 date %s", repairFrom)
	}
}

func readinessMaintenanceDigest(t *testing.T, ctx context.Context, db *DB, table, from string) string {
	t.Helper()
	if table != "target_snapshots" && table != "feature_snapshots" && table != "naive_baselines" {
		t.Fatalf("unexpected readiness table %q", table)
	}
	var digest string
	query := `SELECT COALESCE(jsonb_agg(to_jsonb(r) - 'computed_at' ORDER BY r.date, r.sub_score), '[]'::jsonb)::text FROM ` + table + ` r WHERE r.date >= $1`
	if table == "target_snapshots" || table == "naive_baselines" {
		query = `SELECT COALESCE(jsonb_agg(to_jsonb(r) - 'computed_at' ORDER BY r.date, r.sub_score, r.target_kind), '[]'::jsonb)::text FROM ` + table + ` r WHERE r.date >= $1`
	}
	if table == "naive_baselines" {
		query = `SELECT COALESCE(jsonb_agg(to_jsonb(r) - 'computed_at' ORDER BY r.date, r.sub_score, r.target_kind, r.baseline_kind), '[]'::jsonb)::text FROM naive_baselines r WHERE r.date >= $1`
	}
	if err := db.pool.QueryRow(ctx, query, from).Scan(&digest); err != nil {
		t.Fatalf("digest %s: %v", table, err)
	}
	return digest
}

func energyMaintenanceDigest(t *testing.T, ctx context.Context, db *DB, from string) string {
	t.Helper()
	var digest string
	if err := db.pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(e) - 'computed_at' ORDER BY e.ts_bucket), '[]'::jsonb)::text FROM energy_snapshots e WHERE e.date >= $1`, from).Scan(&digest); err != nil {
		t.Fatalf("digest energy_snapshots: %v", err)
	}
	return digest
}

func firstJSONDifference(want, got string) string {
	var wantRows, gotRows []json.RawMessage
	if err := json.Unmarshal([]byte(want), &wantRows); err != nil {
		return fmt.Sprintf("decode expected rows: %v", err)
	}
	if err := json.Unmarshal([]byte(got), &gotRows); err != nil {
		return fmt.Sprintf("decode actual rows: %v", err)
	}
	for i := 0; i < len(wantRows) && i < len(gotRows); i++ {
		var left, right bytes.Buffer
		_ = json.Compact(&left, wantRows[i])
		_ = json.Compact(&right, gotRows[i])
		if left.String() != right.String() {
			return fmt.Sprintf("row %d: expected %s; got %s", i, left.String(), right.String())
		}
	}
	return fmt.Sprintf("row counts differ: expected %d, got %d", len(wantRows), len(gotRows))
}

type cacheDateRangeProbePool struct {
	storagePool
	rangeQueries int
}

func (p *cacheDateRangeProbePool) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if strings.Contains(query, "SELECT MIN(day), MAX(day)") {
		p.rangeQueries++
	}
	return p.storagePool.QueryRow(ctx, query, args...)
}

var _ storagePool = (*cacheDateRangeProbePool)(nil)

func newCacheMaintenanceTestDB(t *testing.T, dsn, prefix string) (*DB, func()) {
	t.Helper()
	schema := testdb.SchemaName(prefix)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bootstrap, err := testdb.NewPool(ctx, dsn, "")
	if err != nil {
		t.Fatalf("open bootstrap database: %v", err)
	}
	if err := testdb.CreateSchema(ctx, bootstrap, schema); err != nil {
		bootstrap.Close()
		t.Fatalf("create schema %s: %v", schema, err)
	}
	pool, err := testdb.NewPool(ctx, dsn, schema)
	if err != nil {
		_ = testdb.DropSchema(ctx, bootstrap, schema)
		bootstrap.Close()
		t.Fatalf("open test schema %s: %v", schema, err)
	}
	bootstrap.Close()
	if _, err := pool.Exec(ctx, `CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT NOW()::TEXT)`); err != nil {
		pool.Close()
		t.Fatalf("create settings fixture: %v", err)
	}
	db := NewFromPool(pool)
	cleanup := func() {
		db.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		admin, err := testdb.NewPool(dropCtx, dsn, "")
		if err != nil {
			t.Errorf("open cleanup pool: %v", err)
			return
		}
		defer admin.Close()
		if err := testdb.DropSchema(dropCtx, admin, schema); err != nil {
			t.Errorf("drop test schema %s: %v", schema, err)
		}
	}
	return db, cleanup
}

func TestCacheMaintenanceDoesNotPublishNewDirtyGeneration(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	date := db.Today()
	if _, err := db.BeginCacheMaintenance(ctx, date, date); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := db.AdvanceCacheMaintenance(ctx, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.FinishCacheMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.refreshDashboardSnapshotLocked(ctx); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := db.pool.QueryRow(ctx, `SELECT completed_at::text FROM dashboard_cache_snapshots`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	err := db.RunCacheMaintenance(ctx, "UTC", func([]string) error { return db.MarkCacheDirty(ctx, []string{date}) })
	if err == nil {
		t.Fatal("new correction incorrectly published")
	}
	var after string
	if err := db.pool.QueryRow(ctx, `SELECT completed_at::text FROM dashboard_cache_snapshots`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("incomplete generation advanced dashboard snapshot")
	}
	state, err := db.LoadCacheMaintenance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Dirty) != 1 {
		t.Fatalf("new correction was lost: %#v", state.Dirty)
	}
}
