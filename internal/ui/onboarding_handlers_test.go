// Handler-level tests for the readiness onboarding wizard.
//
// The wizard's safety contract: every mutating step must reject
// `?schema=all` and the missing-schema case with 400, mirroring the
// chip-calibration endpoint. Read-only steps must also reject these
// (the wizard is a per-tenant flow by design — `all` makes no sense
// even on a Step 1 preview, because the operator is supposed to be
// picking a tenant in the selector before opening the wizard).

package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOnboardingWizard_RejectsSchemaAll(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()

	h := &Handler{}

	type endpointCase struct {
		name   string
		method string
		path   string
	}
	endpoints := []endpointCase{
		// Read-only steps.
		{"step-1 GET", http.MethodGet, "/fragments/admin-readiness-onboarding/step-1"},
		{"step-2 GET", http.MethodGet, "/fragments/admin-readiness-onboarding/step-2"},
		{"step-3 GET", http.MethodGet, "/fragments/admin-readiness-onboarding/step-3"},
		{"step-4-plan GET", http.MethodGet, "/fragments/admin-readiness-onboarding/step-4-plan"},
		{"step-5 GET", http.MethodGet, "/fragments/admin-readiness-onboarding/step-5"},
		// Mutating steps — the load-bearing rejections.
		{"step-4-run POST", http.MethodPost, "/fragments/admin-readiness-onboarding/step-4-run"},
		{"step-6-run POST", http.MethodPost, "/fragments/admin-readiness-onboarding/step-6-run"},
		// Step 7 reuses the operational-contract fragment but the
		// wizard's onboardingScope guard runs first, so the rule
		// applies here too.
		{"step-7 GET", http.MethodGet, "/fragments/admin-readiness-onboarding/step-7"},
	}

	for _, ep := range endpoints {
		t.Run(ep.name+" with schema=all", func(t *testing.T) {
			req := httptest.NewRequest(ep.method, ep.path+"?schema=all", nil).
				WithContext(adminContext(db, schema))
			w := httptest.NewRecorder()
			dispatchOnboardingHandler(h, ep.path, w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "schema=all") {
				t.Errorf("body should mention schema=all; got %q", w.Body.String())
			}
		})
		t.Run(ep.name+" missing schema", func(t *testing.T) {
			req := httptest.NewRequest(ep.method, ep.path, nil).
				WithContext(adminContext(db, schema))
			w := httptest.NewRecorder()
			dispatchOnboardingHandler(h, ep.path, w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
			}
		})
	}
}

// dispatchOnboardingHandler routes the test request to the matching
// handler method. The production code uses mux.HandleFunc so we
// mirror the same dispatch in-test rather than spinning up a full
// mux just to exercise validation guards.
func dispatchOnboardingHandler(h *Handler, path string, w http.ResponseWriter, r *http.Request) {
	switch path {
	case "/fragments/admin-readiness-onboarding/step-1":
		h.fragmentOnboardingStep1(w, r)
	case "/fragments/admin-readiness-onboarding/step-2":
		h.fragmentOnboardingStep2(w, r)
	case "/fragments/admin-readiness-onboarding/step-3":
		h.fragmentOnboardingStep3(w, r)
	case "/fragments/admin-readiness-onboarding/step-4-plan":
		h.fragmentOnboardingStep4Plan(w, r)
	case "/fragments/admin-readiness-onboarding/step-4-run":
		h.fragmentOnboardingStep4Run(w, r)
	case "/fragments/admin-readiness-onboarding/step-5":
		h.fragmentOnboardingStep5(w, r)
	case "/fragments/admin-readiness-onboarding/step-6-run":
		h.fragmentOnboardingStep6Run(w, r)
	case "/fragments/admin-readiness-onboarding/step-7":
		h.fragmentOnboardingStep7(w, r)
	default:
		http.Error(w, "unknown test endpoint "+path, http.StatusNotImplemented)
	}
}

// Sanity-check the happy read-only path: with a valid schema query
// param Step 1 returns 200 and contains the schema name in the body.
// The full coverage of step content lives in the storage tests
// (LoadOnboardingTenantStatus etc.); this one just confirms the
// handler wires through.
func TestOnboardingWizard_Step1_ValidSchemaReturnsBody(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()

	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet,
		"/fragments/admin-readiness-onboarding/step-1?schema="+schema, nil).
		WithContext(adminContext(db, schema))
	w := httptest.NewRecorder()
	h.fragmentOnboardingStep1(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), schema) {
		t.Errorf("body should mention schema %q; got: %s", schema, w.Body.String())
	}
}
