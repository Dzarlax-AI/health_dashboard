package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestAccessProbeRejectionLogsOnlyFixedFlags(t *testing.T) {
	const (
		issuer = "https://ISSUER_SENTINEL.example.test"
		aud    = "AUDIENCE_SENTINEL"
		email  = "PRIVATE_EMAIL_SENTINEL@example.test"
	)
	cfg := &AccessProbeConfig{Enabled: true, Issuer: issuer, Audience: aud, UserMap: map[string]string{email: "admin"}}
	claims := map[string]any{
		"iss": issuer, "aud": aud, "type": "app", "email": email, "sub": "SUBJECT_SENTINEL",
		"iat": time.Now().Add(-time.Minute).Unix(), "nbf": time.Now().Add(-time.Minute).Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	}
	header := map[string]string{"alg": "RS256", "kid": "KID_SENTINEL"}
	encode := func(value any) string {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(encoded)
	}
	assertion := encode(header) + "." + encode(claims) + ".SIGNATURE_SENTINEL"
	h := NewAccessProbeHandler(cfg, assertionVerifierFunc(func(context.Context, string) (*accessauth.Identity, error) {
		return nil, errors.New("RAW_VERIFIER_ERROR_SENTINEL")
	}))

	var output bytes.Buffer
	previousWriter, previousFlags, previousPrefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(&output)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	}()

	request := httptest.NewRequest(http.MethodPost, "/mcp/oauth", nil)
	request.Header.Set("Cf-Access-Jwt-Assertion", assertion)
	request.Header.Set("Authorization", "Bearer oauth:AUTH_SENTINEL")
	request.Header.Set("Cookie", "CF_Authorization=COOKIE_SENTINEL")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want=401", response.Code)
	}
	firstLog := output.String()
	for _, expected := range []string{
		"reason=assertion_rejected", "assertion_count=1", "authorization_count=1", "authorization_oauth_prefix=true",
		"claims_unverified_parsed=true", "claim_rs256=true", "claim_kid_present=true", "claim_issuer_match=true", "claim_audience_match=true",
		"claim_audience_count=1", "claim_audience_unique_count=1", "claim_audience_contains_expected=true",
		"claim_app_type=true", "claim_iat_present=true", "claim_nbf_present=true", "claim_exp_present=true", "claim_iat_time_valid=true",
		"claim_nbf_time_valid=true", "claim_exp_time_valid=true", "claim_email_mapped=true",
	} {
		if !strings.Contains(firstLog, expected) {
			t.Errorf("log missing %q: %s", expected, firstLog)
		}
	}
	for _, secret := range []string{assertion, "AUTH_SENTINEL", "COOKIE_SENTINEL", "RAW_VERIFIER_ERROR_SENTINEL", "KID_SENTINEL", issuer, aud, email, "SUBJECT_SENTINEL"} {
		if strings.Contains(firstLog, secret) {
			t.Errorf("log exposed sensitive/untrusted value %q", secret)
		}
	}

	output.Reset()
	request = httptest.NewRequest(http.MethodPost, "/mcp/oauth?api_key=QUERY_SENTINEL", nil)
	request.Header.Set("Cf-Access-Jwt-Assertion", assertion)
	request.Header.Set("X-API-Key", "API_KEY_SENTINEL")
	request.Header.Set("Cookie", "CF_Authorization=COOKIE_SENTINEL")
	response = httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("credential conflict status=%d want=400", response.Code)
	}
	secondLog := output.String()
	for _, expected := range []string{"reason=api_key_present", "api_key_present=true", "query_credential_present=true"} {
		if !strings.Contains(secondLog, expected) {
			t.Errorf("log missing %q: %s", expected, secondLog)
		}
	}
	for _, secret := range []string{assertion, "API_KEY_SENTINEL", "QUERY_SENTINEL", "COOKIE_SENTINEL", email} {
		if strings.Contains(secondLog, secret) {
			t.Errorf("credential rejection log exposed %q", secret)
		}
	}

	output.Reset()
	successHandler := NewAccessProbeHandler(cfg, assertionVerifierFunc(func(context.Context, string) (*accessauth.Identity, error) {
		return &accessauth.Identity{Email: email, Username: "admin"}, nil
	}))
	request = httptest.NewRequest(http.MethodPost, "/mcp/oauth", nil)
	request.Header.Set("Cf-Access-Jwt-Assertion", assertion)
	response = httptest.NewRecorder()
	successHandler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || output.String() != "MCP Access assertion probe verified\n" {
		t.Fatalf("success response=%d logs=%q", response.Code, output.String())
	}
}

func TestAudienceShapeDiagnosticsAreValueFree(t *testing.T) {
	const expected = "EXPECTED_AUDIENCE_SENTINEL"
	for _, tc := range []struct {
		name            string
		raw             string
		count           int
		uniqueCount     int
		contains, exact bool
	}{
		{"wrong", `"OTHER_AUDIENCE_SENTINEL"`, 1, 1, false, false},
		{"single", `"EXPECTED_AUDIENCE_SENTINEL"`, 1, 1, true, true},
		{"multiple", `["EXPECTED_AUDIENCE_SENTINEL","OTHER_AUDIENCE_SENTINEL"]`, 2, 2, true, false},
		{"duplicate", `["EXPECTED_AUDIENCE_SENTINEL","EXPECTED_AUDIENCE_SENTINEL"]`, 2, 1, true, false},
		{"malformed value", `["EXPECTED_AUDIENCE_SENTINEL",7]`, 0, 0, false, false},
		{"malformed null", `null`, 0, 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count, uniqueCount, contains, exact := audienceShape(json.RawMessage(tc.raw), expected)
			if count != tc.count || uniqueCount != tc.uniqueCount || contains != tc.contains || exact != tc.exact {
				t.Fatalf("audienceShape = (%d, %d, %t, %t), want (%d, %d, %t, %t)", count, uniqueCount, contains, exact, tc.count, tc.uniqueCount, tc.contains, tc.exact)
			}
		})
	}
}

func TestExclusiveAudienceFailureUsesFixedDiagnosticReason(t *testing.T) {
	cfg := &AccessProbeConfig{Enabled: true, Issuer: "https://issuer.example.test", Audience: "expected-aud-SENTINEL", UserMap: map[string]string{"owner@example.test": "admin"}}
	claimPayload, _ := json.Marshal(map[string]any{"iss": cfg.Issuer, "aud": []string{cfg.Audience, "other-aud-SENTINEL"}, "type": "app", "email": "owner@example.test"})
	encode := func(value []byte) string { return base64.RawURLEncoding.EncodeToString(value) }
	assertion := encode([]byte(`{"alg":"RS256","kid":"KID_SENTINEL"}`)) + "." + encode(claimPayload) + ".SIGNATURE_SENTINEL"
	h := NewAccessProbeHandler(cfg, assertionVerifierFunc(func(context.Context, string) (*accessauth.Identity, error) {
		return nil, accessauth.ErrExclusiveAudience
	}))
	var output bytes.Buffer
	previousWriter, previousFlags := log.Writer(), log.Flags()
	log.SetOutput(&output)
	log.SetFlags(0)
	defer func() { log.SetOutput(previousWriter); log.SetFlags(previousFlags) }()

	request := httptest.NewRequest(http.MethodPost, "/mcp/oauth", nil)
	request.Header.Set("Cf-Access-Jwt-Assertion", assertion)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want=401", response.Code)
	}
	logged := output.String()
	for _, expected := range []string{"reason=assertion_exclusive_audience", "claim_audience_count=2", "claim_audience_unique_count=2", "claim_audience_contains_expected=true", "claim_audience_match=false"} {
		if !strings.Contains(logged, expected) {
			t.Errorf("log missing %q: %s", expected, logged)
		}
	}
	for _, secret := range []string{assertion, cfg.Audience, "other-aud-SENTINEL", "KID_SENTINEL", "owner@example.test", accessauth.ErrExclusiveAudience.Error()} {
		if strings.Contains(logged, secret) {
			t.Errorf("diagnostic log exposed untrusted value %q", secret)
		}
	}
}

func TestProbeRejectionLogThrottleIsPerReasonAndExpires(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	throttle := newProbeRejectionLogThrottleWithClock(func() time.Time { return now })
	if !throttle.allow(probeRejectAssertionRejected) || throttle.allow(probeRejectAssertionRejected) {
		t.Fatal("first assertion rejection should log and immediate duplicate should be suppressed")
	}
	if !throttle.allow(probeRejectAPIKey) {
		t.Fatal("different fixed reason should have an independent throttle")
	}
	now = now.Add(probeRejectionLogInterval - time.Nanosecond)
	if throttle.allow(probeRejectAssertionRejected) {
		t.Fatal("same reason should remain suppressed before the five-second window expires")
	}
	now = now.Add(time.Nanosecond)
	if !throttle.allow(probeRejectAssertionRejected) {
		t.Fatal("same reason should be allowed at the five-second boundary")
	}
	if throttle.allow(probeRejectReason("untrusted-reason")) {
		t.Fatal("reason outside the fixed enum should never enter the limiter")
	}
}

func TestAccessProbeConcurrentRejectionsLogOncePerReason(t *testing.T) {
	cfg := &AccessProbeConfig{Enabled: true, UserMap: map[string]string{"owner@example.test": "admin"}}
	h := NewAccessProbeHandler(cfg, assertionVerifierFunc(func(context.Context, string) (*accessauth.Identity, error) {
		return nil, errors.New("ignored verifier error")
	}))
	var output bytes.Buffer
	previousWriter, previousFlags := log.Writer(), log.Flags()
	log.SetOutput(&output)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	}()

	const burst = 64
	var wg sync.WaitGroup
	wg.Add(burst)
	for range burst {
		go func() {
			defer wg.Done()
			request := httptest.NewRequest(http.MethodPost, "/mcp/oauth", nil)
			response := httptest.NewRecorder()
			h.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Errorf("burst response=%d want=401", response.Code)
			}
		}()
	}
	wg.Wait()
	if lines := strings.Count(output.String(), "MCP Access assertion probe rejected"); lines != 1 {
		t.Fatalf("same-reason burst emitted %d diagnostic lines, want 1: %q", lines, output.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/mcp/oauth", nil)
	request.Header.Set("Cf-Access-Jwt-Assertion", "not-a-jwt")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("distinct-reason response=%d want=401", response.Code)
	}
	if lines := strings.Count(output.String(), "MCP Access assertion probe rejected"); lines != 2 {
		t.Fatalf("distinct fixed reason did not get its own diagnostic: %q", output.String())
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
		"/mcp/%25252525256fauth", "/mcp/%" + strings.Repeat("25", 200) + "6fauth",
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
