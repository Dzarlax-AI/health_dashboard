package ui

import (
	"net/http/httptest"
	"testing"

	"health-receiver/internal/ctxdb"
	"health-receiver/internal/storage"
)

func TestClientAPITenantScopeIgnoresUntrustedSelectors(t *testing.T) {
	expectedDB := &storage.DB{}
	req := httptest.NewRequest(
		"GET",
		"/api/health-briefing?schema=health_other&tenant=other&tenant_id=00000000-0000-0000-0000-000000000000",
		nil,
	)
	req.Header.Set("X-Tenant", "other")
	req.Header.Set("X-Tenant-Schema", "health_other")
	req = req.WithContext(ctxdb.WithDB(req.Context(), expectedDB, "health_expected"))

	handler := &Handler{}
	if got := handler.tenantDB(req); got != expectedDB {
		t.Fatalf("tenantDB returned %p, want authenticated context DB %p", got, expectedDB)
	}
	if got := handler.tenantSchema(req); got != "health_expected" {
		t.Fatalf("tenantSchema = %q, want authenticated context schema", got)
	}
}
