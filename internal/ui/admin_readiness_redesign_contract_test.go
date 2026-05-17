// Handler-level tests for /api/admin/readiness-redesign/operational-contract
// and the matching /fragments/admin-readiness-contract HTML view.
//
// Storage-side tests in internal/storage prove the join semantics on a
// real schema across all three chip states (value / unknown / pending).
// These tests cover the handler-side contract: input validation,
// response shape, value/unknown rendering, and `days` clamping.

package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"health-receiver/internal/ctxdb"
	"health-receiver/internal/storage"
)

func adminContext(db *storage.DB, schema string) context.Context {
	ctx := ctxdb.WithDB(context.Background(), db, schema)
	return ctxdb.WithIsAdmin(ctx, true)
}

func seedRecoveryBaseline(t *testing.T, db *storage.DB, date string, value *float64, reason string) {
	t.Helper()
	nb := storage.NaiveBaseline{
		Date:           date,
		SubScore:       storage.SubScoreRecoveryStability,
		TargetKind:     storage.TargetKindRolling3d,
		BaselineKind:   storage.BaselineKindEWMA45d,
		PredictedValue: value,
		Reason:         reason,
		SourceEpoch:    storage.InitialSourceEpoch,
		FormulaVersion: 1,
	}
	if err := db.SaveNaiveBaseline(nb); err != nil {
		t.Fatalf("seed baseline %s: %v", date, err)
	}
}

func TestAdminReadinessRedesignOperationalContract_JSONShape(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()

	v := 0.91
	seedRecoveryBaseline(t, db, "2026-05-10", &v, "")
	seedRecoveryBaseline(t, db, "2026-05-11", nil, storage.BaselineReasonWarmup)

	h := &Handler{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet,
		"/api/admin/readiness-redesign/operational-contract?days=14", nil).
		WithContext(adminContext(db, schema))
	h.adminReadinessRedesignOperationalContract(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Schema string `json:"schema"`
		Days   int    `json:"days"`
		Rows   []struct {
			Date                    string   `json:"Date"`
			SubScore                string   `json:"SubScore"`
			PredictedValue          *float64 `json:"PredictedValue"`
			BaselineReason          *string  `json:"BaselineReason"`
			TargetEligibilityReason *string  `json:"TargetEligibilityReason"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if resp.Schema != schema {
		t.Errorf("schema = %q, want %q", resp.Schema, schema)
	}
	if resp.Days != 14 {
		t.Errorf("days = %d, want 14", resp.Days)
	}

	type cell struct {
		val    *float64
		reason *string
	}
	got := map[string]cell{}
	for _, row := range resp.Rows {
		if row.SubScore == storage.SubScoreRecoveryStability {
			got[row.Date] = cell{val: row.PredictedValue, reason: row.BaselineReason}
		}
	}
	if c := got["2026-05-10"]; c.val == nil || *c.val < 0.90 || *c.val > 0.92 {
		t.Errorf("value-date predicted_value = %v, want ~0.91", c.val)
	}
	if c := got["2026-05-10"]; c.reason != nil {
		t.Errorf("value-date baseline_reason = %q, want NULL", *c.reason)
	}
	if c := got["2026-05-11"]; c.val != nil {
		t.Errorf("unknown-date predicted_value = %v, want NULL", *c.val)
	}
	if c := got["2026-05-11"]; c.reason == nil || *c.reason != storage.BaselineReasonWarmup {
		t.Errorf("unknown-date baseline_reason = %v, want %q", c.reason, storage.BaselineReasonWarmup)
	}
}

func TestAdminReadinessRedesignOperationalContract_RejectsBadDays(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()

	h := &Handler{}
	cases := []struct {
		name string
		days string
	}{
		{"non-numeric", "abc"},
		{"zero", "0"},
		{"negative", "-5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet,
				"/api/admin/readiness-redesign/operational-contract?days="+tc.days, nil).
				WithContext(adminContext(db, schema))
			h.adminReadinessRedesignOperationalContract(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("days=%q: status = %d, want 400; body=%s", tc.days, w.Code, w.Body.String())
			}
		})
	}
}

func TestAdminReadinessRedesignOperationalContract_CapsDaysAt90(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()

	h := &Handler{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet,
		"/api/admin/readiness-redesign/operational-contract?days=365", nil).
		WithContext(adminContext(db, schema))
	h.adminReadinessRedesignOperationalContract(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Days int `json:"days"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Days != 90 {
		t.Errorf("days = %d, want 90 (capped)", resp.Days)
	}
}

// TestParseOperationalContractDays exercises the input-validation
// rules shared by the JSON and fragment surfaces. Pure unit test,
// no DB.
func TestParseOperationalContractDays(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantN   int
		wantErr bool
	}{
		{"empty defaults to 14", "", 14, false},
		{"valid value", "7", 7, false},
		{"caps at 90", "365", 90, false},
		{"non-numeric", "abc", 0, true},
		{"zero", "0", 0, true},
		{"negative", "-5", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, err := parseOperationalContractDays(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if !tc.wantErr && n != tc.wantN {
				t.Errorf("n = %d, want %d", n, tc.wantN)
			}
		})
	}
}

func TestFragmentAdminReadinessContract_RejectsBadDays(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()

	h := &Handler{}
	cases := []string{"abc", "0", "-5"}
	for _, days := range cases {
		t.Run("days="+days, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet,
				"/fragments/admin-readiness-contract?days="+days, nil).
				WithContext(adminContext(db, schema))
			h.fragmentAdminReadinessContract(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("days=%q: status = %d, want 400; body=%s", days, w.Code, w.Body.String())
			}
		})
	}
}

func TestFragmentAdminReadinessContract_RendersValueAndUnknown(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()

	v := 0.91
	seedRecoveryBaseline(t, db, "2026-05-10", &v, "")
	seedRecoveryBaseline(t, db, "2026-05-11", nil, storage.BaselineReasonWarmup)

	h := &Handler{}
	w := httptest.NewRecorder()
	// `days=90` pads the window enough to cover both seeded dates
	// regardless of when the test runs.
	r := httptest.NewRequest(http.MethodGet,
		"/fragments/admin-readiness-contract?days=90", nil).
		WithContext(adminContext(db, schema))
	h.fragmentAdminReadinessContract(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "0.910") {
		t.Errorf("fragment missing value cell '0.910': %s", body)
	}
	if !strings.Contains(body, "unknown") {
		t.Errorf("fragment missing 'unknown' marker: %s", body)
	}
	if !strings.Contains(body, storage.BaselineReasonWarmup) {
		t.Errorf("fragment missing baseline_warmup label: %s", body)
	}
}
