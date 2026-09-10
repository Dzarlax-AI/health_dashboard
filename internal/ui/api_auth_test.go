package ui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRejectUnauthenticatedUsesJSON401ForAPI(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/health-briefing?lang=en", nil)
	rec := httptest.NewRecorder()

	h.rejectUnauthenticated(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := rec.Body.String(); got != `{"error":"authentication required"}` {
		t.Fatalf("body = %q", got)
	}
}

func TestRejectUnauthenticatedKeepsPageLoginRedirect(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/sleep?lang=en", nil)
	rec := httptest.NewRecorder()

	h.rejectUnauthenticated(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if got := rec.Header().Get("Location"); got != "/login?next=/sleep?lang=en" {
		t.Fatalf("Location = %q", got)
	}
}

func TestSessionRecoveryRedirectsToSafeSPAPath(t *testing.T) {
	h := &Handler{}

	for _, tc := range []struct {
		name string
		next string
		want string
	}{
		{name: "spa path", next: "/sleep?lang=en", want: "/sleep?lang=en"},
		{name: "unsafe URL", next: "https://evil.example", want: "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/auth/session?next="+tc.next, nil)
			rec := httptest.NewRecorder()

			h.sessionRecovery(rec, req)

			if rec.Code != http.StatusFound {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
			}
			if got := rec.Header().Get("Location"); got != tc.want {
				t.Fatalf("Location = %q, want %q", got, tc.want)
			}
		})
	}
}
