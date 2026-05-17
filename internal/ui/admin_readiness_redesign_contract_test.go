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
	"time"

	"health-receiver/internal/ctxdb"
	"health-receiver/internal/storage"
)

func adminContext(db *storage.DB, schema string) context.Context {
	ctx := ctxdb.WithDB(context.Background(), db, schema)
	return ctxdb.WithIsAdmin(ctx, true)
}

// daysAgoUTC returns a YYYY-MM-DD string `n` days before `time.Now()`
// in UTC. The handler helper resolves to UTC when h.mgr is nil
// (which is the case in these tests built with a bare Handler{}),
// so seeding relative to UTC `now` keeps the test independent of
// the calendar date it runs on.
func daysAgoUTC(n int) string {
	return time.Now().UTC().AddDate(0, 0, -n).Format("2006-01-02")
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

	// Seed relative to "now" so the test isn't pinned to a calendar
	// date — the handler's default 14-day window is anchored on
	// time.Now(), and a fixed 2026-05-10 seed would silently drop out
	// of range once enough wall-clock time passes.
	valueDate := daysAgoUTC(3)
	unknownDate := daysAgoUTC(2)
	v := 0.91
	seedRecoveryBaseline(t, db, valueDate, &v, "")
	seedRecoveryBaseline(t, db, unknownDate, nil, storage.BaselineReasonWarmup)

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
		Tenants []string `json:"tenants"`
		Days    int      `json:"days"`
		Rows    []struct {
			Tenant                  string   `json:"tenant"`
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
	if len(resp.Tenants) != 1 || resp.Tenants[0] != schema {
		t.Errorf("tenants = %v, want [%q]", resp.Tenants, schema)
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
		if row.Tenant != schema {
			t.Errorf("row tenant = %q, want %q", row.Tenant, schema)
		}
		if row.SubScore == storage.SubScoreRecoveryStability {
			got[row.Date] = cell{val: row.PredictedValue, reason: row.BaselineReason}
		}
	}
	if c := got[valueDate]; c.val == nil || *c.val < 0.90 || *c.val > 0.92 {
		t.Errorf("value-date predicted_value = %v, want ~0.91", c.val)
	}
	if c := got[valueDate]; c.reason != nil {
		t.Errorf("value-date baseline_reason = %q, want NULL", *c.reason)
	}
	if c := got[unknownDate]; c.val != nil {
		t.Errorf("unknown-date predicted_value = %v, want NULL", *c.val)
	}
	if c := got[unknownDate]; c.reason == nil || *c.reason != storage.BaselineReasonWarmup {
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

	// Same relative-to-`now` shape as the JSON test — the previous
	// 2026-05-10 / 11 literals went stale once enough wall-clock
	// passed for them to fall outside the default 14-day window.
	v := 0.91
	seedRecoveryBaseline(t, db, daysAgoUTC(3), &v, "")
	seedRecoveryBaseline(t, db, daysAgoUTC(2), nil, storage.BaselineReasonWarmup)

	h := &Handler{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet,
		"/fragments/admin-readiness-contract?days=14", nil).
		WithContext(adminContext(db, schema))
	h.fragmentAdminReadinessContract(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	// Pivot table: value rendered as text in a chip cell.
	if !strings.Contains(body, "0.910") {
		t.Errorf("fragment missing value cell '0.910': %s", body)
	}
	// nil-value, reason-set chip renders the word "unknown" in its
	// cell text.
	if !strings.Contains(body, "unknown") {
		t.Errorf("fragment missing 'unknown' marker: %s", body)
	}
	// Baseline reason now lives in the cell's title= attribute as
	// part of "baseline=… · target=… · epoch=…" tooltip — confirm
	// it's the value-side label, not just the cell text.
	if !strings.Contains(body, `title="baseline=`+storage.BaselineReasonWarmup) {
		t.Errorf("fragment missing baseline_warmup title attribute: %s", body)
	}
	// Pivot includes tenant column with the schema name.
	if !strings.Contains(body, schema) {
		t.Errorf("fragment missing tenant column with schema %q: %s", schema, body)
	}
}

// TestAdminReadinessRedesignChipCalibrations_RejectsPOSTSchemaAll proves
// the safety contract: `POST ?schema=all` returns 400 even though the
// admin UI doesn't generate that request. A curl caller posting it
// must not cascade recompute across every registered tenant; the
// only way to recompute all tenants is to do it explicitly per tenant
// (selector switched between calls). GET ?schema=all stays valid for
// the read-only pivot.
func TestAdminReadinessRedesignChipCalibrations_RejectsPOSTSchemaAll(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()

	h := &Handler{}

	// POST ?schema=all must 400 with a message that names the rule.
	postW := httptest.NewRecorder()
	postR := httptest.NewRequest(http.MethodPost,
		"/api/admin/readiness-redesign/chip-calibrations?schema=all", nil).
		WithContext(adminContext(db, schema))
	h.adminReadinessRedesignChipCalibrations(postW, postR)
	if postW.Code != http.StatusBadRequest {
		t.Fatalf("POST schema=all: status = %d, want 400; body=%s", postW.Code, postW.Body.String())
	}
	if !strings.Contains(postW.Body.String(), "schema=all") ||
		!strings.Contains(postW.Body.String(), "per-tenant") {
		t.Errorf("POST schema=all error body should reference the rule, got: %s", postW.Body.String())
	}

	// GET ?schema=all still works — read-only aggregation.
	getW := httptest.NewRecorder()
	getR := httptest.NewRequest(http.MethodGet,
		"/api/admin/readiness-redesign/chip-calibrations?schema=all", nil).
		WithContext(adminContext(db, schema))
	h.adminReadinessRedesignChipCalibrations(getW, getR)
	if getW.Code != http.StatusOK {
		t.Errorf("GET schema=all: status = %d, want 200; body=%s", getW.Code, getW.Body.String())
	}

	// POST without schema (or explicit ?schema=<name>) targets a
	// single tenant — proves the safe path still works.
	okW := httptest.NewRecorder()
	okR := httptest.NewRequest(http.MethodPost,
		"/api/admin/readiness-redesign/chip-calibrations", nil).
		WithContext(adminContext(db, schema))
	h.adminReadinessRedesignChipCalibrations(okW, okR)
	if okW.Code != http.StatusOK {
		t.Errorf("POST without schema: status = %d, want 200; body=%s", okW.Code, okW.Body.String())
	}
}
