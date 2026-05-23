package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"health-receiver/internal/storage"
)

func TestAdminCheckinCoverage_JSONShape(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()

	today := time.Now().UTC()
	todayStr := today.Format("2006-01-02")
	promptedAt := time.Date(today.Year(), today.Month(), today.Day(), 8, 0, 0, 0, time.UTC)
	expiresAt := promptedAt.Add(time.Hour)
	if err := db.SaveCheckinPrompted(todayStr, storage.CheckinSourceTelegram, 42, promptedAt, expiresAt); err != nil {
		t.Fatalf("seed prompt: %v", err)
	}
	if _, err := db.SaveCheckinAnswer(todayStr, storage.CheckinSourceTelegram, storage.CheckinAnswerGreat, promptedAt.Add(30*time.Second)); err != nil {
		t.Fatalf("seed answer: %v", err)
	}

	h := &Handler{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/admin/checkin-coverage?days=3", nil).
		WithContext(adminContext(db, schema))
	h.adminCheckinCoverage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp storage.CheckinCoverage
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if resp.Days != 3 || len(resp.Rows) != 1 {
		t.Fatalf("days=%d rows=%d, want requested=3 rows=1", resp.Days, len(resp.Rows))
	}
	if resp.Rows[0].Date != todayStr || resp.Rows[0].Status != storage.CheckinStatusAnswered {
		t.Fatalf("first row = %+v, want today answered", resp.Rows[0])
	}
	if resp.Summary.Answered != 1 || resp.Summary.Missing != 0 || resp.Summary.TotalDays != 1 {
		t.Fatalf("summary = %+v, want answered=1 missing=0 total=1", resp.Summary)
	}
	if resp.Summary.AverageResponseSeconds == nil || *resp.Summary.AverageResponseSeconds != 30 {
		t.Fatalf("avg latency = %v, want 30", resp.Summary.AverageResponseSeconds)
	}
}

func TestAdminCheckinCoverage_RejectsBadDays(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/admin/checkin-coverage?days=0", nil)
	h := &Handler{}

	h.adminCheckinCoverage(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}
