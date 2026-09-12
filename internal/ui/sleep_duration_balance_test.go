package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	clientapi "health-receiver/internal/api"
)

// These are real-schema tests for the two user-facing sleep-balance routes.
// They deliberately cover the first useful state after a user selects a Goal:
// the Goal is durable, while an absent coverage commitment is communicated as
// an incomplete result with a null balance rather than a fabricated zero.
func TestSleepGoalAndBalance_ManualGoalPersistsWithHonestIncompleteBalance(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()
	if err := db.EnsureSleepDurationBalanceTablesContext(t.Context()); err != nil {
		t.Fatalf("ensure sleep duration balance tables: %v", err)
	}

	h := &Handler{}
	const effectiveDate = "2026-09-01"

	put := httptest.NewRecorder()
	h.sleepGoal(put, requestWithTenant(http.MethodPut,
		`{"effective_date":"2026-09-01","goal_hours":7.5}`, db, schema))
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body=%s", put.Code, put.Body.String())
	}
	var saved clientapi.SleepGoalResponse
	if err := json.Unmarshal(put.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode PUT response: %v; body=%s", err, put.Body.String())
	}
	if saved.Goal == nil || saved.Goal.EffectiveDate != effectiveDate || saved.Goal.GoalHours != 7.5 || saved.Goal.Version != "manual-goal-v1" {
		t.Fatalf("saved goal = %#v, want manual 7.5h goal effective %s", saved.Goal, effectiveDate)
	}

	getGoalRequest := requestWithTenant(http.MethodGet, "", db, schema)
	getGoalRequest.URL.RawQuery = "date=2026-09-10"
	getGoal := httptest.NewRecorder()
	h.sleepGoal(getGoal, getGoalRequest)
	if getGoal.Code != http.StatusOK {
		t.Fatalf("GET goal status = %d, want 200; body=%s", getGoal.Code, getGoal.Body.String())
	}
	var fetched clientapi.SleepGoalResponse
	if err := json.Unmarshal(getGoal.Body.Bytes(), &fetched); err != nil {
		t.Fatalf("decode GET goal response: %v; body=%s", err, getGoal.Body.String())
	}
	if fetched.Goal == nil || fetched.Goal.EffectiveDate != effectiveDate || fetched.Goal.GoalHours != 7.5 {
		t.Fatalf("fetched goal = %#v, want effective manual goal", fetched.Goal)
	}

	balanceRequest := requestWithTenant(http.MethodGet, "", db, schema)
	balanceRequest.URL.RawQuery = "date=2026-09-10"
	balance := httptest.NewRecorder()
	h.sleepDurationBalance(balance, balanceRequest)
	if balance.Code != http.StatusOK {
		t.Fatalf("GET balance status = %d, want 200; body=%s", balance.Code, balance.Body.String())
	}
	var response clientapi.SleepDurationBalanceResponse
	if err := json.Unmarshal(balance.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode balance response: %v; body=%s", err, balance.Body.String())
	}
	if response.State != "incomplete" || response.BalanceHours != nil {
		t.Fatalf("balance state = %q, balance_hours = %#v; want incomplete with null balance", response.State, response.BalanceHours)
	}
}

func TestSleepGoal_RejectsOutOfRangeGoal(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()
	if err := db.EnsureSleepDurationBalanceTablesContext(t.Context()); err != nil {
		t.Fatalf("ensure sleep duration balance tables: %v", err)
	}

	w := httptest.NewRecorder()
	(&Handler{}).sleepGoal(w, requestWithTenant(http.MethodPut,
		`{"effective_date":"2026-09-01","goal_hours":2}`, db, schema))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT invalid goal status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}
