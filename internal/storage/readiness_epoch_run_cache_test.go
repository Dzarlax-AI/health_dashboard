package storage

import (
	"context"
	"testing"
)

func TestSourceEpochRunCacheResolveUsesNewestConfirmedWindowAndSentinelGaps(t *testing.T) {
	end := "2026-02-28"
	springEnd := "2026-03-02"
	cache := &sourceEpochRunCache{
		windows: []sourceEpochWindow{
			{epochID: SentinelSourceEpoch, startDate: "2026-04-01"},
			{epochID: "spring", startDate: "2026-03-01", endDate: &springEnd},
			{epochID: "winter", startDate: "2026-01-01", endDate: &end},
			{epochID: "initial", startDate: "2014-01-01", endDate: ptrString("2025-12-31")},
		},
		byDate: make(map[string]sourceEpochResolution),
	}
	for _, tc := range []struct {
		date, wantEpoch, wantStart string
	}{
		{date: "2025-12-31", wantEpoch: "initial", wantStart: "2014-01-01"},
		{date: "2026-01-01", wantEpoch: "winter", wantStart: "2026-01-01"},
		{date: "2026-02-28", wantEpoch: "winter", wantStart: "2026-01-01"},
		{date: "2026-03-01", wantEpoch: "spring", wantStart: "2026-03-01"},
		{date: "2026-03-02", wantEpoch: "spring", wantStart: "2026-03-01"},
		{date: "2026-03-03", wantEpoch: SentinelSourceEpoch, wantStart: ""},
		{date: "2026-04-01", wantEpoch: SentinelSourceEpoch, wantStart: ""},
		{date: "2026-04-02", wantEpoch: SentinelSourceEpoch, wantStart: ""},
	} {
		epoch, start := cache.resolve(tc.date)
		if epoch != tc.wantEpoch || start != tc.wantStart {
			t.Errorf("resolve(%s) = (%q, %q), want (%q, %q)", tc.date, epoch, start, tc.wantEpoch, tc.wantStart)
		}
	}
	if _, ok := cache.byDate["2026-02-28"]; !ok {
		t.Fatal("resolution was not cached for the run")
	}
}

func TestReadinessBackfillInvalidRangeReturnsBeforeDatabaseAccess(t *testing.T) {
	var db *DB
	for _, tc := range []struct {
		name, from, to string
	}{
		{name: "invalid", from: "not-a-date", to: "2026-09-30"},
		{name: "reversed", from: "2026-09-30", to: "2026-09-29"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.BackfillRecoveryStabilitySnapshots(tc.from, tc.to); err == nil {
				t.Fatal("expected invalid range error")
			}
		})
	}
}

func TestSourceEpochRunCacheFallsBackAfterPreloadFailureAndDoesNotCacheFailure(t *testing.T) {
	db, cleanup := testIsolatedReadinessDB(t)
	defer cleanup()
	if _, err := db.pool.Exec(context.Background(), `DROP TABLE source_epochs`); err != nil {
		t.Fatalf("drop source_epochs: %v", err)
	}
	cache, err := db.loadSourceEpochRunCache()
	if err != nil {
		t.Fatalf("load fallback cache: %v", err)
	}
	if epoch, start := cache.resolve("2026-01-01"); epoch != SentinelSourceEpoch || start != "" {
		t.Fatalf("missing-table fallback = (%q, %q), want sentinel/no clip", epoch, start)
	}
	db.EnsureReadinessRedesignTables()
	if epoch, start := cache.resolve("2026-01-01"); epoch != InitialSourceEpoch || start != "2014-01-01" {
		t.Fatalf("fallback after catalog recovery = (%q, %q), want initial epoch", epoch, start)
	}
}

func TestSourceEpochRunCacheIsTenantScoped(t *testing.T) {
	dbA, cleanupA := testIsolatedReadinessDB(t)
	defer cleanupA()
	dbB, cleanupB := testIsolatedReadinessDB(t)
	defer cleanupB()
	for _, item := range []struct {
		db      *DB
		epochID string
	}{
		{db: dbA, epochID: "tenant_a_epoch"},
		{db: dbB, epochID: "tenant_b_epoch"},
	} {
		end := "2026-09-29"
		if err := item.db.UpsertSourceEpoch(SourceEpoch{
			EpochID: item.epochID, StartDate: "2026-09-01", EndDate: &end,
			Kind: SourceEpochKindIngest, Description: "synthetic tenant isolation test",
			DetectedBy: DetectedByManual, Confirmed: true,
		}); err != nil {
			t.Fatalf("upsert %s: %v", item.epochID, err)
		}
	}
	cacheA, err := dbA.loadSourceEpochRunCache()
	if err != nil {
		t.Fatalf("load tenant A cache: %v", err)
	}
	cacheB, err := dbB.loadSourceEpochRunCache()
	if err != nil {
		t.Fatalf("load tenant B cache: %v", err)
	}
	for _, tc := range []struct {
		cache *sourceEpochRunCache
		want  string
	}{
		{cacheA, "tenant_a_epoch"},
		{cacheB, "tenant_b_epoch"},
	} {
		epoch, start := tc.cache.resolve("2026-09-15")
		if epoch != tc.want || start != "2026-09-01" {
			t.Errorf("tenant resolver = (%q, %q), want (%q, 2026-09-01)", epoch, start, tc.want)
		}
	}
}

func ptrString(s string) *string { return &s }
