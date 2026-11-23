package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"health-receiver/internal/api"
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

// EnergyPage requests the current briefing and the last 14 saved day values independently.
func TestInstalledProfileEnergy(t *testing.T) {
	phase := os.Getenv("PROFILE_SCREEN_PHASE")
	if phase == "" || os.Getenv("PROFILE_GRAPH_FIXTURE_TOKEN") == "" {
		t.Skip("run the installed Energy screen fixture")
	}
	if phase != "before" && phase != "after" {
		t.Fatalf("unexpected Energy fixture phase %q", phase)
	}
	ctx := t.Context()
	source, graph, dsn := os.Getenv("PROFILE_SOURCE_SCHEMA"), os.Getenv("PROFILE_GRAPH_SCHEMA"), os.Getenv("DATABASE_URL")
	peer := source == "peer_source"
	if !(source == "profile_source" && graph == "profile_graph" || peer && graph == "peer_graph") {
		t.Fatal("unexpected Energy fixture schemas")
	}
	observed, offset := 76, 0
	if peer {
		observed, offset = 84, 5
	}
	for _, mode := range []string{"legacy", "graph", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			var db *storage.DB
			var err error
			if mode == "graph" {
				db, err = storage.NewLocalProfileGraphReader(ctx, dsn, source, graph)
			} else {
				db, err = storage.NewWithSchema(ctx, dsn, source)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mgr := &tenants.Manager{}
			if err := mgr.SetLegacyMode(db, "energy-fixture-key", ""); err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			New(mgr, nil, false).Register(mux)
			get := func(path string, out any) {
				t.Helper()
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Header.Set("X-API-Key", "energy-fixture-key")
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, req)
				if w.Code != http.StatusOK {
					t.Fatalf("%s returned HTTP %d: %s", path, w.Code, w.Body.String())
				}
				if err := json.NewDecoder(w.Body).Decode(out); err != nil {
					t.Fatal(err)
				}
			}
			var briefing health.BriefingResponse
			get("/api/health-briefing?lang=en", &briefing)
			today, err := time.Parse(time.DateOnly, db.Today())
			if err != nil {
				t.Fatal(err)
			}
			b := briefing.EnergyBank
			if briefing.Date != today.Format(time.DateOnly) || briefing.EnergyBankStatus == "stale" || b == nil {
				t.Fatalf("card date=%q status=%q bank=%+v", briefing.Date, briefing.EnergyBankStatus, b)
			}
			expected := "moderate"
			if mode == "graph" {
				expected = "rest"
				if phase == "before" || peer {
					expected = "push_hard"
				}
			}
			if b.Current != observed || b.Capacity != observed+6 || b.DrainSoFar != 6 ||
				b.ActionVerdict != expected || b.VerdictReason == "" || b.VerdictLabel == "" || (briefing.EnergyBankCurrent != nil && *briefing.EnergyBankCurrent != observed) {
				t.Fatalf("observed reserve and current guidance: got %+v, want reserve %d verdict %s", b, observed, expected)
			}
			var history api.EnergyHistoryDayResponse
			get("/api/energy-history?days=14", &history)
			if history.Granularity != "day" || len(history.Points) != 14 {
				t.Fatalf("history: %s, %d rows", history.Granularity, len(history.Points))
			}
			for i, point := range history.Points {
				n := 13 - i
				want := storage.EnergyHistoryPoint{Date: today.AddDate(0, 0, -n).Format(time.DateOnly), Capacity: 80 - n + offset, CurrentEOD: 70 - n + offset, Drain: 10, Verdict: "moderate"}
				if point != want {
					t.Fatalf("history[%d]=%+v, want %+v", i, point, want)
				}
			}
		})
	}
}
