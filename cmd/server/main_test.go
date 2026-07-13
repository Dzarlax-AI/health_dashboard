package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"health-receiver/internal/notify"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

func TestBootstrapAdminRequestAPIKeyOnlyDisablesBlankPasswordLogin(t *testing.T) {
	req, generated, err := bootstrapAdminRequest("existing-api-key", "", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if !generated || req.Password == "" {
		t.Fatal("API_KEY-only bootstrap retained a blank UI password")
	}
	if req.InitialAPIKey != "existing-api-key" {
		t.Fatalf("bootstrap API key=%q", req.InitialAPIKey)
	}
	hash, err := registry.HashPassword(req.Password)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := registry.VerifyPassword(hash, ""); ok {
		t.Fatal("blank password authenticated against generated bootstrap credential")
	}
}

func TestReportScheduleAtDeadlineFiresInsteadOfReschedulingTomorrow(t *testing.T) {
	cfg := notify.Config{Timezone: "UTC", MorningWeekdayHour: 8, MorningWeekendHour: 9, EveningWeekdayHour: 20, EveningWeekendHour: 21}
	now := time.Date(2026, time.July, 13, 7, 0, 0, 0, time.UTC)
	next := cfg.NextMorning(now)
	if reportScheduleChanged(next, next, true, cfg, cfg) {
		t.Fatal("reaching the scheduled deadline was mistaken for a configuration change")
	}

	changed := cfg
	changed.MorningWeekdayHour = 9
	if !reportScheduleChanged(now, next, true, cfg, changed) {
		t.Fatal("schedule change was not detected before the deadline")
	}
	if !reportScheduleChanged(next, next, true, cfg, changed) {
		t.Fatal("schedule change during the final wait was ignored at the old deadline")
	}
}

func TestReadinessAllowsPoolsAddedAfterStartup(t *testing.T) {
	mgr := tenants.New(nil, "postgres://db.example/health")
	if err := mgr.SetLegacyMode(&storage.DB{}, "key", "hash"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerOperationalEndpoints(mux, mgr, 0)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil).WithContext(context.Background()))
	if w.Code != http.StatusOK {
		t.Fatalf("ready status=%d body=%q", w.Code, w.Body.String())
	}
}
