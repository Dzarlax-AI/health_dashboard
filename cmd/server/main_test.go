package main

import (
	"context"
	"errors"
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

func TestRetryEveningSnapshotUnavailableUntilComplete(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, time.September, 10, 20, 0, 0, 0, loc)
	attempts := 0
	pauses := 0
	err := retryEveningSnapshotUnavailable(
		context.Background(),
		now,
		loc,
		func() time.Time { return now },
		func(context.Context, time.Duration) error {
			pauses++
			return nil
		},
		func() error {
			attempts++
			if attempts == 1 {
				return notify.ErrDashboardSnapshotUnavailable
			}
			return nil
		},
	)
	if err != nil || attempts != 2 || pauses != 1 {
		t.Fatalf("retry result = err:%v attempts:%d pauses:%d, want success after one retry", err, attempts, pauses)
	}
}

func TestRetryEveningSnapshotUnavailableStopsAtDayRollover(t *testing.T) {
	loc := time.UTC
	scheduled := time.Date(2026, time.September, 10, 23, 59, 0, 0, loc)
	now := scheduled
	attempts := 0
	err := retryEveningSnapshotUnavailable(
		context.Background(),
		scheduled,
		loc,
		func() time.Time { return now },
		func(context.Context, time.Duration) error {
			now = now.Add(time.Minute)
			return nil
		},
		func() error {
			attempts++
			return notify.ErrDashboardSnapshotUnavailable
		},
	)
	if !errors.Is(err, notify.ErrDashboardSnapshotUnavailable) || attempts != 1 {
		t.Fatalf("rollover retry = err:%v attempts:%d, want one unavailable attempt", err, attempts)
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
