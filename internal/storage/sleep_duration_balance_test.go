package storage

import (
	"testing"
	"time"

	"health-receiver/internal/health"
)

func TestCompletedSleepEpisodesMustStayWithinOneCompletePeriodAndNotOverlap(t *testing.T) {
	start := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	coverage := SleepPeriodCoverageCommitment{
		WakeDate: "2026-09-10", SourceEpoch: "health-sync-ios-v1",
		CaptureCompleteness: health.SleepBalanceCoverageComplete, CoverageGeneration: "period-1",
		CoveredIntervalStart: start, CoveredIntervalEnd: start.Add(24 * time.Hour),
		ObservedAt: start.Add(25 * time.Hour), InputHash: "coverage-hash",
	}
	valid := []CompletedSleepEpisodeCommitment{
		testEpisode("one", coverage, start.Add(10*time.Hour), start.Add(14*time.Hour)),
		testEpisode("two", coverage, start.Add(15*time.Hour), start.Add(16*time.Hour)),
	}
	if err := validateCompletedSleepEpisodeCommitments(coverage, valid); err != nil {
		t.Fatalf("valid episodes rejected: %v", err)
	}
	overlapping := append([]CompletedSleepEpisodeCommitment(nil), valid...)
	overlapping[1].Start = start.Add(13 * time.Hour)
	if err := validateCompletedSleepEpisodeCommitments(coverage, overlapping); err == nil {
		t.Fatal("overlapping episodes accepted")
	}
	outside := valid[:1]
	outside[0].Start = start.Add(-time.Minute)
	if err := validateCompletedSleepEpisodeCommitments(coverage, outside); err == nil {
		t.Fatal("episode outside coverage accepted")
	}
}

func TestSleepPeriodSnapshotInputHashChangesForEpisodeOnlyCorrection(t *testing.T) {
	coverage := SleepPeriodCoverageCommitment{InputHash: "coverage-hash"}
	first, err := sleepPeriodSnapshotInputHash(coverage, []CompletedSleepEpisodeCommitment{{InputHash: "episode-a"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := sleepPeriodSnapshotInputHash(coverage, []CompletedSleepEpisodeCommitment{{InputHash: "episode-b"}})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("episode-only correction retained the old coverage hash")
	}
}

func TestValidateSleepPeriodCoverageCommitmentRequiresTenantNoonWindow(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	wake := time.Date(2026, 10, 25, 0, 0, 0, 0, loc)
	end := time.Date(wake.Year(), wake.Month(), wake.Day(), 12, 0, 0, 0, loc)
	coverage := SleepPeriodCoverageCommitment{
		WakeDate: wake.Format("2006-01-02"), SourceEpoch: "ios", CoverageGeneration: "one", InputHash: "hash",
		CaptureCompleteness: health.SleepBalanceCoverageComplete, CoveredIntervalStart: end.AddDate(0, 0, -1), CoveredIntervalEnd: end, ObservedAt: end,
	}
	if err := validateSleepPeriodCoverageCommitment(coverage, loc); err != nil {
		t.Fatalf("DST-aware noon window rejected: %v", err)
	}
	coverage.CoveredIntervalStart = coverage.CoveredIntervalStart.Add(time.Hour)
	if err := validateSleepPeriodCoverageCommitment(coverage, loc); err == nil {
		t.Fatal("shifted coverage window accepted")
	}
}

func TestSleepDurationBalanceInputHashIsStableAcrossInputOrder(t *testing.T) {
	goals := []health.SleepGoal{
		{EffectiveDate: "2026-09-02", Hours: 7.5, Version: "manual-v1"},
		{EffectiveDate: "2026-09-01", Hours: 8, Version: "manual-v1"},
	}
	coverage := []health.SleepBalanceCoverage{
		{WakeDate: "2026-09-02", CaptureState: health.SleepBalanceCoverageComplete, CoverageGeneration: "b"},
		{WakeDate: "2026-09-01", CaptureState: health.SleepBalanceCoverageComplete, CoverageGeneration: "a"},
	}
	start := time.Date(2026, 9, 1, 20, 0, 0, 0, time.UTC)
	episodes := []health.CompletedSleepEpisode{
		{ID: "b", Start: start.Add(24 * time.Hour), End: start.Add(31 * time.Hour), Source: "Watch", InputHash: "b"},
		{ID: "a", Start: start, End: start.Add(7 * time.Hour), Source: "Watch", InputHash: "a"},
	}
	first, err := sleepDurationBalanceInputHash(time.UTC, goals, coverage, episodes)
	if err != nil {
		t.Fatal(err)
	}
	second, err := sleepDurationBalanceInputHash(time.UTC,
		[]health.SleepGoal{goals[1], goals[0]},
		[]health.SleepBalanceCoverage{coverage[1], coverage[0]},
		[]health.CompletedSleepEpisode{episodes[1], episodes[0]},
	)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("input hash changed with order: %s != %s", first, second)
	}
}

func TestSleepDurationBalanceInputHashTracksTimezoneAcrossDST(t *testing.T) {
	belgrade, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	lagos, err := time.LoadLocation("Africa/Lagos")
	if err != nil {
		t.Fatal(err)
	}
	for _, day := range []int{20, 31} {
		a := time.Date(2026, time.October, day, 12, 0, 0, 0, belgrade)
		b := time.Date(2026, time.October, day, 12, 0, 0, 0, lagos)
		if a.Equal(b) != (day == 31) {
			t.Fatal("unexpected offsets in test window")
		}
	}
	a, err := sleepDurationBalanceInputHash(belgrade, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := sleepDurationBalanceInputHash(lagos, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("different historical period boundaries shared a snapshot input hash")
	}
}

func testEpisode(id string, coverage SleepPeriodCoverageCommitment, start, end time.Time) CompletedSleepEpisodeCommitment {
	return CompletedSleepEpisodeCommitment{
		EpisodeID: id, WakeDate: coverage.WakeDate, Start: start, End: end,
		Source: "Apple Watch", SourceEpoch: coverage.SourceEpoch, InputHash: "hash-" + id,
		CoverageGeneration: coverage.CoverageGeneration, CaptureState: health.SleepBalanceCoverageComplete,
		DurationAssessment: health.NightDurationPlausible, ObservedAt: coverage.ObservedAt,
	}
}

func TestSleepDurationBalanceUsesSharedDBTWindowSQL(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	if err := db.EnsureSleepDurationBalanceTablesContext(ctx); err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSettings(map[string]string{"timezone": loc.String()}); err != nil {
		t.Fatal(err)
	}
	wake, _ := time.ParseInLocation(time.DateOnly, "2028-10-30", loc)
	if _, err := db.pool.Exec(ctx, `insert into sleep_goal(effective_date,goal_hours,version,created_at,updated_at)
		values('2028-01-01',8,'synthetic',now(),now())`); err != nil {
		t.Fatal(err)
	}
	for offset := health.SleepDurationBalanceWindowDays - 1; offset >= 0; offset-- {
		day := wake.AddDate(0, 0, -offset)
		end := time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, loc)
		start := end.AddDate(0, 0, -1)
		if _, err := db.pool.Exec(ctx, `insert into sleep_period_coverage(
			wake_date,source_epoch,capture_completeness,coverage_generation,covered_interval_start,covered_interval_end,observed_at,input_hash)
			values($1,'synthetic','complete','generation',$2,$3,$3,$4)`, day.Format(time.DateOnly), start, end, "coverage-"+day.Format(time.DateOnly)); err != nil {
			t.Fatal(err)
		}
		episodeStart := time.Date(day.Year(), day.Month(), day.Day()-1, 23, 30, 0, 0, loc)
		episodeEnd := episodeStart.Add(7 * time.Hour)
		if _, err := db.pool.Exec(ctx, `insert into completed_sleep_episode(
			episode_id,wake_date,start_at,end_at,source,source_epoch,input_hash,coverage_generation,capture_state,duration_assessment,observed_at)
			values($1,$2,$3,$4,'Synthetic Watch','synthetic',$5,'generation','complete','plausible',$6)`,
			"episode-"+day.Format(time.DateOnly), day.Format(time.DateOnly), episodeStart, episodeEnd,
			"episode-"+day.Format(time.DateOnly), end); err != nil {
			t.Fatal(err)
		}
	}
	result, err := db.ReconcileSleepDurationBalance(ctx, wake.Format(time.DateOnly), time.Date(2028, 10, 30, 15, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("reconcile with shared dbt SQL: %v", err)
	}
	if result.State != health.SleepBalanceStateComplete || result.BalanceHours == nil || *result.BalanceHours != -14 || len(result.Periods) != health.SleepDurationBalanceWindowDays {
		t.Fatalf("unexpected shared SQL result: %+v", result)
	}
	if result.CalculatedThrough.Location().String() != loc.String() || result.Periods[0].Start.Location().String() != loc.String() || result.Periods[0].End.Location().String() != loc.String() {
		t.Fatalf("public result lost tenant timezone representation: through=%s start=%s end=%s", result.CalculatedThrough.Location(), result.Periods[0].Start.Location(), result.Periods[0].End.Location())
	}
	stored, err := db.GetSleepDurationBalance(ctx, wake.Format(time.DateOnly))
	if err != nil || stored == nil || stored.BalanceHours == nil || *stored.BalanceHours != -14 || len(stored.Periods) != health.SleepDurationBalanceWindowDays {
		t.Fatalf("snapshot diverged from shared SQL result: %+v %v", stored, err)
	}
}

func TestSleepDurationBalanceEmptyInputsAndNestedCacheTransaction(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	if err := db.EnsureSleepDurationBalanceTablesContext(ctx); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REPORT_TZ", "Europe/Belgrade")
	if err := db.SaveSettings(map[string]string{"timezone": "Europe/Belgrade"}); err != nil {
		t.Fatal(err)
	}
	date := "2028-10-30"
	result, err := db.ReconcileSleepDurationBalance(ctx, date, time.Date(2028, 10, 30, 15, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("empty profile reconciliation: %v", err)
	}
	if result.State != health.SleepBalanceStateIncomplete || result.IncompleteReason != "coverage_missing" || result.BalanceHours != nil || len(result.Periods) != 0 {
		t.Fatalf("empty profile result: %+v", result)
	}

	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	nested, err := unit.ReconcileSleepDurationBalance(ctx, date, time.Date(2028, 10, 30, 15, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("reconcile in existing cache transaction: %v", err)
	}
	if nested.State != result.State || nested.IncompleteReason != result.IncompleteReason {
		t.Fatalf("nested transaction changed empty result: %+v", nested)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
