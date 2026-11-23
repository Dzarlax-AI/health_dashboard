package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"health-receiver/internal/api"
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
	get := func(key, path string, result any) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("X-API-Key", key)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s HTTP status %d", path, response.Code)
		}
		if err := json.Unmarshal(response.Body.Bytes(), result); err != nil {
			t.Fatal(err)
		}
	}
	fetch := func(key string) *health.SleepDurationComparison {
		var result health.BriefingResponse
		get(key, "/api/health-briefing", &result)
		return result.SleepDurationComparison
	}
	dated := func(day string) *health.SleepDurationComparison {
		var result api.SleepDurationComparisonResponse
		get("rollout_a-key", "/api/sleep/comparison?date="+day, &result)
		return result.Comparison
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
	if got := dated(first.WakeDate); !reflect.DeepEqual(got, first) {
		t.Fatalf("dated Sleep and Today disagree: Sleep=%+v Today=%+v", got, first)
	}
	yesterday := date.AddDate(0, 0, -1).Format(time.DateOnly)
	if got := dated(yesterday); got == nil || got.WakeDate != yesterday || got.Status != health.SleepComparisonInsufficient ||
		got.EligibleNights == nil || *got.EligibleNights != 6 || got.ReferenceMinutes != nil || got.DeltaMinutes != nil {
		t.Fatalf("dated Sleep must exclude the selected date and later nights: %+v", got)
	}
	saveNight(7, 8.75, "corrected")
	next := fetch("rollout_a-key")
	if next == nil || next.DeltaMinutes == nil || *next.DeltaMinutes != -75 || first.FactVersion == next.FactVersion {
		t.Fatalf("comparison after historical correction: %+v", next)
	}
	if got := dated(first.WakeDate); !reflect.DeepEqual(got, next) {
		t.Fatalf("dated Sleep and Today disagree after correction: Sleep=%+v Today=%+v", got, next)
	}
	if peer := fetch("rollout_b-key"); peer != nil {
		t.Fatalf("another tenant saw a comparison: %+v", peer)
	}
}
