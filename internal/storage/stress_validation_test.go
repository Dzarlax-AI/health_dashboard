package storage

import (
	"testing"
	"time"
)

func TestStressValidationDateRangeUsesInclusiveWindow(t *testing.T) {
	asOf := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	from, to := stressValidationDateRange(asOf, 30)
	if from != "2026-05-03" || to != "2026-06-01" {
		t.Fatalf("30-day range = %s..%s, want 2026-05-03..2026-06-01", from, to)
	}

	from, to = stressValidationDateRange(asOf, 7)
	if from != "2026-05-26" || to != "2026-06-01" {
		t.Fatalf("7-day range = %s..%s, want 2026-05-26..2026-06-01", from, to)
	}
}
