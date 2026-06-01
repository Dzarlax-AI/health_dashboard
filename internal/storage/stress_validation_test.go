package storage

import (
	"testing"
	"time"
)

func TestStressValidationDateRangeUsesInclusiveWindow(t *testing.T) {
	asOf := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	from, to := stressValidationDateRange(asOf, 30)
	if from != "2026-05-02" || to != "2026-05-31" {
		t.Fatalf("30-day range = %s..%s, want 2026-05-02..2026-05-31", from, to)
	}

	from, to = stressValidationDateRange(asOf, 7)
	if from != "2026-05-25" || to != "2026-05-31" {
		t.Fatalf("7-day range = %s..%s, want 2026-05-25..2026-05-31", from, to)
	}
}
