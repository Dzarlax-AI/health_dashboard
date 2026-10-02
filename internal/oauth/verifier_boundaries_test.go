package oauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestJWTVerifierRejectsMissingExpiryUnsignedAndFutureTokens(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewJWTVerifier(JWTVerifierConfig{Issuer: "https://auth.example.test/", Audience: "https://health.example.test/mcp", JWKSURL: "https://auth.example.test/jwks", Scopes: []string{"health:read"}})
	if err != nil {
		t.Fatal(err)
	}
	v.keys = map[string]any{"synthetic": &key.PublicKey}
	v.fetched = time.Now()
	for _, tc := range []struct {
		name       string
		method     jwt.SigningMethod
		signingKey any
		expiry     bool
		future     bool
	}{
		{"missing expiry", jwt.SigningMethodRS256, key, false, false},
		{"unsigned", jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, true, false},
		{"algorithm confusion", jwt.SigningMethodHS256, []byte("synthetic-hmac-only"), true, false},
		{"not yet valid", jwt.SigningMethodRS256, key, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := jwt.MapClaims{"iss": v.issuer, "aud": v.audience, "sub": "synthetic-subject", "scope": "health:read"}
			if tc.expiry {
				claims["exp"] = time.Now().Add(time.Hour).Unix()
			}
			if tc.future {
				claims["nbf"] = time.Now().Add(time.Hour).Unix()
			}
			token := jwt.NewWithClaims(tc.method, claims)
			token.Header["kid"] = "synthetic"
			signed, err := token.SignedString(tc.signingKey)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := v.Verify(context.Background(), signed); err == nil {
				t.Fatal("invalid token accepted")
			}
		})
	}
}
