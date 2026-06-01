package ui

import (
	"net/http/httptest"
	"testing"
)

func TestAuthCookieSecurePolicy(t *testing.T) {
	plain := httptest.NewRequest("GET", "http://example.test/", nil)
	if authCookieSecure(plain) {
		t.Fatalf("plain HTTP request marked secure")
	}

	forwarded := httptest.NewRequest("GET", "http://example.test/", nil)
	forwarded.Header.Set("X-Forwarded-Proto", "https")
	if !authCookieSecure(forwarded) {
		t.Fatalf("X-Forwarded-Proto=https request not marked secure")
	}

	tlsReq := httptest.NewRequest("GET", "https://example.test/", nil)
	if !authCookieSecure(tlsReq) {
		t.Fatalf("HTTPS request not marked secure")
	}
}

func TestSetAuthCookieUsesOpaqueTokenAttributes(t *testing.T) {
	req := httptest.NewRequest("GET", "https://example.test/", nil)
	rec := httptest.NewRecorder()

	setAuthCookie(rec, req, "opaque-token")

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
