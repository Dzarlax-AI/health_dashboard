package storage

import (
	"testing"
	"time"
)

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

func TestReadinessRedesignRoutineWindow_ClampsFutureDatesToToday(t *testing.T) {
	gotFrom, gotTo, ok := readinessRedesignRoutineWindow(
		[]string{"2026-05-22", "2035-01-01"},
		time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC),
	)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if gotFrom != "2026-05-08" || gotTo != "2026-05-23" {
		t.Fatalf("window = %s..%s, want 2026-05-08..2026-05-23", gotFrom, gotTo)
	}
}

func TestReadinessRedesignRoutineWindow_FutureOnlyNoops(t *testing.T) {
	gotFrom, gotTo, ok := readinessRedesignRoutineWindow(
		[]string{"2035-01-01"},
		time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC),
	)
	if ok {
		t.Fatalf("ok = true, want false with window %s..%s", gotFrom, gotTo)
	}
}

func TestReadinessRedesignRoutineWindow_AllInvalidNoops(t *testing.T) {
	gotFrom, gotTo, ok := readinessRedesignRoutineWindow(
		[]string{"", "bad"},
		time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC),
	)
	if ok {
		t.Fatalf("ok = true, want false with window %s..%s", gotFrom, gotTo)
	}
}

func TestRunReadinessRedesignBackfillForDates_EmptyInputNoops(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	db.RunReadinessRedesignBackfillForDates(nil)
	db.RunReadinessRedesignBackfillForDates([]string{"", "bad"})
}
