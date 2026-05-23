# Readiness Freshness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Phase 0 readiness chip inputs refresh automatically after tenant-local ingest, while keeping ingest fast and making stale source-epoch rows render as explicit unknowns.

**Architecture:** Add a tenant-bound storage orchestrator that expands affected dates to the routine serving window, then runs the four Phase 0 writers sequentially. Call it from the existing debounced date-aware backfill path and from the daily 03:00 quality-scan path. Extend operational-contract rows so serving can detect stored/source epoch mismatches and render `source_epoch_change`.

**Tech Stack:** Go, pgx/PostgreSQL, existing `internal/storage` writer APIs, existing admin operational-contract tests.

---

## Task 1: Storage Orchestrator

**Files:**
- Create: `internal/storage/readiness_redesign_orchestrator.go`
- Test: `internal/storage/readiness_redesign_orchestrator_test.go`

- [ ] **Step 1: Write failing tests for date expansion and sequential dependency order**

Add tests that call a pure helper:

```go
func TestReadinessRedesignRoutineWindow_ExpandsBack14AndCapsAtToday(t *testing.T) {
	gotFrom, gotTo, ok := readinessRedesignRoutineWindow(
		[]string{"2026-05-20", "2026-05-22"},
		time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC),
	)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if gotFrom != "2026-05-06" || gotTo != "2026-05-23" {
		t.Fatalf("window = %s..%s, want 2026-05-06..2026-05-23", gotFrom, gotTo)
	}
}

func TestReadinessRedesignRoutineWindow_IgnoresInvalidDates(t *testing.T) {
	gotFrom, gotTo, ok := readinessRedesignRoutineWindow(
		[]string{"", "bad", "2026-05-22T10:00:00+02:00"},
		time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC),
	)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if gotFrom != "2026-05-08" || gotTo != "2026-05-23" {
		t.Fatalf("window = %s..%s, want 2026-05-08..2026-05-23", gotFrom, gotTo)
	}
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run: `go test ./internal/storage -run TestReadinessRedesignRoutineWindow -count=1`

Expected: FAIL with `undefined: readinessRedesignRoutineWindow`.

- [ ] **Step 3: Implement minimal orchestrator**

Create `internal/storage/readiness_redesign_orchestrator.go`:

```go
package storage

import (
	"log"
	"time"
)

const readinessRedesignRoutineLookbackDays = 14

func readinessRedesignRoutineWindow(dates []string, today time.Time) (string, string, bool) {
	var minDate, maxDate string
	for _, raw := range dates {
		if len(raw) < len(isoDate) {
			continue
		}
		d := raw[:len(isoDate)]
		if _, err := time.Parse(isoDate, d); err != nil {
			continue
		}
		if minDate == "" || d < minDate {
			minDate = d
		}
		if maxDate == "" || d > maxDate {
			maxDate = d
		}
	}
	if minDate == "" {
		return "", "", false
	}
	todayDate := today.Format(isoDate)
	if maxDate < todayDate {
		maxDate = todayDate
	}
	minT, _ := time.Parse(isoDate, minDate)
	return minT.AddDate(0, 0, -readinessRedesignRoutineLookbackDays).Format(isoDate), maxDate, true
}

func (s *DB) RunReadinessRedesignBackfillForDates(dates []string) {
	from, to, ok := readinessRedesignRoutineWindow(dates, time.Now())
	if !ok {
		return
	}
	log.Printf("readiness redesign backfill: starting %s..%s", from, to)
	if n, err := s.BackfillRecoveryStabilitySnapshots(from, to); err != nil {
		log.Printf("readiness redesign backfill recovery_stability: wrote=%d err=%v", n, err)
	}
	if n, err := s.BackfillPassiveEfficiencySnapshots(from, to); err != nil {
		log.Printf("readiness redesign backfill passive_efficiency: wrote=%d err=%v", n, err)
	}
	if n, err := s.BackfillAcuteRiskSnapshots(from, to); err != nil {
		log.Printf("readiness redesign backfill acute_risk: wrote=%d err=%v", n, err)
	}
	if n, err := s.BackfillChronicLoadSnapshots(from, to); err != nil {
		log.Printf("readiness redesign backfill chronic_load: wrote=%d err=%v", n, err)
	}
	log.Printf("readiness redesign backfill: done %s..%s", from, to)
}
```

- [ ] **Step 4: Run tests and verify they pass**

Run: `go test ./internal/storage -run TestReadinessRedesignRoutineWindow -count=1`

Expected: PASS.

## Task 2: Wire Ingest Freshness

**Files:**
- Modify: `internal/storage/scores.go`
- Test: `internal/storage/readiness_redesign_orchestrator_test.go`

- [ ] **Step 1: Add a focused test for the hook helper only**

Add a test for the public method's empty/invalid input behavior so the ingest hook can call it without guards:

```go
func TestRunReadinessRedesignBackfillForDates_EmptyInputNoops(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	db.RunReadinessRedesignBackfillForDates(nil)
	db.RunReadinessRedesignBackfillForDates([]string{"", "bad"})
}
```

- [ ] **Step 2: Wire the hook after cache refresh**

Modify `internal/storage/scores.go`:

```go
func (s *DB) RunIncrementalBackfillForDates(dates []string) {
	if len(dates) == 0 {
		return
	}
	s.UpsertRecentCache(dates, true)
	s.RunReadinessRedesignBackfillForDates(dates)
}
```

- [ ] **Step 3: Run storage tests**

Run: `go test ./internal/storage -run 'Test(ReadinessRedesignRoutineWindow|RunReadinessRedesignBackfillForDates)' -count=1`

Expected: PASS.

## Task 3: Daily Re-Stamp

**Files:**
- Modify: `cmd/server/main.go`
- Test: `cmd/server/main.go` compile through package tests

- [ ] **Step 1: Add the call after the quality scan pass**

In `runDailyQualityScan`, after the log block for `MarkSuspectPoints`, add:

```go
today := time.Now().In(loc).Format("2006-01-02")
db.RunReadinessRedesignBackfillForDates([]string{today})
```

This reuses the same local `loc` chosen for the 03:00 scan and expands to `today-14d..today` inside storage.

- [ ] **Step 2: Run compile-focused tests**

Run: `go test ./cmd/server ./internal/storage -run TestReadinessRedesignRoutineWindow -count=1`

Expected: PASS.

## Task 4: Source-Epoch Change Rendering

**Files:**
- Modify: `internal/storage/readiness_redesign.go`
- Modify: `internal/ui/handler.go`
- Test: `internal/storage/operational_contract_test.go`
- Test: `internal/ui/admin_readiness_redesign_contract_test.go`

- [ ] **Step 1: Extend the storage row**

Add fields to `OperationalContractRow`:

```go
CurrentSourceEpoch *string
SourceEpochChanged bool
```

In `LoadOperationalContractRows`, after scanning each row, resolve the current epoch for `r.Date`. If both stored and current epochs are non-empty and differ, set `SourceEpochChanged = true`.

- [ ] **Step 2: Update chip rendering**

At the top of `buildChipCell`, before predicted-value handling:

```go
if row.SourceEpochChanged {
	cell.Text = "unknown"
	cell.Title = "baseline=source_epoch_change"
	return cell
}
```

- [ ] **Step 3: Add tests**

Storage test: seed a row under `initial`, add a new confirmed source epoch covering that date, call `LoadOperationalContractRows`, and assert `SourceEpochChanged == true`.

UI test: construct a `storage.OperationalContractRow{SourceEpochChanged: true}` and assert `buildChipCell(row).Text == "unknown"` and title contains `source_epoch_change`.

- [ ] **Step 4: Run focused tests**

Run: `go test ./internal/storage ./internal/ui -run 'TestLoadOperationalContractRows|TestBuildChipCell|TestAdminReadinessRedesign' -count=1`

Expected: PASS.

## Task 5: Verification

**Files:**
- Modify: `READINESS_REDESIGN_PLAN.md` only if implementation differs from the approved contract

- [ ] **Step 1: Run package tests**

Run: `go test ./internal/storage ./internal/ui ./cmd/server -count=1`

Expected: PASS.

- [ ] **Step 2: Run broader build check**

Run: `go test ./...`

Expected: PASS, unless unrelated existing integration prerequisites are missing. If blocked, capture the exact failing package and reason.

- [ ] **Step 3: Review diff**

Run: `git diff --stat` and `git diff --check`.

Expected: no whitespace errors; changed files match this plan.
