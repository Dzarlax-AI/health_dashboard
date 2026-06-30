// Handler-level tests for the readiness onboarding wizard.
//
// Safety contract:
//   - Every endpoint rejects `?schema=all` with 400 (mutating and
//     read-only). The wizard is per-tenant by design.
//   - Missing schema is **accepted** and falls back to the request's
//     own tenant — same shape as adminBackfill's default. This lets
//     the wizard stay usable in legacy single-tenant mode and when
//     a multi-tenant admin profile tab has not supplied a schema.

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
		name                 string
		method               string
		path                 string
		checkMissingFallback bool
	}
	endpoints := []endpointCase{
		// Read-only steps.
		{"step-1 GET", http.MethodGet, "/fragments/admin-readiness-onboarding/step-1", true},
		{"step-2 GET", http.MethodGet, "/fragments/admin-readiness-onboarding/step-2", true},
		{"step-3 GET", http.MethodGet, "/fragments/admin-readiness-onboarding/step-3", true},
		{"step-4-plan GET", http.MethodGet, "/fragments/admin-readiness-onboarding/step-4-plan", true},
		{"step-5 GET", http.MethodGet, "/fragments/admin-readiness-onboarding/step-5", true},
		// Mutating steps — the load-bearing rejections.
		{"step-4-run POST", http.MethodPost, "/fragments/admin-readiness-onboarding/step-4-run", false},
		{"step-6-run POST", http.MethodPost, "/fragments/admin-readiness-onboarding/step-6-run", true},
		// Step 7 reuses the operational-contract fragment but the
		// wizard's onboardingScope guard runs first, so the rule
		// applies here too.
		{"step-7 GET", http.MethodGet, "/fragments/admin-readiness-onboarding/step-7", true},
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
		if ep.checkMissingFallback {
			t.Run(ep.name+" missing schema falls back to ctx", func(t *testing.T) {
				req := httptest.NewRequest(ep.method, ep.path, nil).
					WithContext(adminContext(db, schema))
				w := httptest.NewRecorder()
				dispatchOnboardingHandler(h, ep.path, w, req)
				if w.Code == http.StatusBadRequest {
					t.Fatalf("missing schema should fall back to request tenant, got 400: %s", w.Body.String())
				}
			})
		}
	}

	t.Run("scope missing schema falls back to ctx for heavy run endpoints", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/fragments/admin-readiness-onboarding/step-4-run", nil).
			WithContext(adminContext(db, schema))
		w := httptest.NewRecorder()
		scope := h.onboardingScope(w, req)
		if w.Code == http.StatusBadRequest {
			t.Fatalf("missing schema rejected as bad request; body=%s", w.Body.String())
		}
		if scope == nil {
			t.Fatalf("expected fallback scope, got nil with status=%d body=%s", w.Code, w.Body.String())
		}
		if scope.schema != schema {
			t.Fatalf("scope.schema = %q, want %q", scope.schema, schema)
		}
	})
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
