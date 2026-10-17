package ui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"health-receiver/internal/accessauth"
	"health-receiver/internal/ctxdb"
	"health-receiver/internal/storage"
)

type fakeAccessVerifier struct {
	identities map[string]*accessauth.Identity
	err        error
	calls      int
	last       string
}

func (f *fakeAccessVerifier) Verify(_ context.Context, token string) (*accessauth.Identity, error) {
	f.calls++
	f.last = token
	if f.err != nil {
		return nil, f.err
	}
	return f.identities[token], nil
}

func cloudflareTestHandler() (*Handler, *fakeAccessVerifier, *int) {
	verifier := &fakeAccessVerifier{identities: map[string]*accessauth.Identity{"jwt-a": {Email: "a@example.test", Username: "alice"}, "jwt-b": {Email: "b@example.test", Username: "bob"}}}
	userLookups := 0
	h := &Handler{accessOrigin: "https://health.example.test"}
	h.ConfigureCloudflareAccess(verifier, h.accessOrigin)
	db := &storage.DB{}
	h.accessUserResolver = func(_ context.Context, username string) (*storage.DB, string, bool, bool) {
		userLookups++
		if username != "alice" && username != "bob" {
			return nil, "", false, false
		}
		return db, "health_" + username, username == "alice", true
	}
	h.accessAPIKeyResolver = func(_ context.Context, key string) (*storage.DB, string, bool, bool) {
		if key == "native-key" {
			return db, "health_alice", true, true
		}
		return nil, "", false, false
	}
	return h, verifier, &userLookups
}

func TestCloudflareGuardBindsJWTToMappedUserWithoutSessionCookie(t *testing.T) {
	h, verifier, lookups := cloudflareTestHandler()
	var gotUser, gotSchema string
	next := func(w http.ResponseWriter, r *http.Request) {
		gotSchema = ctxdb.SchemaFromContext(r.Context())
		if ctxdb.IsAdminFromContext(r.Context()) {
			gotUser = "alice"
		} else {
			gotUser = "bob"
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	r.Header.Set("Cf-Access-Jwt-Assertion", "jwt-b")
	r.AddCookie(&http.Cookie{Name: "auth", Value: "alice-old-session"})
	r.Header.Set("X-authentik-username", "alice")
	w := httptest.NewRecorder()
	h.guard(next).ServeHTTP(w, r)
	if w.Code != http.StatusOK || gotUser != "bob" || gotSchema != "health_bob" {
		t.Fatalf("status=%d user=%q schema=%q", w.Code, gotUser, gotSchema)
	}
	if len(w.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("unexpected local session cookie: %v", w.Header().Values("Set-Cookie"))
	}
	if *lookups != 1 || verifier.last != "jwt-b" {
		t.Fatalf("lookups=%d token=%q", *lookups, verifier.last)
	}
}

func TestCloudflareGuardRejectsOldCookieAndForwardAuthHeaders(t *testing.T) {
	h, verifier, _ := cloudflareTestHandler()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: "auth", Value: "old"})
	r.Header.Set("X-authentik-username", "alice")
	r.Header.Set("X-authentik-email", "a@example.test")
	w := httptest.NewRecorder()
	h.guard(func(http.ResponseWriter, *http.Request) { t.Fatal("forged identity authorized") }).ServeHTTP(w, r)
	if w.Code != http.StatusFound || verifier.calls != 0 {
		t.Fatalf("status=%d verifier calls=%d", w.Code, verifier.calls)
	}
}

func TestCloudflareGuardSupportsAccessCookieAndRejectsAmbiguousAssertions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*http.Request)
		want  int
	}{
		{"cookie", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "CF_Authorization", Value: "jwt-a"}) }, http.StatusNoContent},
		{"same header and cookie", func(r *http.Request) {
			r.Header.Set("Cf-Access-Jwt-Assertion", "jwt-a")
			r.AddCookie(&http.Cookie{Name: "CF_Authorization", Value: "jwt-a"})
		}, http.StatusNoContent},
		{"conflicting tokens", func(r *http.Request) {
			r.Header.Set("Cf-Access-Jwt-Assertion", "jwt-a")
			r.AddCookie(&http.Cookie{Name: "CF_Authorization", Value: "jwt-b"})
		}, http.StatusUnauthorized},
		{"duplicate headers", func(r *http.Request) {
			r.Header.Add("Cf-Access-Jwt-Assertion", "jwt-a")
			r.Header.Add("Cf-Access-Jwt-Assertion", "jwt-a")
		}, http.StatusUnauthorized},
		{"duplicate cookies", func(r *http.Request) { r.Header.Add("Cookie", "CF_Authorization=jwt-a; CF_Authorization=jwt-a") }, http.StatusUnauthorized},
		{"bad header cannot fall back", func(r *http.Request) {
			r.Header.Set("Cf-Access-Jwt-Assertion", "bad")
			r.AddCookie(&http.Cookie{Name: "CF_Authorization", Value: "jwt-a"})
		}, http.StatusUnauthorized},
		{"empty header cannot fall back", func(r *http.Request) {
			r.Header["Cf-Access-Jwt-Assertion"] = []string{""}
			r.AddCookie(&http.Cookie{Name: "CF_Authorization", Value: "jwt-a"})
		}, http.StatusUnauthorized},
		{"empty cookie cannot be ignored", func(r *http.Request) {
			r.Header.Set("Cf-Access-Jwt-Assertion", "jwt-a")
			r.Header.Add("Cookie", "CF_Authorization=")
		}, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := cloudflareTestHandler()
			r := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
			tc.setup(r)
			w := httptest.NewRecorder()
			h.guard(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d want %d body=%q", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestCloudflareCSRFRequiresExactOriginOnlyForJWT(t *testing.T) {
	for _, tc := range []struct {
		name, origin string
		duplicates   bool
		want         int
	}{
		{"missing", "", false, http.StatusForbidden},
		{"null", "null", false, http.StatusForbidden},
		{"foreign", "https://attacker.example", false, http.StatusForbidden},
		{"exact", "https://health.example.test", false, http.StatusNoContent},
		{"duplicate", "https://health.example.test", true, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := cloudflareTestHandler()
			r := httptest.NewRequest(http.MethodPost, "/api/settings", nil)
			r.Header.Set("Cf-Access-Jwt-Assertion", "jwt-a")
			if tc.origin != "" {
				r.Header.Add("Origin", tc.origin)
			}
			if tc.duplicates {
				r.Header.Add("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			h.guard(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d want %d", w.Code, tc.want)
			}
		})
	}
}

func TestCloudflareNativeAPIKeyDoesNotDependOnJWKSOrOrigin(t *testing.T) {
	h, verifier, _ := cloudflareTestHandler()
	r := httptest.NewRequest(http.MethodPost, "/api/metrics", nil)
	r.Header.Set("X-API-Key", "native-key")
	w := httptest.NewRecorder()
	h.guard(func(w http.ResponseWriter, r *http.Request) {
		if ctxdb.SchemaFromContext(r.Context()) != "health_alice" {
			t.Fatal("wrong API key tenant")
		}
		w.WriteHeader(http.StatusNoContent)
	}).ServeHTTP(w, r)
	if w.Code != http.StatusNoContent || verifier.calls != 0 {
		t.Fatalf("status=%d verifier calls=%d", w.Code, verifier.calls)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	r.Header.Set("X-API-Key", "invalid")
	r.Header.Set("Cf-Access-Jwt-Assertion", "jwt-a")
	w = httptest.NewRecorder()
	h.guard(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid explicit key fell through to JWT") }).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || verifier.calls != 0 {
		t.Fatalf("status=%d verifier calls=%d", w.Code, verifier.calls)
	}
}

func TestCloudflareVerifierEndpointIsJWTOnlyAndModeBound(t *testing.T) {
	h, verifier, _ := cloudflareTestHandler()
	r := httptest.NewRequest(http.MethodGet, "/_internal/cloudflare-access/verify", nil)
	r.Header.Set("Cf-Access-Jwt-Assertion", "jwt-a")
	w := httptest.NewRecorder()
	h.cloudflareAccessVerify(w, r)
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d body=%q headers=%v", w.Code, w.Body.String(), w.Header())
	}
	r = httptest.NewRequest(http.MethodGet, "/_internal/cloudflare-access/verify", nil)
	r.Header.Set("X-API-Key", "native-key")
	w = httptest.NewRecorder()
	h.cloudflareAccessVerify(w, r)
	if w.Code != http.StatusUnauthorized || verifier.calls != 1 {
		t.Fatalf("API key bypassed verify endpoint: status=%d calls=%d", w.Code, verifier.calls)
	}
	disabled := &Handler{}
	w = httptest.NewRecorder()
	disabled.cloudflareAccessVerify(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("disabled endpoint status=%d", w.Code)
	}
}

func TestCloudflareModeRecoveryLoginAndSetupDoNotCreateSessions(t *testing.T) {
	h, _, _ := cloudflareTestHandler()
	for _, tc := range []struct {
		method, path, wantLocation string
		wantStatus                 int
	}{
		{http.MethodGet, "/login?next=%2Fsleep%3Flang%3Den", "/auth/session?next=%2Fsleep%3Flang%3Den", http.StatusFound},
		{http.MethodPost, "/login", "", http.StatusForbidden},
		{http.MethodGet, "/setup", "/auth/session?next=%2F", http.StatusFound},
		{http.MethodPost, "/setup", "", http.StatusForbidden},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			if tc.path == "/setup" {
				h.setup(w, r)
			} else {
				h.login(w, r)
			}
			if w.Code != tc.wantStatus || w.Header().Get("Location") != tc.wantLocation {
				t.Fatalf("status=%d location=%q", w.Code, w.Header().Get("Location"))
			}
			if len(w.Header().Values("Set-Cookie")) != 0 {
				t.Fatal("mode created a local auth session")
			}
		})
	}
	r := httptest.NewRequest(http.MethodGet, "/auth/session?next=%2Factivity", nil)
	r.Header.Set("Cf-Access-Jwt-Assertion", "jwt-a")
	w := httptest.NewRecorder()
	h.guard(h.sessionRecovery).ServeHTTP(w, r)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/activity" || len(w.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("recovery status=%d location=%q cookies=%v", w.Code, w.Header().Get("Location"), w.Header().Values("Set-Cookie"))
	}
}

func TestCloudflareVerifierUnavailableIs503(t *testing.T) {
	h, verifier, _ := cloudflareTestHandler()
	verifier.err = accessauth.ErrUnavailable
	r := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	r.Header.Set("Cf-Access-Jwt-Assertion", "jwt-a")
	w := httptest.NewRecorder()
	h.guard(func(http.ResponseWriter, *http.Request) { t.Fatal("unavailable verifier authorized") }).ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", w.Code)
	}
	if !errors.Is(verifier.err, accessauth.ErrUnavailable) {
		t.Fatal("bad test setup")
	}
}

func TestCloudflareLogoutRequiresJWTAndExactOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, origin string
		want         int
		location     string
	}{
		{"valid", "https://health.example.test", http.StatusSeeOther, "/cdn-cgi/access/logout"},
		{"missing origin", "", http.StatusForbidden, ""},
		{"foreign origin", "https://attacker.example", http.StatusForbidden, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := cloudflareTestHandler()
			r := httptest.NewRequest(http.MethodPost, "/logout", nil)
			r.Header.Set("Cf-Access-Jwt-Assertion", "jwt-a")
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			h.logout(w, r)
			if w.Code != tc.want || w.Header().Get("Location") != tc.location {
				t.Fatalf("status=%d location=%q", w.Code, w.Header().Get("Location"))
			}
			if len(w.Header().Values("Set-Cookie")) != 0 {
				t.Fatal("Cloudflare logout modified Health session cookies")
			}
		})
	}
}
