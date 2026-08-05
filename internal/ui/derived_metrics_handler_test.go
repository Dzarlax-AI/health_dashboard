package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"health-receiver/internal/storage"
)

func TestDerivedMetricsReturnsSanitizedWakeValues(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()
	db.EnsureDerivedMetricsTables()

	wake := time.Date(2026, 8, 5, 7, 58, 0, 0, time.UTC)
	if err := db.SaveDerivedMetric(storage.DerivedMetric{
		MetricName:     storage.DerivedMetricWakeTime,
		MetricDate:     "2026-08-05",
		ValueType:      storage.DerivedValueTimestamp,
		ValueTimestamp: &wake,
		Unit:           "timestamp",
		State:          storage.DerivedMetricStateFinal,
		FormulaVersion: storage.WakeFormulaVersion,
		InputsHash:     "private-input-hash",
		CalculatedAt:   wake.Add(time.Hour),
		FinalizedAt:    ptrTime(wake.Add(time.Hour)),
		Metadata:       json.RawMessage(`{"input_source":"Private Watch Name","reason":"post_wake_activity"}`),
	}); err != nil {
		t.Fatalf("seed wake metric: %v", err)
	}

	h := &Handler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/derived-metrics?metric=wake_time&from=2026-08-05&to=2026-08-05", nil).
		WithContext(adminContext(db, schema))
	h.derivedMetrics(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Values []map[string]any `json:"values"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Values) != 1 {
		t.Fatalf("values=%d, want 1", len(response.Values))
	}
	if _, ok := response.Values[0]["inputs_hash"]; ok {
		t.Fatal("API leaked inputs_hash")
	}
	if _, ok := response.Values[0]["metadata"]; ok {
		t.Fatal("API leaked private metadata")
	}
	if response.Values[0]["value_timestamp"] != wake.Format(time.RFC3339) {
		t.Fatalf("value_timestamp=%v, want %s", response.Values[0]["value_timestamp"], wake.Format(time.RFC3339))
	}
}

func TestDerivedMetricsRejectsUnknownMetricAndInvalidRange(t *testing.T) {
	h := &Handler{}
	for _, path := range []string{
		"/api/derived-metrics?metric=made_up&from=2026-08-01&to=2026-08-05",
		"/api/derived-metrics?metric=wake_time&from=2026-08-06&to=2026-08-05",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		h.derivedMetrics(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d, want 400", path, rec.Code)
		}
	}
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
