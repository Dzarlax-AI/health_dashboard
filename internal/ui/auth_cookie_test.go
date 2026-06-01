package ui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthCookieSecurePolicy(t *testing.T) {
	h := &Handler{}

	plain := httptest.NewRequest("GET", "http://example.test/", nil)
	if h.authCookieSecure(plain) {
		t.Fatalf("plain HTTP request marked secure")
	}

	spoofed := httptest.NewRequest("GET", "http://example.test/", nil)
	spoofed.RemoteAddr = "203.0.113.10:1234"
	spoofed.Header.Set("X-Forwarded-Proto", "https")
	if h.authCookieSecure(spoofed) {
		t.Fatalf("untrusted X-Forwarded-Proto=https request marked secure")
	}

	trusted := httptest.NewRequest("GET", "http://example.test/", nil)
	trusted.RemoteAddr = "10.0.0.10:1234"
	trusted.Header.Set("X-Forwarded-Proto", "https")
	h.trustFwdAuth = true
	if !h.authCookieSecure(trusted) {
		t.Fatalf("trusted X-Forwarded-Proto=https request not marked secure")
	}

	tlsReq := httptest.NewRequest("GET", "https://example.test/", nil)
	if !h.authCookieSecure(tlsReq) {
		t.Fatalf("HTTPS request not marked secure")
	}
}

func TestSetAuthCookieUsesOpaqueTokenAttributes(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest("GET", "https://example.test/", nil)
	rec := httptest.NewRecorder()

	h.setAuthCookie(rec, req, "opaque-token")

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	c := cookies[0]
	if c.Name != authCookieName || c.Value != "opaque-token" {
		t.Fatalf("cookie = %s=%q, want auth opaque token", c.Name, c.Value)
	}
	if !c.HttpOnly || !c.Secure || c.MaxAge <= 0 {
		t.Fatalf("cookie attrs HttpOnly=%v Secure=%v MaxAge=%d, want secure session cookie", c.HttpOnly, c.Secure, c.MaxAge)
	}
}

func TestLogoutRejectsNonPost(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "http://example.test/logout", nil)
	rec := httptest.NewRecorder()

	h.logout(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /logout status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
