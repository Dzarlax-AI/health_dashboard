package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	clientapi "health-receiver/internal/api"
	"health-receiver/internal/ctxdb"
	"health-receiver/internal/metriccontract"
	"health-receiver/internal/storage"
)

// Run on an isolated archive with its audited daily_scores reference. Emit
// only row counts and elapsed time so fleet verification can retain its output.
func TestArchiveSharedQuantitySteps(t *testing.T) {
	schema := os.Getenv("QUANTITY_ARCHIVE_SCHEMA")
	if schema == "" {
		t.Skip("requires an isolated profile archive")
	}
	db, err := storage.NewWithSchema(t.Context(), os.Getenv("DATABASE_URL"), schema)
	if err != nil {
		t.Fatal("archive connection failed")
	}
	defer db.Close()

	rows, err := db.QueryReadOnly("select date,steps::float8 as qty from daily_scores where steps is not null order by date")
	if err != nil {
		t.Fatal("could not read archive reference")
	}
	want := make([]storage.MetricQuantityPoint, 0, len(rows))
	for _, row := range rows {
		want = append(want, storage.MetricQuantityPoint{Date: row["date"].(string), Qty: row["qty"].(float64)})
	}
	end := db.Today()
	if len(want) > 0 {
		// Exercise a populated month even on a historical or inactive profile.
		end = want[len(want)-1].Date
	}
	endTime, err := time.Parse(time.DateOnly, end)
	if err != nil {
		t.Fatal("invalid archive end date")
	}
	start := endTime.AddDate(0, 0, -29).Format(time.DateOnly)
	index, _ := slices.BinarySearchFunc(want, start, func(p storage.MetricQuantityPoint, day string) int {
		return strings.Compare(p.Date, day)
	})
	handler := &Handler{}
	for _, tc := range []struct {
		name, query string
		want        []storage.MetricQuantityPoint
	}{
		{"all", "range=all", want},
		{"month", "from=" + start, want[index:]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/metrics/data?metric=step_count&bucket=day&view=quantity&to="+end+"&"+tc.query, nil)
			r.Header.Set(metriccontract.Header, "2")
			r = r.WithContext(ctxdb.WithDB(r.Context(), db, schema))
			w := httptest.NewRecorder()
			started := time.Now()
			handler.metricData(w, r)
			elapsed := time.Since(started)
			var got clientapi.MetricQuantityResponse
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil ||
				got.Metric != "step_count" || got.Bucket != "day" || got.Agg != "SUM" || got.Points == nil {
				t.Fatalf("invalid quantity response, status %d", w.Code)
			}
			if !slices.Equal(got.Points, tc.want) {
				t.Fatalf("archive mismatch: returned %d rows, reference %d", len(got.Points), len(tc.want))
			}
			t.Logf("matched_rows=%d http_duration=%v", len(got.Points), elapsed)
		})
	}
}
