package accessauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestParseConfigModeAndExactMap(t *testing.T) {
	base := map[string]string{
		"BROWSER_AUTH_MODE":          "cloudflare",
		"CLOUDFLARE_ACCESS_ISSUER":   "https://team.example.com",
		"CLOUDFLARE_ACCESS_AUD":      "aud-1",
		"CLOUDFLARE_ACCESS_USER_MAP": `{"a@example.test":"alice"}`,
		"CLOUDFLARE_ACCESS_ORIGIN":   "https://health.example.test",
	}
	lookup := func(values map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := values[k]; return v, ok }
	}
	cfg, err := ParseConfig(lookup(base))
	if err != nil || cfg.UserMap["a@example.test"] != "alice" {
		t.Fatalf("ParseConfig = %#v, %v", cfg, err)
	}
	for _, tc := range []struct {
		name   string
		change func(map[string]string)
	}{
		{"missing origin", func(m map[string]string) { delete(m, "CLOUDFLARE_ACCESS_ORIGIN") }},
		{"issuer path", func(m map[string]string) { m["CLOUDFLARE_ACCESS_ISSUER"] += "/path" }},
		{"issuer slash", func(m map[string]string) { m["CLOUDFLARE_ACCESS_ISSUER"] += "/" }},
		{"duplicate map key", func(m map[string]string) {
			m["CLOUDFLARE_ACCESS_USER_MAP"] = `{"a@example.test":"a","a@example.test":"b"}`
		}},
		{"wrong mode", func(m map[string]string) { m["BROWSER_AUTH_MODE"] = "authentik" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]string{}
			for k, v := range base {
				m[k] = v
			}
			tc.change(m)
			if _, err := ParseConfig(lookup(m)); err == nil {
				t.Fatal("expected config rejection")
			}
		})
	}
	if cfg, err := ParseConfig(func(k string) (string, bool) {
		if k == "BROWSER_AUTH_MODE" {
			return "existing", true
		}
		return "", false
	}); err != nil || cfg != nil {
		t.Fatalf("existing mode = %#v, %v", cfg, err)
	}
	if cfg, err := ParseConfig(func(k string) (string, bool) {
		if k == "BROWSER_AUTH_MODE" {
			return "existing", true
		}
		if strings.HasPrefix(k, "CLOUDFLARE_ACCESS_") {
			return "", true
		}
		return "", false
	}); err != nil || cfg != nil {
		t.Fatalf("blank sample Cloudflare settings = %#v, %v", cfg, err)
	}
	if _, err := ParseConfig(func(k string) (string, bool) {
		if k == "CLOUDFLARE_ACCESS_AUD" {
			return "aud", true
		}
		return "", false
	}); err == nil {
		t.Fatal("Cloudflare setting without mode accepted")
	}
}

func TestParseUserMapRejectsNonObjectAndTrailingData(t *testing.T) {
	for _, input := range []string{`[]`, `{"a":"b"} {}`, `{"a":1}`, `{}`} {
		if _, err := ParseUserMap(input); err == nil {
			t.Errorf("ParseUserMap(%q) accepted", input)
		}
	}
}

func TestVerifierValidatesClaimsAndCachesKeys(t *testing.T) {
	key := mustRSA(t)
	var fetches atomic.Int32
	client := testClient(func() (*http.Response, error) {
		fetches.Add(1)
		return response(http.StatusOK, jwksJSON("key-a", &key.PublicKey)), nil
	})
	v := mustVerifier(t, "https://jwks.invalid/certs", map[string]string{"alice@example.test": "alice"}, client)
	token := signedToken(t, key, "key-a", jwt.SigningMethodRS256, claims{Email: "alice@example.test", Type: "app", RegisteredClaims: registeredClaims(time.Now().Add(-time.Minute), time.Now().Add(-time.Minute), time.Now().Add(time.Hour), "https://team.example.com", jwt.ClaimStrings{"aud-1"})})
	for i := 0; i < 2; i++ {
		id, err := v.Verify(context.Background(), token)
		if err != nil || id.Username != "alice" || id.Email != "alice@example.test" {
			t.Fatalf("Verify = %#v, %v", id, err)
		}
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("JWKS fetches = %d, want cached single fetch", got)
	}
}

func TestVerifierRejectsInvalidTokens(t *testing.T) {
	key := mustRSA(t)
	otherKey := mustRSA(t)
	client := testClient(func() (*http.Response, error) { return response(http.StatusOK, jwksJSON("key-a", &key.PublicKey)), nil })
	v := mustVerifier(t, "https://jwks.invalid/certs", map[string]string{"alice@example.test": "alice"}, client)
	now := time.Now()
	valid := func(c claims, method jwt.SigningMethod, kid string, k *rsa.PrivateKey) string {
		return signedToken(t, k, kid, method, c)
	}
	base := func() claims {
		return claims{Email: "alice@example.test", Type: "app", RegisteredClaims: registeredClaims(now.Add(-time.Minute), now.Add(-time.Minute), now.Add(time.Hour), "https://team.example.com", jwt.ClaimStrings{"aud-1"})}
	}
	bad := []struct{ name, token string }{
		{"expired", valid(claims{Email: "alice@example.test", Type: "app", RegisteredClaims: registeredClaims(now.Add(-time.Hour), now.Add(-time.Hour), now.Add(-time.Minute), "https://team.example.com", jwt.ClaimStrings{"aud-1"})}, jwt.SigningMethodRS256, "key-a", key)},
		{"not before future", valid(claims{Email: "alice@example.test", Type: "app", RegisteredClaims: registeredClaims(now.Add(-time.Minute), now.Add(time.Minute), now.Add(time.Hour), "https://team.example.com", jwt.ClaimStrings{"aud-1"})}, jwt.SigningMethodRS256, "key-a", key)},
		{"issued at future", valid(claims{Email: "alice@example.test", Type: "app", RegisteredClaims: registeredClaims(now.Add(time.Minute), now.Add(-time.Minute), now.Add(time.Hour), "https://team.example.com", jwt.ClaimStrings{"aud-1"})}, jwt.SigningMethodRS256, "key-a", key)},
		{"wrong issuer", valid(func() claims { c := base(); c.Issuer = "https://other.example.com"; return c }(), jwt.SigningMethodRS256, "key-a", key)},
		{"wrong signature", valid(base(), jwt.SigningMethodRS256, "key-a", otherKey)},
		{"wrong audience", valid(func() claims { c := base(); c.Audience = jwt.ClaimStrings{"other-app"}; return c }(), jwt.SigningMethodRS256, "key-a", key)},
		{"missing expiration", valid(func() claims { c := base(); c.ExpiresAt = nil; return c }(), jwt.SigningMethodRS256, "key-a", key)},
		{"wrong app type", valid(func() claims { c := base(); c.Type = "login"; return c }(), jwt.SigningMethodRS256, "key-a", key)},
		{"multiple audiences", valid(func() claims { c := base(); c.Audience = jwt.ClaimStrings{"aud-1", "extra"}; return c }(), jwt.SigningMethodRS256, "key-a", key)},
		{"unmapped email", valid(func() claims { c := base(); c.Email = "someone@example.test"; return c }(), jwt.SigningMethodRS256, "key-a", key)},
		{"unknown kid", valid(base(), jwt.SigningMethodRS256, "missing", key)},
		{"wrong algorithm", valid(base(), jwt.SigningMethodHS256, "key-a", key)},
		{"oversized", strings.Repeat("a", maxTokenSize+1)},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := v.Verify(context.Background(), tc.token); err == nil || errors.Is(err, ErrUnavailable) {
				t.Fatalf("Verify error = %v, expected invalid-token error", err)
			}
		})
	}
	missing := base()
	missing.NotBefore = nil
	if _, err := v.Verify(context.Background(), valid(missing, jwt.SigningMethodRS256, "key-a", key)); err == nil {
		t.Fatal("missing nbf accepted")
	}
	missing = base()
	missing.IssuedAt = nil
	if _, err := v.Verify(context.Background(), valid(missing, jwt.SigningMethodRS256, "key-a", key)); err == nil {
		t.Fatal("missing iat accepted")
	}
}

func TestVerifierReportsJWKSOutageAndRefreshesRotatedKey(t *testing.T) {
	keyA, keyB := mustRSA(t), mustRSA(t)
	var current atomic.Value
	current.Store(jwksJSON("key-a", &keyA.PublicKey))
	var status atomic.Int32
	status.Store(http.StatusOK)
	var fetches atomic.Int32
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		fetches.Add(1)
		body := ""
		if int(status.Load()) == http.StatusOK {
			body = current.Load().(string)
		}
		return response(int(status.Load()), body), nil
	})
	client := &http.Client{Transport: transport}
	v := mustVerifier(t, "https://jwks.invalid/certs", map[string]string{"alice@example.test": "alice"}, client)
	now := time.Now()
	makeToken := func(k *rsa.PrivateKey, kid string) string {
		c := claims{Email: "alice@example.test", Type: "app", RegisteredClaims: registeredClaims(now.Add(-time.Minute), now.Add(-time.Minute), now.Add(time.Hour), "https://team.example.com", jwt.ClaimStrings{"aud-1"})}
		return signedToken(t, k, kid, jwt.SigningMethodRS256, c)
	}
	if _, err := v.Verify(context.Background(), makeToken(keyA, "key-a")); err != nil {
		t.Fatal(err)
	}
	current.Store(jwksJSON("key-b", &keyB.PublicKey))
	v.mu.Lock()
	v.lastRefresh = time.Time{}
	v.mu.Unlock()
	if _, err := v.Verify(context.Background(), makeToken(keyB, "key-b")); err != nil {
		t.Fatalf("rotated token: %v", err)
	}
	status.Store(http.StatusServiceUnavailable)
	v.mu.Lock()
	v.fetched = time.Time{}
	v.mu.Unlock()
	before := fetches.Load()
	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := v.Verify(context.Background(), makeToken(keyB, "key-b"))
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("concurrent outage error=%v, want ErrUnavailable", err)
		}
	}
	if got := fetches.Load(); got != before+1 {
		t.Fatalf("concurrent JWKS outage fetches=%d, want exactly one request", got-before)
	}
	if fetches.Load() < 3 {
		t.Fatalf("JWKS refreshes=%d, want rotation and outage fetches", fetches.Load())
	}
}

func mustRSA(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
func mustVerifier(t *testing.T, jwksURL string, users map[string]string, clients ...*http.Client) *Verifier {
	t.Helper()
	client := http.DefaultClient
	if len(clients) != 0 {
		client = clients[0]
	}
	v, err := newVerifier("https://team.example.com", "aud-1", jwksURL, users, client)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testClient(fn func() (*http.Response, error)) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return fn() })}
}
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}
func jwksJSON(kid string, key *rsa.PublicKey) string {
	e := big.NewInt(int64(key.E)).Bytes()
	doc := map[string]any{"keys": []any{map[string]string{"kid": kid, "kty": "RSA", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(e)}}}
	b, _ := json.Marshal(doc)
	return string(b)
}
func registeredClaims(iat, nbf, exp time.Time, issuer string, aud jwt.ClaimStrings) jwt.RegisteredClaims {
	return jwt.RegisteredClaims{Issuer: issuer, Audience: aud, IssuedAt: jwt.NewNumericDate(iat), NotBefore: jwt.NewNumericDate(nbf), ExpiresAt: jwt.NewNumericDate(exp)}
}
func signedToken(t *testing.T, key *rsa.PrivateKey, kid string, method jwt.SigningMethod, c claims) string {
	t.Helper()
	token := jwt.NewWithClaims(method, c)
	if kid != "" {
		token.Header["kid"] = kid
	}
	if _, ok := token.Header["typ"]; !ok {
		token.Header["typ"] = "JWT"
	}
	s, err := token.SignedString(key)
	if method == jwt.SigningMethodHS256 {
		s, err = token.SignedString([]byte("not-a-rsa-key"))
	}
	if err != nil {
		t.Fatal(err)
	}
	return s
}
