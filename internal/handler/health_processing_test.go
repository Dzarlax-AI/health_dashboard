package handler

import (
	"errors"
	"testing"

	"health-receiver/internal/storage"
)

func TestAcceptedPointsAfterStatusUpdatePreservesInsertedPoints(t *testing.T) {
	points := []storage.MetricPoint{{MetricName: "step_count", Date: "2026-07-13 10:00:00 +0000", Qty: 42}}

	got, err := acceptedPointsAfterStatusUpdate(17, points, errors.New("status update unavailable"))
	if err != nil {
		t.Fatalf("status bookkeeping failure must not suppress cache finalization: %v", err)
	}
	if len(got) != 1 || got[0].MetricName != points[0].MetricName || got[0].Qty != points[0].Qty {
		t.Fatalf("inserted points were not preserved: got %+v, want %+v", got, points)
	}
}
