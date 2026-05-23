package ui

import (
	"net/http/httptest"
	"testing"

	"health-receiver/internal/ctxdb"
	"health-receiver/internal/storage"
)

func TestResolveAdminTenantScope_DefaultsToRequestTenant(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest("GET", "/api/admin/gaps", nil)
	req = req.WithContext(ctxdb.WithDB(req.Context(), &storage.DB{}, "health"))

	scope, err := h.resolveAdminTenantScope(req)
	if err != nil {
		t.Fatalf("resolveAdminTenantScope returned error: %v", err)
	}
	if scope.Schema != "health" {
		t.Fatalf("schema = %q, want health", scope.Schema)
	}
	if scope.DB == nil {
		t.Fatalf("DB is nil")
	}
}

func TestResolveAdminTenantScope_RejectsSchemaOverrideWithoutRegistry(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest("GET", "/api/admin/gaps?schema=health_mariia", nil)
	req = req.WithContext(ctxdb.WithDB(req.Context(), &storage.DB{}, "health"))
	req = req.WithContext(ctxdb.WithIsAdmin(req.Context(), true))

	_, err := h.resolveAdminTenantScope(req)
	if err == nil {
		t.Fatalf("expected error")
	}
	if err.status != 503 {
		t.Fatalf("status = %d, want 503", err.status)
	}
}

func TestResolveAdminTenantScope_RejectsSchemaOverrideForNonAdmin(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest("GET", "/api/settings/energy-backfill?schema=health_mariia", nil)
	req = req.WithContext(ctxdb.WithDB(req.Context(), &storage.DB{}, "health"))

	_, err := h.resolveAdminTenantScope(req)
	if err == nil {
		t.Fatalf("expected error")
	}
	if err.status != 403 {
		t.Fatalf("status = %d, want 403", err.status)
	}
}

func TestAdminGaps_RejectsSchemaOverrideWithoutRegistry(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest("GET", "/api/admin/gaps?schema=missing", nil)
	req = req.WithContext(ctxdb.WithDB(req.Context(), &storage.DB{}, "health"))
	req = req.WithContext(ctxdb.WithIsAdmin(req.Context(), true))
	w := httptest.NewRecorder()

	h.adminGaps(w, req)

	if w.Code != 503 {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}
