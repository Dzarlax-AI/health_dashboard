package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"health-receiver/internal/accessauth"
	"health-receiver/internal/ctxdb"
	"health-receiver/internal/storage"
)

type accessVerifierFunc func(context.Context, string) (*accessauth.Identity, error)

func accessEnvLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func (f accessVerifierFunc) Verify(ctx context.Context, assertion string) (*accessauth.Identity, error) {
	return f(ctx, assertion)
}

func TestParseAccessConfigIsIndependentAndFailClosed(t *testing.T) {
	if cfg, err := ParseAccessConfig(accessEnvLookup(nil)); err != nil || cfg.Enabled {
		t.Fatalf("empty config = %#v, %v; want disabled", cfg, err)
	}
	if cfg, err := ParseAccessConfig(accessEnvLookup(map[string]string{
		"MCP_ACCESS_ENABLED": "", "MCP_ACCESS_ISSUER": "", "MCP_ACCESS_AUD": "", "MCP_ACCESS_USER_MAP": "",
	})); err != nil || cfg.Enabled {
		t.Fatalf("empty sample config = %#v, %v; want disabled", cfg, err)
	}
	valid := map[string]string{
		"MCP_ACCESS_ENABLED": "true", "MCP_ACCESS_ISSUER": "https://tenant.cloudflareaccess.com",
		"MCP_ACCESS_AUD": "health-mcp-app", "MCP_ACCESS_USER_MAP": `{"owner@example.test":"admin"}`,
		"BROWSER_AUTH_MODE": "existing", "MCP_OAUTH_ISSUER": "https://other.example.test",
	}
	if cfg, err := ParseAccessConfig(accessEnvLookup(valid)); err != nil || !cfg.Enabled || cfg.UserMap["owner@example.test"] != "admin" {
		t.Fatalf("independent config = %#v, %v", cfg, err)
	}
	bad := []map[string]string{
		{"MCP_ACCESS_ENABLED": "TRUE"},
		{"MCP_ACCESS_ISSUER": "https://tenant.cloudflareaccess.com"},
		{"MCP_ACCESS_ENABLED": "true", "MCP_ACCESS_ISSUER": "https://tenant.cloudflareaccess.com"},
		{"MCP_ACCESS_ENABLED": "true", "MCP_ACCESS_ISSUER": "https://tenant.cloudflareaccess.com", "MCP_ACCESS_AUD": "app", "MCP_ACCESS_USER_MAP": `{"owner@example.test":"admin","owner@example.test":"admin"}`},
		{"MCP_ACCESS_ENABLED": "true", "MCP_ACCESS_ISSUER": "https://tenant.cloudflareaccess.com/", "MCP_ACCESS_AUD": "app", "MCP_ACCESS_USER_MAP": `{"owner@example.test":"admin"}`},
		{"MCP_ACCESS_ENABLED": "true", "MCP_ACCESS_ISSUER": "https://tenant.cloudflareaccess.com", "MCP_ACCESS_AUD": "app,other", "MCP_ACCESS_USER_MAP": `{"owner@example.test":"admin"}`},
		{"MCP_ACCESS_ENABLED": "true", "MCP_ACCESS_ISSUER": "https://tenant.cloudflareaccess.com", "MCP_ACCESS_AUD": "app other", "MCP_ACCESS_USER_MAP": `{"owner@example.test":"admin"}`},
		{"MCP_ACCESS_ENABLED": "true", "MCP_ACCESS_ISSUER": "https://tenant.cloudflareaccess.com", "MCP_ACCESS_AUD": "app", "MCP_ACCESS_USER_MAP": `{" owner@example.test":"admin"}`},
		{"MCP_ACCESS_ENABLED": "true", "MCP_ACCESS_ISSUER": "https://tenant.cloudflareaccess.com", "MCP_ACCESS_AUD": "app", "MCP_ACCESS_USER_MAP": `{"owner@example.test":" admin"}`},
	}
	for i, values := range bad {
		if _, err := ParseAccessConfig(accessEnvLookup(values)); err == nil {
			t.Errorf("case %d accepted invalid config %#v", i, values)
		}
	}
}

func TestAccessAuthResolvesMappedUserForEveryRequest(t *testing.T) {
	aliceDB, bobDB := &storage.DB{}, &storage.DB{}
	cfg := &AccessConfig{Enabled: true, Audience: "mcp-aud", UserMap: map[string]string{"alice@example.test": "alice", "bob@example.test": "bob"}}
	var resolverCalls atomic.Int32
	resolve := DBResolver(func(_ context.Context, username string) (*storage.DB, string, bool, bool) {
		resolverCalls.Add(1)
		switch username {
		case "alice":
			return aliceDB, "health_alice", false, true
		case "bob":
			return bobDB, "health_bob", false, true
		default:
			return nil, "", false, false
		}
	})
	verifier := accessVerifierFunc(func(_ context.Context, assertion string) (*accessauth.Identity, error) {
		switch assertion {
		case "alice.signed.assertion":
			return &accessauth.Identity{Email: "alice@example.test", Username: "alice"}, nil
		case "bob.signed.assertion":
			return &accessauth.Identity{Email: "bob@example.test", Username: "bob"}, nil
		case "mismatch.signed.assertion":
			return &accessauth.Identity{Email: "alice@example.test", Username: "bob"}, nil
		case "unmapped.signed.assertion":
			return &accessauth.Identity{Email: "other@example.test", Username: "alice"}, nil
		default:
			return nil, errors.New("invalid assertion")
		}
	})
	h := withAccessAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		db := ctxdb.FromContext(r.Context())
		schema := ctxdb.SchemaFromContext(r.Context())
		if db == aliceDB && schema == "health_alice" {
			_, _ = w.Write([]byte("alice"))
		} else if db == bobDB && schema == "health_bob" {
			_, _ = w.Write([]byte("bob"))
		} else {
			http.Error(w, "missing tenant context", http.StatusInternalServerError)
		}
	}), resolve, verifier, cfg)

	for _, tc := range []struct{ token, want string }{{"alice.signed.assertion", "alice"}, {"bob.signed.assertion", "bob"}, {"alice.signed.assertion", "alice"}} {
		request := httptest.NewRequest(http.MethodPost, "/mcp/oauth", nil)
		request.Header.Set(accessAssertionHeader, tc.token)
		request.Header.Set("Mcp-Session-Id", "same-client-session")
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.String() != tc.want {
			t.Fatalf("token=%q status=%d body=%q want=%q", tc.token, response.Code, response.Body.String(), tc.want)
		}
	}
	if got := resolverCalls.Load(); got != 3 {
		t.Fatalf("tenant resolver calls=%d want=3", got)
	}
}

func TestAccessAuthRejectsInvalidIdentityAndInactiveTenant(t *testing.T) {
	cfg := &AccessConfig{Enabled: true, Audience: "mcp-aud", UserMap: map[string]string{"alice@example.test": "alice"}}
	var resolverCalls int
	resolve := DBResolver(func(context.Context, string) (*storage.DB, string, bool, bool) {
		resolverCalls++
		return nil, "", false, false // Inactive or unavailable users fail closed.
	})
	verifier := accessVerifierFunc(func(_ context.Context, assertion string) (*accessauth.Identity, error) {
		if assertion == "unavailable.signed.assertion" {
			return nil, accessauth.ErrUnavailable
		}
		if assertion == "unmapped.signed.assertion" {
			return &accessauth.Identity{Email: "other@example.test", Username: "alice"}, nil
		}
		return &accessauth.Identity{Email: "alice@example.test", Username: "alice"}, nil
	})
	h := withAccessAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unauthorized request reached transport") }), resolve, verifier, cfg)
	for _, tc := range []struct {
		token string
		want  int
	}{
		{"unmapped.signed.assertion", http.StatusForbidden},
		{"inactive.signed.assertion", http.StatusForbidden},
		{"unavailable.signed.assertion", http.StatusServiceUnavailable},
	} {
		request := httptest.NewRequest(http.MethodPost, "/mcp/oauth", nil)
		request.Header.Set(accessAssertionHeader, tc.token)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		if response.Code != tc.want {
			t.Errorf("token=%q status=%d want=%d", tc.token, response.Code, tc.want)
		}
	}
	if resolverCalls != 1 {
		t.Fatalf("tenant resolver calls=%d want=1 (only mapped active identity reaches lookup)", resolverCalls)
	}
}

func TestAccessAuthCredentialBoundaries(t *testing.T) {
	cfg := &AccessConfig{Enabled: true, UserMap: map[string]string{"alice@example.test": "alice"}}
	var verifierCalls atomic.Int32
	verifier := accessVerifierFunc(func(context.Context, string) (*accessauth.Identity, error) {
		verifierCalls.Add(1)
		return &accessauth.Identity{Email: "alice@example.test", Username: "alice"}, nil
	})
	resolve := DBResolver(func(context.Context, string) (*storage.DB, string, bool, bool) {
		return &storage.DB{}, "health_alice", false, true
	})
	h := withAccessAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), resolve, verifier, cfg)
	tests := []struct {
		name   string
		setup  func(*http.Request)
		status int
	}{
		{"valid assertion", func(r *http.Request) { r.Header.Set(accessAssertionHeader, "signed.assertion.token") }, 204},
		{"cookie is not identity", func(r *http.Request) { r.Header.Set("Cookie", "CF_Authorization=ambient") }, 401},
		{"cookie does not override assertion", func(r *http.Request) {
			r.Header.Set(accessAssertionHeader, "signed.assertion.token")
			r.Header.Set("Cookie", "CF_Authorization=ambient; health_session=ambient")
		}, 204},
		{"Origin absent allowed", func(r *http.Request) { r.Header.Set(accessAssertionHeader, "signed.assertion.token") }, 204},
		{"Origin rejected", func(r *http.Request) {
			r.Header.Set(accessAssertionHeader, "signed.assertion.token")
			r.Header.Set("Origin", "https://browser.example.test")
		}, 403},
		{"lowercase Origin rejected", func(r *http.Request) {
			r.Header.Set(accessAssertionHeader, "signed.assertion.token")
			r.Header["origin"] = []string{"https://browser.example.test"}
		}, 403},
		{"empty Origin rejected", func(r *http.Request) {
			r.Header.Set(accessAssertionHeader, "signed.assertion.token")
			r.Header["Origin"] = []string{""}
		}, 403},
		{"null Origin rejected", func(r *http.Request) {
			r.Header.Set(accessAssertionHeader, "signed.assertion.token")
			r.Header.Set("Origin", "null")
		}, 403},
		{"duplicate Origin rejected", func(r *http.Request) {
			r.Header.Set(accessAssertionHeader, "signed.assertion.token")
			r.Header.Add("Origin", "https://one.example.test")
			r.Header.Add("Origin", "https://two.example.test")
		}, 403},
		{"unsigned identity header ignored", func(r *http.Request) {
			r.Header.Set(accessAssertionHeader, "signed.assertion.token")
			r.Header.Set("Cf-Access-Authenticated-User-Email", "other@example.test")
		}, 204},
		{"opaque managed bearer tolerated", func(r *http.Request) {
			r.Header.Set(accessAssertionHeader, "signed.assertion.token")
			r.Header.Set("Authorization", "Bearer oauth:opaque_123")
		}, 204},
		{"foreign bearer rejected", func(r *http.Request) {
			r.Header.Set(accessAssertionHeader, "signed.assertion.token")
			r.Header.Set("Authorization", "Bearer foreign.jwt.token")
		}, 400},
		{"duplicate assertion rejected", func(r *http.Request) {
			r.Header.Add(accessAssertionHeader, "one")
			r.Header.Add(accessAssertionHeader, "two")
		}, 401},
		{"combined assertion rejected", func(r *http.Request) { r.Header.Set(accessAssertionHeader, "one,two") }, 401},
		{"API key rejected", func(r *http.Request) {
			r.Header.Set(accessAssertionHeader, "signed.assertion.token")
			r.Header.Set("X-API-Key", "native-key")
		}, 400},
		{"query token rejected", func(r *http.Request) {
			r.Header.Set(accessAssertionHeader, "signed.assertion.token")
			r.URL.RawQuery = "token=secret"
		}, 400},
		{"duplicate authorization rejected", func(r *http.Request) {
			r.Header.Set(accessAssertionHeader, "signed.assertion.token")
			r.Header.Add("Authorization", "Bearer oauth:one")
			r.Header.Add("Authorization", "Bearer oauth:two")
		}, 400},
		{"oversized assertion rejected", func(r *http.Request) { r.Header.Set(accessAssertionHeader, strings.Repeat("a", 16*1024+1)) }, 401},
		{"GET not allowed", func(r *http.Request) {
			r.Method = http.MethodGet
			r.Header.Set(accessAssertionHeader, "signed.assertion.token")
		}, 405},
		{"DELETE not allowed", func(r *http.Request) {
			r.Method = http.MethodDelete
			r.Header.Set(accessAssertionHeader, "signed.assertion.token")
		}, 405},
		{"GET with Origin rejected before method", func(r *http.Request) {
			r.Method = http.MethodGet
			r.Header.Set(accessAssertionHeader, "signed.assertion.token")
			r.Header.Set("Origin", "null")
		}, 403},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/mcp/oauth", nil)
			tc.setup(request)
			response := httptest.NewRecorder()
			h.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%q", response.Code, tc.status, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control=%q want no-store", response.Header().Get("Cache-Control"))
			}
			if tc.status == http.StatusMethodNotAllowed && response.Header().Get("Allow") != http.MethodPost {
				t.Errorf("Allow=%q want=%q", response.Header().Get("Allow"), http.MethodPost)
			}
		})
	}
	if got := verifierCalls.Load(); got != 5 {
		t.Fatalf("verifier calls=%d want=5 (only valid assertion/transport combinations)", got)
	}
}

func TestAccessJWKSOutageDoesNotAffectNativeAPIKeys(t *testing.T) {
	db := &storage.DB{}
	nativeResolver := DBResolver(func(_ context.Context, key string) (*storage.DB, string, bool, bool) {
		if key == "native-key" {
			return db, "health_admin", true, true
		}
		return nil, "", false, false
	})
	cfg := &AccessConfig{Enabled: true, UserMap: map[string]string{"alice@example.test": "alice"}}
	accessVerifier := accessVerifierFunc(func(context.Context, string) (*accessauth.Identity, error) {
		return nil, accessauth.ErrUnavailable
	})
	mux := http.NewServeMux()
	mux.Handle("/mcp/oauth", NewAccessHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("JWKS outage must not reach MCP transport")
	}), func(context.Context, string) (*storage.DB, string, bool, bool) {
		return db, "health_alice", false, true
	}, accessVerifier, cfg))
	mux.Handle("/mcp", withAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ctxdb.FromContext(r.Context()) != db || ctxdb.SchemaFromContext(r.Context()) != "health_admin" {
			t.Fatal("native API key did not retain its tenant context")
		}
		w.WriteHeader(http.StatusNoContent)
	}), nativeResolver, nil, nil, nil))

	accessRequest := httptest.NewRequest(http.MethodPost, "/mcp/oauth", nil)
	accessRequest.Header.Set(accessAssertionHeader, "signed.assertion.token")
	accessResponse := httptest.NewRecorder()
	mux.ServeHTTP(accessResponse, accessRequest)
	if accessResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("Access outage status=%d want=503", accessResponse.Code)
	}
	nativeRequest := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	nativeRequest.Header.Set("X-API-Key", "native-key")
	nativeResponse := httptest.NewRecorder()
	mux.ServeHTTP(nativeResponse, nativeRequest)
	if nativeResponse.Code != http.StatusNoContent {
		t.Fatalf("native API-key status=%d want=204", nativeResponse.Code)
	}
}

func TestAccessPathVariantsNeverFallThroughNativeMCP(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/mcp/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(299) }))
	RegisterAccess(mux, nil, &AccessConfig{}, nil)
	h := ProtectAccessPathEquivalents(mux)
	for _, path := range []string{
		"/mcp/oauth", "/mcp/oauth/", "/mcp/oauth/child", "/mcp/%6fauth", "/mcp/%256fauth",
		"/mcp/OAUTH", "/mcp//oauth", "/mcp/./oauth", "/mcp/oauth%2fchild", "/mcp/%25252525256fauth",
	} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		if response.Code == 299 {
			t.Errorf("path %q fell through native MCP", path)
		}
	}
	malformed := httptest.NewRequest(http.MethodPost, "/mcp/oauth", nil)
	malformed.URL = &url.URL{Path: "/mcp/oauth%ZZ", RawPath: "/mcp/oauth%ZZ"}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, malformed)
	if response.Code == 299 {
		t.Fatal("malformed escaped path fell through native MCP")
	}
	other := httptest.NewRecorder()
	h.ServeHTTP(other, httptest.NewRequest(http.MethodPost, "/mcp/other", nil))
	if other.Code != 299 {
		t.Fatalf("unrelated native path status=%d want=299", other.Code)
	}
}
