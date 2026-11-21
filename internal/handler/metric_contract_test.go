package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"health-receiver/internal/metriccontract"
)

func TestMetricContractProbeAndInvalidWrite(t *testing.T) {
	h := &Handler{}
	for _, contract := range []string{"", "2"} {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		expected := "1"
		if contract != "" {
			req.Header.Set(metriccontract.Header, contract)
			expected = contract
		}
		w := httptest.NewRecorder()
		h.health(w, req)
		if w.Code != http.StatusOK || w.Header().Get(metriccontract.Header) != expected {
			t.Fatalf("probe contract=%q response=%d headers=%v", contract, w.Code, w.Header())
		}
	}

	for _, invalid := range []string{
		`{"data":{"metrics":[{"name":"height","units":"m","data":[]}]}}`,
		`{"data":{"metrics":[{"name":"height","units":"cm","grain":"sample_start","data":[]}]}}`,
		`{"data":{"metrics":[{"name":"distance_swimming","units":"km","grain":"sample_start","data":[]}]}}`,
		`{"data":{"metrics":[{"name":"six_min_walk_distance","units":"m","grain":"sample_start","data":[]}]}}`,
		`{"data":{"metrics":[{"name":"__healthkit_archive_sample:distance_swimming","units":"km","data":[]}]}}`,
		`{"data":{"metrics":[{"name":"distance_swimming","units":"km","grain":"hour_sum","data":[{"date":"2026-11-01 00:15:00 +0100","qty":0.25}]}]}}`,
		`{"data":{"metrics":[{"name":"distance_swimming","units":"km","grain":"hour_sum","data":[{"date":"2026-11-01 00:00:00.5 +0100","qty":0.25}]}]}}`,
		`{"data":{"metrics":[{"name":"height","units":"m","grain":"sample_start","data":[{"date":"2026-11-01  1:00:00 +0100","qty":1.8}]}]}}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/health", strings.NewReader(invalid))
		req.Header.Set(metriccontract.Header, "2")
		w := httptest.NewRecorder()
		h.health(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid contract 2 write returned %d", w.Code)
		}
	}
	valid := []byte(`{"data":{"metrics":[{"name":"distance_swimming","units":"km","grain":"hour_sum","data":[{"date":"2026-11-01 00:00:00 +0100","source":"Watch","qty":0.25}]}]}}`)
	if err := validateCurrentMetricPayload(valid); err != nil {
		t.Fatal(err)
	}
	points, err := parseMetricPoints(valid)
	if err != nil || len(points) != 1 || points[0].Qty != .25 {
		t.Fatalf("v2 envelope is unreadable by the established processor: %+v, %v", points, err)
	}
}

func TestMetricWireDoesNotAcceptInternalOriginMarkers(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/health", nil)
	request.Header.Set("Content-Type", "application/json; metrics-contract=2")
	if got := metricContentType(request, metriccontract.Legacy); got != "application/json" {
		t.Fatalf("legacy media type = %q", got)
	}
	request.Header.Set("Content-Type", "apple-health-import; catalog=1")
	if got := metricContentType(request, metriccontract.Legacy); got != "" {
		t.Fatalf("legacy claimed archive provenance = %q", got)
	}
	if got := extractPoints("__healthkit_archive_sample:distance_swimming", "km", []byte(`{"date":"2026-11-01 00:00:00 +0100","qty":1}`)); len(got) != 0 {
		t.Fatalf("reserved metric was accepted: %+v", got)
	}
}
