package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	clientapi "health-receiver/internal/api"
	"health-receiver/internal/ctxdb"
)

func TestClientSessionUsesAuthenticatedContextOnly(t *testing.T) {
	tests := []struct {
		name    string
		isAdmin bool
	}{
		{name: "member", isAdmin: false},
		{name: "admin", isAdmin: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(
				http.MethodGet,
				"/api/session?is_admin=true&schema=health_other&tenant=other",
				nil,
			)
			req.Header.Set("X-Is-Admin", "true")
			req.Header.Set("X-Tenant", "other")
			req = req.WithContext(ctxdb.WithIsAdmin(req.Context(), tt.isAdmin))

			rec := httptest.NewRecorder()
			(&Handler{}).clientSession(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			var got clientapi.SessionResponse
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if got.IsAdmin != tt.isAdmin {
				t.Fatalf("is_admin = %v, want authenticated context value %v", got.IsAdmin, tt.isAdmin)
			}
		})
	}
}
