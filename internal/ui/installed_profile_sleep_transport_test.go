package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"health-receiver/internal/api"
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

func TestInstalledProfileSleepTransport(t *testing.T) {
	if os.Getenv("PROFILE_GRAPH_FIXTURE_TOKEN") == "" {
		t.Skip("run the installed profile graph probe")
	}
	ctx := t.Context()
	dsn, source := os.Getenv("DATABASE_URL"), os.Getenv("PROFILE_SOURCE_SCHEMA")
	legacy, err := storage.NewWithSchema(ctx, dsn, source)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	db, err := storage.NewLocalProfileGraphReader(ctx, dsn, source, os.Getenv("PROFILE_GRAPH_SCHEMA"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := &tenants.Manager{}
	if err := mgr.SetLegacyMode(db, "fixture-key", ""); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(mgr, nil, false).Register(mux)
	get := func(path string, output any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-API-Key", "fixture-key")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), output) != nil {
			t.Fatalf("%s: HTTP %d: %s", path, w.Code, w.Body.String())
		}
	}
	var history api.SleepDurationComparisonResponse
	get("/api/sleep/comparison?date=1981-03-15", &history)
	if f := history.Comparison; f == nil || f.DeltaMinutes == nil || *f.DeltaMinutes != -42 ||
		!reflect.DeepEqual(f, legacy.GetSleepDurationComparison(ctx, "1981-03-15")) {
		t.Fatalf("history HTTP comparison: %+v", f)
	}
	var briefing health.BriefingResponse
	get("/api/health-briefing", &briefing)
	if f := briefing.SleepDurationComparison; f == nil || f.DurationMinutes == nil ||
		!reflect.DeepEqual(f, legacy.GetSleepDurationComparison(ctx, db.Today())) {
		t.Fatalf("Today HTTP comparison: %+v", f)
	}
}
