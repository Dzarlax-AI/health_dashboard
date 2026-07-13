package storage

import "testing"

func TestCalendarAlignedValuesDoesNotRecycleOlderObservation(t *testing.T) {
	values := map[string]float64{"2026-07-10": 40, "2026-07-08": 38}
	if got := calendarAlignedValues(values, "2026-07-11", 30); got != nil {
		t.Fatalf("missing anchor recycled older values: %v", got)
	}
	got := calendarAlignedValues(values, "2026-07-10", 2)
	if len(got) != 1 || got[0] != 40 {
		t.Fatalf("calendar window = %v, want current day only", got)
	}
}
