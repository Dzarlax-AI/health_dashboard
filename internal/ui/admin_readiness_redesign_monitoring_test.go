package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"health-receiver/internal/storage"
)

func TestAdminReadinessRedesignMonitoring_JSONShape(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()

	date := daysAgoUTC(1)
	v := 0.5
	if err := db.SaveTargetSnapshot(storage.TargetSnapshot{
		Date:              date,
		SubScore:          storage.SubScoreRecoveryStability,
		TargetKind:        storage.TargetKindRolling3d,
		TargetValue:       &v,
		Eligible:          false,
		EligibilityReason: storage.EligibilitySleepDataMissing,
		SourceEpoch:       storage.InitialSourceEpoch,
		FormulaVersion:    1,
	}); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	h := &Handler{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet,
		"/api/admin/readiness-redesign/monitoring", nil).
		WithContext(adminContext(db, schema))
	h.adminReadinessRedesignMonitoring(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Tenants []string `json:"tenants"`
		Rows    []struct {
			Tenant        string `json:"tenant"`
			OverallStatus string `json:"OverallStatus"`
			CoverageRows  []struct {
				SubScore   string `json:"SubScore"`
				TargetKind string `json:"TargetKind"`
				Status     string `json:"Status"`
			} `json:"CoverageRows"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if len(resp.Tenants) != 1 || resp.Tenants[0] != schema {
		t.Fatalf("tenants = %v, want [%q]", resp.Tenants, schema)
	}
	if len(resp.Rows) != 1 || resp.Rows[0].Tenant != schema {
		t.Fatalf("rows = %+v, want one row for %q", resp.Rows, schema)
	}
	if len(resp.Rows[0].CoverageRows) == 0 {
		t.Fatalf("CoverageRows empty")
	}
}

func TestFragmentAdminReadinessMonitoring_Renders(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()

	date := daysAgoUTC(1)
	v := 0.5
	if err := db.SaveTargetSnapshot(storage.TargetSnapshot{
		Date:              date,
		SubScore:          storage.SubScoreRecoveryStability,
		TargetKind:        storage.TargetKindRolling3d,
		TargetValue:       &v,
		Eligible:          true,
		EligibilityReason: storage.EligibilityOK,
		SourceEpoch:       storage.InitialSourceEpoch,
		FormulaVersion:    1,
	}); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	h := &Handler{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet,
		"/fragments/admin-readiness-monitoring", nil).
		WithContext(adminContext(db, schema))
	h.fragmentAdminReadinessMonitoring(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{schema, "coverage", storage.SubScoreRecoveryStability} {
		if !strings.Contains(body, want) {
			t.Fatalf("fragment missing %q; body=%s", want, body)
		}
	}
}
