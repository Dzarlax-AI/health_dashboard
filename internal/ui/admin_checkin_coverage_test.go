package ui

import (
	"bytes"
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

func TestAdminCheckinCoverage_CalendarSLA(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()

	today := time.Now().UTC()
	todayStr := today.Format("2006-01-02")
	yesterdayStr := today.AddDate(0, 0, -1).Format("2006-01-02")
	if err := db.SaveCheckinEnabledSince(yesterdayStr); err != nil {
		t.Fatalf("save enabled since: %v", err)
	}
	promptedAt := time.Date(today.Year(), today.Month(), today.Day(), 8, 0, 0, 0, time.UTC)
	if err := db.SaveCheckinPrompted(todayStr, storage.CheckinSourceTelegram, 42, promptedAt, promptedAt.Add(time.Hour)); err != nil {
		t.Fatalf("seed prompt: %v", err)
	}
	if _, err := db.SaveCheckinAnswer(todayStr, storage.CheckinSourceTelegram, storage.CheckinAnswerOK, promptedAt.Add(time.Minute)); err != nil {
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
	if !resp.SLAActive || resp.EnabledSince != yesterdayStr {
		t.Fatalf("sla active=%v enabled_since=%q, want active %s", resp.SLAActive, resp.EnabledSince, yesterdayStr)
	}
	if len(resp.SLARows) != 2 {
		t.Fatalf("sla rows = %d, want 2: %+v", len(resp.SLARows), resp.SLARows)
	}
	if resp.SLARows[0].Date != todayStr || resp.SLARows[0].Status != storage.CheckinStatusAnswered {
		t.Fatalf("sla row0 = %+v, want today answered", resp.SLARows[0])
	}
	if resp.SLARows[1].Date != yesterdayStr || resp.SLARows[1].Status != storage.CheckinStatusMissing {
		t.Fatalf("sla row1 = %+v, want yesterday missing", resp.SLARows[1])
	}
	if resp.SLASummary.Missing != 1 || resp.SLASummary.Answered != 1 {
		t.Fatalf("sla summary = %+v, want missing=1 answered=1", resp.SLASummary)
	}
}

func TestAdminCheckinCoverage_PostSavesEnabledSince(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()

	h := &Handler{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/admin/checkin-coverage?days=3", bytes.NewBufferString(`{"enabled_since":"2026-06-15"}`)).
		WithContext(adminContext(db, schema))
	h.adminCheckinCoverage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := db.GetCheckinEnabledSince(); got != "2026-06-15" {
		t.Fatalf("enabled_since = %q, want 2026-06-15", got)
	}
	var resp storage.CheckinCoverage
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if !resp.SLAActive {
		t.Fatalf("expected SLA active after POST: %+v", resp)
	}
}

func TestAdminCheckinCoverage_PostRejectsBadEnabledSince(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()

	h := &Handler{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/admin/checkin-coverage", bytes.NewBufferString(`{"enabled_since":"15/06/2026"}`)).
		WithContext(adminContext(db, schema))
	h.adminCheckinCoverage(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
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
