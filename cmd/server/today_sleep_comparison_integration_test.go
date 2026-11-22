package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"health-receiver/internal/health"
	"health-receiver/internal/ui"
)

func TestTodaySleepComparisonBriefing(t *testing.T) {
	mgr, _ := newSleepRolloutFixture(t)
	ctx := t.Context()
	db, _, _, ok := mgr.DBForAPIKey(ctx, "rollout_a-key")
	if !ok {
		t.Fatal("missing test profile")
	}
	mux := http.NewServeMux()
	ui.New(mgr, nil, false).Register(mux)
	fetch := func(key string) *health.SleepDurationComparison {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/api/health-briefing", nil)
		request.Header.Set("X-API-Key", key)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		var result health.BriefingResponse
		if response.Code != http.StatusOK {
			t.Fatalf("briefing HTTP status %d", response.Code)
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.SleepDurationComparison
	}
	if got := fetch("rollout_a-key"); got != nil {
		t.Fatalf("comparison enabled by default: %v", got)
	}
	if err := db.SaveSettings(map[string]string{"sleep_duration_comparison_enabled": "true", "timezone": "Pacific/Kiritimati"}); err != nil {
		t.Fatal(err)
	}
	date, err := time.Parse(time.DateOnly, db.Today())
	if err != nil {
		t.Fatal(err)
	}
	saveNight := func(daysAgo int, hours float64, hash string) {
		t.Helper()
		day := date.AddDate(0, 0, -daysAgo)
		err := db.SaveCompletedNightSleep(ctx, health.CompletedNightSleep{
			WakeDate: day.Format(time.DateOnly), DurationHours: hours, Source: "Synthetic Watch",
			SourceEpoch: "sample", AlgorithmVersion: "v1", InputHash: hash,
			CaptureCompleteness: health.NightCaptureComplete, DurationAssessment: health.NightDurationPlausible,
			FinalizationState: health.NightFinalFinal, ClaimEligibility: health.NightClaimEligible, ObservedAt: day,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	for daysAgo := 1; daysAgo <= 7; daysAgo++ {
		saveNight(daysAgo, 7, "original")
	}
	saveNight(0, 6, "current")
	first := fetch("rollout_a-key")
	if first == nil || first.WakeDate != date.Format(time.DateOnly) || first.Status != health.SleepComparisonAvailable ||
		first.DeltaMinutes == nil || *first.DeltaMinutes != -60 || first.FactVersion == "" {
		t.Fatalf("original comparison: %+v", first)
	}
	saveNight(7, 8.75, "corrected")
	next := fetch("rollout_a-key")
	if next == nil || next.DeltaMinutes == nil || *next.DeltaMinutes != -75 || first.FactVersion == next.FactVersion {
		t.Fatalf("comparison after historical correction: %+v", next)
	}
	if peer := fetch("rollout_b-key"); peer != nil {
		t.Fatalf("another tenant saw a comparison: %+v", peer)
	}
}
