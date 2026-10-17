package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"health-receiver/internal/accessauth"
)

type assertionVerifierFunc func(context.Context, string) (*accessauth.Identity, error)

func (f assertionVerifierFunc) Verify(ctx context.Context, assertion string) (*accessauth.Identity, error) {
	return f(ctx, assertion)
}

func envLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestParseAccessProbeConfigIsIndependentAndFailClosed(t *testing.T) {
	if cfg, err := ParseAccessProbeConfig(envLookup(nil)); err != nil || cfg.Enabled {
		t.Fatalf("empty config = %#v, %v; want disabled", cfg, err)
	}
	valid := map[string]string{
		"MCP_ACCESS_ENABLED":  "true",
		"MCP_ACCESS_ISSUER":   "https://tenant.cloudflareaccess.com",
		"MCP_ACCESS_AUD":      "health-mcp-app",
		"MCP_ACCESS_USER_MAP": `{"owner@example.test":"admin"}`,
		"BROWSER_AUTH_MODE":   "existing",
		"MCP_OAUTH_ISSUER":    "https://other.example.test",
		"MCP_OAUTH_AUDIENCE":  "unrelated",
	}
	if cfg, err := ParseAccessProbeConfig(envLookup(valid)); err != nil || !cfg.Enabled || cfg.UserMap["owner@example.test"] != "admin" {
		t.Fatalf("independent config = %#v, %v", cfg, err)
	}

	bad := []map[string]string{
		{"MCP_ACCESS_ENABLED": "TRUE"},
		{"MCP_ACCESS_ISSUER": "https://tenant.cloudflareaccess.com"},
		{"MCP_ACCESS_ENABLED": "true", "MCP_ACCESS_ISSUER": "https://tenant.cloudflareaccess.com"},
		{"MCP_ACCESS_ENABLED": "true", "MCP_ACCESS_ISSUER": "https://tenant.cloudflareaccess.com", "MCP_ACCESS_AUD": "app", "MCP_ACCESS_USER_MAP": `{"owner@example.test":"admin","owner@example.test":"admin"}`},
		{"MCP_ACCESS_ENABLED": "true", "MCP_ACCESS_ISSUER": "https://tenant.cloudflareaccess.com/", "MCP_ACCESS_AUD": "app", "MCP_ACCESS_USER_MAP": `{"owner@example.test":"admin"}`},
		{"MCP_ACCESS_ENABLED": "true", "MCP_ACCESS_ISSUER": "https://tenant.cloudflareaccess.com", "MCP_ACCESS_AUD": "app,other", "MCP_ACCESS_USER_MAP": `{"owner@example.test":"admin"}`},
		{"MCP_ACCESS_ENABLED": "true", "MCP_ACCESS_ISSUER": "https://tenant.cloudflareaccess.com", "MCP_ACCESS_AUD": "app", "MCP_ACCESS_USER_MAP": `{" owner@example.test":"admin"}`},
		{"MCP_ACCESS_ENABLED": "true", "MCP_ACCESS_ISSUER": "https://tenant.cloudflareaccess.com", "MCP_ACCESS_AUD": "app", "MCP_ACCESS_USER_MAP": `{"owner@example.test":" admin"}`},
	}
	for i, values := range bad {
		if _, err := ParseAccessProbeConfig(envLookup(values)); err == nil {
			t.Errorf("case %d accepted invalid config %#v", i, values)
		}
	}
}

func TestAccessProbeOnlyAcceptsAssertionAndReturnsNoContent(t *testing.T) {
	cfg := &AccessProbeConfig{Enabled: true, UserMap: map[string]string{"owner@example.test": "admin"}}
	var calls int
	verifier := assertionVerifierFunc(func(_ context.Context, assertion string) (*accessauth.Identity, error) {
		calls++
		if assertion == "signed-unmapped" {
			return &accessauth.Identity{Email: "other@example.test", Username: "other"}, nil
		}
		if assertion != "signed-app-assertion" {
			return nil, errors.New("invalid")
		}
		return &accessauth.Identity{Email: "owner@example.test", Username: "admin"}, nil
	})
	h := NewAccessProbeHandler(cfg, verifier)

	for _, tc := range []struct {
		name   string
		setup  func(*http.Request)
		status int
	}{
		{"assertion only", func(r *http.Request) { r.Header.Set("Cf-Access-Jwt-Assertion", "signed-app-assertion") }, 204},
		{"opaque managed OAuth bearer", func(r *http.Request) {
			r.Header.Set("Cf-Access-Jwt-Assertion", "signed-app-assertion")
			r.Header.Set("Authorization", "Bearer oauth:opaque-token_1")
		}, 204},
		{"ambient cookies ignored", func(r *http.Request) {
			r.Header.Set("Cf-Access-Jwt-Assertion", "signed-app-assertion")
			r.Header.Set("Cookie", "CF_Authorization=ambient; health_session=ambient")
		}, 204},
		{"cookie cannot authenticate", func(r *http.Request) { r.Header.Set("Cookie", "CF_Authorization=ambient") }, 401},
		{"foreign bearer rejected", func(r *http.Request) {
			r.Header.Set("Cf-Access-Jwt-Assertion", "signed-app-assertion")
			r.Header.Set("Authorization", "Bearer another.jwt.token")
		}, 400},
		{"API key rejected", func(r *http.Request) {
			r.Header.Set("Cf-Access-Jwt-Assertion", "signed-app-assertion")
			r.Header.Set("X-API-Key", "legacy-key")
		}, 400},
		{"query credential rejected", func(r *http.Request) {
			r.Header.Set("Cf-Access-Jwt-Assertion", "signed-app-assertion")
			r.URL.RawQuery = "access_token=secret"
		}, 400},
		{"matching ancillary identity ignored", func(r *http.Request) {
			r.Header.Set("Cf-Access-Jwt-Assertion", "signed-app-assertion")
			r.Header.Set("Cf-Access-Authenticated-User-Email", "owner@example.test")
		}, 204},
		{"conflicting ancillary identity ignored", func(r *http.Request) {
			r.Header.Set("Cf-Access-Jwt-Assertion", "signed-app-assertion")
			r.Header.Set("Cf-Access-Authenticated-User-Email", "other@example.test")
			r.Header.Set("X-Forwarded-User", "other")
		}, 204},
		{"ancillary identity cannot authenticate", func(r *http.Request) {
			r.Header.Set("Cf-Access-Authenticated-User-Email", "owner@example.test")
		}, 401},
		{"unmapped verifier identity rejected", func(r *http.Request) { r.Header.Set("Cf-Access-Jwt-Assertion", "signed-unmapped") }, 403},
		{"empty assertion", func(r *http.Request) { r.Header.Set("Cf-Access-Jwt-Assertion", "") }, 401},
		{"wrong assertion", func(r *http.Request) { r.Header.Set("Cf-Access-Jwt-Assertion", "wrong") }, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp/oauth", nil)
			tc.setup(req)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%q", w.Code, tc.status, w.Body.String())
			}
			if w.Header().Get("Set-Cookie") != "" {
				t.Fatalf("unexpected Set-Cookie: %q", w.Header().Get("Set-Cookie"))
			}
			if tc.status == http.StatusNoContent && (w.Body.Len() != 0 || w.Header().Get("Cache-Control") != "no-store") {
				t.Fatalf("success body=%q cache=%q", w.Body.String(), w.Header().Get("Cache-Control"))
			}
		})
	}
	if calls != 7 {
		t.Fatalf("verifier calls=%d, want 7 assertion-bearing attempts", calls)
	}
}

func TestAccessProbeRejectsAmbiguousAssertionsAndUnavailableVerifier(t *testing.T) {
	cfg := &AccessProbeConfig{Enabled: true}
	var calls int
	h := NewAccessProbeHandler(cfg, assertionVerifierFunc(func(context.Context, string) (*accessauth.Identity, error) {
		calls++
		return nil, accessauth.ErrUnavailable
	}))
	for _, tc := range []struct {
		setup  func(*http.Request)
		status int
	}{
		{func(r *http.Request) {
			r.Header.Add("Cf-Access-Jwt-Assertion", "one")
			r.Header.Add("Cf-Access-Jwt-Assertion", "two")
		}, 401},
		{func(r *http.Request) { r.Header.Set("Cf-Access-Jwt-Assertion", "one,two") }, 401},
		{func(r *http.Request) { r.Header.Set("Cf-Access-Jwt-Assertion", " signed ") }, 401},
		{func(r *http.Request) {
			r.Header.Set("Cf-Access-Jwt-Assertion", "signed")
			r.Header.Add("Authorization", "Bearer oauth:one")
			r.Header.Add("Authorization", "Bearer oauth:two")
		}, 400},
	} {
		req := httptest.NewRequest(http.MethodPost, "/mcp/oauth", nil)
		tc.setup(req)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("ambiguous request status=%d want=%d", w.Code, tc.status)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp/oauth", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", "signed")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 503 || calls != 1 {
		t.Fatalf("unavailable verifier status=%d calls=%d", w.Code, calls)
	}
	if strings.Contains(w.Body.String(), "signed") {
		t.Fatalf("response leaked assertion: %q", w.Body.String())
	}
}

func TestAccessProbePathVariantsNeverReachNativeMCP(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/mcp/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(299) }))
	RegisterAccessProbe(mux, &AccessProbeConfig{}, nil)
	h := ProtectAccessProbePathEquivalents(mux)
	for _, target := range []string{
		"/mcp/oauth", "/mcp/oauth/", "/mcp/oauth/child", "/mcp/%6fauth", "/mcp/%256fauth",
		"/mcp/OAUTH", "/mcp//oauth", "/mcp/./oauth", "/mcp/oauth%2fchild",
	} {
		t.Run(target, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, target, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusNotFound {
				t.Fatalf("status=%d want=404 for %q", w.Code, target)
			}
			if w.Code == 299 || strings.Contains(w.Body.String(), "native") {
				t.Fatalf("reserved path reached native MCP handler: %q", target)
			}
		})
	}
	malformed := httptest.NewRequest(http.MethodPost, "/mcp/oauth", nil)
	malformed.URL = &url.URL{Path: "/mcp/oauth%ZZ", RawPath: "/mcp/oauth%ZZ"}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, malformed)
	if w.Code != http.StatusNotFound {
		t.Fatalf("malformed escaped route status=%d want=404", w.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp/other", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 299 {
		t.Fatalf("unrelated native route status=%d want=299", w.Code)
	}
}
