package storage

import (
	"context"
	"testing"

	"health-receiver/internal/health"
)

func TestRecoverySleepAssessmentSQLMatchesGoOracle(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ptr := func(v float64) *float64 { return &v }
	rows := []health.SleepRow{
		{Date: "2031-01-01"},
		{Date: "2031-01-02", Total: ptr(0), Core: ptr(1)},
		{Date: "2031-01-03", Total: ptr(3.949), Core: ptr(3.949)},
		{Date: "2031-01-04", Total: ptr(3.95), Core: ptr(3.95)},
		{Date: "2031-01-05", Total: ptr(7), Deep: ptr(2), REM: ptr(2), Core: ptr(3), Awake: ptr(0.5)},
		{Date: "2031-01-06", Total: ptr(7), Deep: ptr(2), Awake: ptr(0.1)},
		{Date: "2031-01-07", Total: ptr(7), Unspecified: ptr(7)},
		{Date: "2031-01-08", Total: ptr(14), Core: ptr(14), Awake: ptr(1)},
		{Date: "2031-01-09", Total: ptr(14.001), Core: ptr(14)},
		{Date: "2031-01-10", Total: ptr(7), Deep: ptr(4), Core: ptr(1)},
	}
	efficiency, captures, err := db.assessRecoverySleepRows(context.Background(), rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		wantEfficiency := health.ComputeSleepEfficiency(row)
		wantCapture := health.ComputeSleepCaptureConfidence(row)
		gotEfficiency, ok := efficiency[row.Date]
		if !ok || gotEfficiency.Eligible != wantEfficiency.Eligible || gotEfficiency.EligibilityReason != wantEfficiency.EligibilityReason || !sameOptionalFloat(gotEfficiency.Efficiency, wantEfficiency.Efficiency) {
			t.Errorf("%s SQL efficiency=%#v, Go oracle=%#v", row.Date, gotEfficiency, wantEfficiency)
		}
		if got, ok := captures[row.Date]; !ok || got != wantCapture {
			t.Errorf("%s SQL capture=%#v, Go oracle=%#v", row.Date, got, wantCapture)
		}
	}
}

func sameOptionalFloat(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
