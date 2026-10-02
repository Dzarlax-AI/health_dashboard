package oauth

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
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestJWTVerifierSyntheticClaims(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwksBody, err := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kid": "test", "kty": "RSA", "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	issuer, audience, scope := "https://auth.example.test/issuer/", "https://health.example.test/mcp", "health:read"
	v, err := NewJWTVerifier(JWTVerifierConfig{Issuer: issuer, Audience: audience, JWKSURL: "https://auth.example.test/jwks", Scopes: []string{scope}})
	if err != nil {
		t.Fatal(err)
	}
	v.client = newNoRedirectHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(jwksBody))), Header: make(http.Header), Request: r}, nil
	})})
	sign := func(iss, aud, sc, sub string, exp time.Time) string {
		t.Helper()
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": iss, "aud": aud, "scope": sc, "sub": sub, "exp": exp.Unix(),
		})
		token.Header["kid"] = "test"
		text, err := token.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return text
	}
	validExpiry := time.Now().Add(time.Hour)
	valid := sign(issuer, audience, scope, "subject-1", validExpiry)
	claims, err := v.Verify(context.Background(), valid)
	if err != nil || claims.Subject != "subject-1" {
		t.Fatalf("valid token: claims=%+v err=%v", claims, err)
	}
	tests := []struct {
		name, token string
		wantScope   bool
	}{
		{"expired", sign(issuer, audience, scope, "subject-1", time.Now().Add(-time.Hour)), false},
		{"wrong issuer", sign("https://other.example.test", audience, scope, "subject-1", validExpiry), false},
		{"wrong audience", sign(issuer, "https://other.example.test/mcp", scope, "subject-1", validExpiry), false},
		{"wrong scope", sign(issuer, audience, "profile", "subject-1", validExpiry), true},
		{"empty subject", sign(issuer, audience, scope, "", validExpiry), false},
		{"invalid signature", valid[:len(valid)-2] + "ab", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := v.Verify(context.Background(), tc.token)
			if err == nil {
				t.Fatal("accepted invalid token")
			}
			if tc.wantScope && !errors.Is(err, ErrInsufficientScope) {
				t.Fatalf("scope failure = %v", err)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestResourceMetadataAndConfig(t *testing.T) {
	env := map[string]string{
		"MCP_OAUTH_ISSUER":      "https://auth.example.test/issuer/",
		"MCP_OAUTH_JWKS_URL":    "https://auth.example.test/jwks",
		"MCP_OAUTH_RESOURCE":    "https://health.example.test/mcp",
		"MCP_OAUTH_READ_SCOPE":  "health:read",
		"MCP_OAUTH_SUBJECT_MAP": `{"subject-1":"alice"}`,
	}
	get := func(k string) string { return env[k] }
	cfg, err := ConfigFromEnv(get)
	if err != nil || cfg.SubjectMap["subject-1"] != "alice" {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
	if got := MetadataURL(cfg.Resource); got != "https://health.example.test/.well-known/oauth-protected-resource/mcp" {
		t.Fatalf("metadata URL: %q", got)
	}
	if got := MetadataPath(cfg.Resource); got != "/.well-known/oauth-protected-resource/mcp" {
		t.Fatalf("metadata path: %q", got)
	}
	if challenge := Challenge(cfg.Resource, []string{cfg.ReadScope}); !strings.Contains(challenge, `resource_metadata="https://health.example.test/.well-known/oauth-protected-resource/mcp"`) {
		t.Fatalf("challenge: %q", challenge)
	}
	delete(env, "MCP_OAUTH_SUBJECT_MAP")
	if _, err := ConfigFromEnv(get); err == nil {
		t.Fatal("accepted partial config")
	}
	env["MCP_OAUTH_SUBJECT_MAP"] = `{"subject-1":"alice"}`
	env["MCP_OAUTH_RESOURCE"] = "http://health.example.test/mcp"
	if _, err := ConfigFromEnv(get); err == nil {
		t.Fatal("accepted non-HTTPS resource")
	}
}
