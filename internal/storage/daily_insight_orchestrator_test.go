package storage

import "testing"

func TestDailyInsightLeaseCoversGenerationDeadline(t *testing.T) {
	if dailyInsightLeaseDuration <= dailyInsightGenerationDeadline {
		t.Fatalf("lease %s must exceed generation deadline %s", dailyInsightLeaseDuration, dailyInsightGenerationDeadline)
	}
}
