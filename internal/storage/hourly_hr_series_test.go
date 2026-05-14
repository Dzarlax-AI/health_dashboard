package storage

import (
	"testing"
	"time"
)

func TestEmitHourSlots_FullDay(t *testing.T) {
	loc := time.UTC
	start := time.Date(2026, 5, 13, 7, 0, 0, 0, loc)
	end := time.Date(2026, 5, 13, 22, 0, 0, 0, loc)
	slots := emitHourSlots(start, end, loc)
	if len(slots) != 15 {
		t.Fatalf("07-22 awake window = %d slots, want 15", len(slots))
	}
	if !slots[0].Equal(start) {
		t.Fatalf("first slot = %v, want %v", slots[0], start)
	}
	// End is exclusive — last slot must be 21:00 (start of the 21h hour),
	// not 22:00.
	wantLast := time.Date(2026, 5, 13, 21, 0, 0, 0, loc)
	if !slots[14].Equal(wantLast) {
		t.Fatalf("last slot = %v, want %v", slots[14], wantLast)
	}
}

func TestEmitHourSlots_RoundsStartDownToHour(t *testing.T) {
	// Wake at 07:42 → first hour slot is 07:00 (the hour that contains
	// the awake-window start). Caller filters out partial-hour samples
	// at the SQL layer via the precise start timestamp, but slot
	// alignment must be hour-aligned for SQL aggregates to match.
	loc := time.UTC
	start := time.Date(2026, 5, 13, 7, 42, 0, 0, loc)
	end := time.Date(2026, 5, 13, 22, 0, 0, 0, loc)
	slots := emitHourSlots(start, end, loc)
	wantFirst := time.Date(2026, 5, 13, 7, 0, 0, 0, loc)
	if !slots[0].Equal(wantFirst) {
		t.Fatalf("first slot = %v, want %v (rounded down)", slots[0], wantFirst)
	}
}

func TestEmitHourSlots_CrossMidnight(t *testing.T) {
	// Cross-midnight awake window: wake 08:00 of d, onset 01:00 of d+1.
	// 17 hour-slots: 8-23 (16) + 0 (1) = 17.
	loc := time.UTC
	start := time.Date(2026, 5, 13, 8, 0, 0, 0, loc)
	end := time.Date(2026, 5, 14, 1, 0, 0, 0, loc)
	slots := emitHourSlots(start, end, loc)
	if len(slots) != 17 {
		t.Fatalf("cross-midnight slots = %d, want 17", len(slots))
	}
	// Verify sequence — last slot of d should be 23:00, first of d+1
	// should be 00:00.
	wantD23 := time.Date(2026, 5, 13, 23, 0, 0, 0, loc)
	wantDPlus0 := time.Date(2026, 5, 14, 0, 0, 0, 0, loc)
	if !slots[15].Equal(wantD23) {
		t.Fatalf("slot[15] = %v, want %v (d 23:00)", slots[15], wantD23)
	}
	if !slots[16].Equal(wantDPlus0) {
		t.Fatalf("slot[16] = %v, want %v (d+1 00:00)", slots[16], wantDPlus0)
	}
}

func TestEmitHourSlots_EmptyWindow(t *testing.T) {
	// Degenerate: start == end (zero-width window). No slots.
	loc := time.UTC
	t0 := time.Date(2026, 5, 13, 10, 0, 0, 0, loc)
	slots := emitHourSlots(t0, t0, loc)
	if len(slots) != 0 {
		t.Fatalf("zero-width = %d slots, want 0", len(slots))
	}
}

func TestEmitHourSlots_SubHourWindow(t *testing.T) {
	// Pathological: 30-minute window inside a single hour. Caller
	// should never produce this from a real awake window, but the
	// function must not panic and must emit exactly one slot for the
	// containing hour.
	loc := time.UTC
	start := time.Date(2026, 5, 13, 10, 15, 0, 0, loc)
	end := time.Date(2026, 5, 13, 10, 45, 0, 0, loc)
	slots := emitHourSlots(start, end, loc)
	if len(slots) != 1 {
		t.Fatalf("sub-hour = %d slots, want 1", len(slots))
	}
	wantSlot := time.Date(2026, 5, 13, 10, 0, 0, 0, loc)
	if !slots[0].Equal(wantSlot) {
		t.Fatalf("slot = %v, want %v", slots[0], wantSlot)
	}
}

func TestEmitHourSlots_TZAware(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Skip("Europe/Belgrade tzdata unavailable")
	}
	start := time.Date(2026, 5, 13, 7, 0, 0, 0, loc)
	end := time.Date(2026, 5, 13, 22, 0, 0, 0, loc)
	slots := emitHourSlots(start, end, loc)
	if len(slots) != 15 {
		t.Fatalf("Belgrade window slots = %d, want 15", len(slots))
	}
	// All slots must carry the Belgrade TZ — guard against a future
	// refactor that accidentally converts via .UTC().
	for i, s := range slots {
		_, off := s.Zone()
		if off == 0 {
			t.Fatalf("slot[%d] = %v dropped tz offset", i, s)
		}
	}
}

func TestMinBucketsPerHour(t *testing.T) {
	// Pin the constant — bumping it tightens the §4.4 "coverage_ok"
	// criterion and silently shifts sustained_hr_load across all
	// history. Any change should be intentional, not accidental.
	if MinBucketsPerHour != 3 {
		t.Fatalf("MinBucketsPerHour = %d, want 3 (changing affects §4.4 stress drain across all history)", MinBucketsPerHour)
	}
}
