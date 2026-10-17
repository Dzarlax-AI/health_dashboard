package accessauth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const maxTokenSize = 16 * 1024

var (
	ErrUnavailable = errors.New("cloudflare access verifier unavailable")
)

type Identity struct {
	Email    string
	Username string
}

type claims struct {
	Email string `json:"email"`
	Type  string `json:"type"`
	jwt.RegisteredClaims
}

type Verifier struct {
	issuer, audience, jwksURL string
	users                     map[string]string
	client                    *http.Client
	now                       func() time.Time
	cacheTTL                  time.Duration
	refreshInterval           time.Duration
	fetchBackoff              time.Duration
	allowAdditionalAudiences  bool

	mu          sync.Mutex
	keys        map[string]*rsa.PublicKey
	fetched     time.Time
	lastRefresh time.Time
	retryAfter  time.Time
}

func NewVerifier(cfg *Config) (*Verifier, error) {
	return newConfiguredVerifier(cfg, false)
}

// NewVerifierWithAudienceMembership accepts an application audience when it
// is one member of a signed token's audience claim. Browser authentication
// should continue using NewVerifier, which requires the application audience
// to be exclusive.
func NewVerifierWithAudienceMembership(cfg *Config) (*Verifier, error) {
	return newConfiguredVerifier(cfg, true)
}

func newConfiguredVerifier(cfg *Config, allowAdditionalAudiences bool) (*Verifier, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	issuer := strings.TrimSuffix(cfg.Issuer, "/")
	return newVerifierWithAudiencePolicy(issuer, cfg.Audience, issuer+"/cdn-cgi/access/certs", cfg.UserMap, &http.Client{Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, allowAdditionalAudiences)
}

// newVerifier is kept narrow so package tests can inject an in-memory JWKS server.
func newVerifier(issuer, audience, jwksURL string, users map[string]string, client *http.Client) (*Verifier, error) {
	return newVerifierWithAudiencePolicy(issuer, audience, jwksURL, users, client, false)
}

func newVerifierWithAudiencePolicy(issuer, audience, jwksURL string, users map[string]string, client *http.Client, allowAdditionalAudiences bool) (*Verifier, error) {
	cfg := &Config{Issuer: issuer, Audience: audience, Origin: issuer, UserMap: users}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("cloudflare access HTTP client is required")
	}
	copyUsers := make(map[string]string, len(users))
	for email, username := range users {
		copyUsers[email] = username
	}
	return &Verifier{issuer: issuer, audience: audience, jwksURL: jwksURL, users: copyUsers, client: client, now: time.Now, cacheTTL: 10 * time.Minute, refreshInterval: 30 * time.Second, fetchBackoff: 5 * time.Second, allowAdditionalAudiences: allowAdditionalAudiences}, nil
}

func (v *Verifier) Verify(ctx context.Context, raw string) (*Identity, error) {
	if raw == "" || len(raw) > maxTokenSize || strings.TrimSpace(raw) != raw || strings.Contains(raw, ",") {
		return nil, errors.New("invalid token size or format")
	}
	parsedClaims := &claims{}
	parsed, err := jwt.ParseWithClaims(raw, parsedClaims, func(token *jwt.Token) (any, error) {
		if token.Method.Alg() != jwt.SigningMethodRS256.Alg() {
			return nil, errors.New("unsupported signing method")
		}
		kid, ok := token.Header["kid"].(string)
		if !ok || kid == "" {
			return nil, errors.New("missing jwt kid")
		}
		return v.key(ctx, kid)
	}, jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}), jwt.WithIssuer(v.issuer), jwt.WithAudience(v.audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			return nil, ErrUnavailable
		}
		var unavailable *keyFetchError
		if errors.As(err, &unavailable) {
			return nil, ErrUnavailable
		}
		return nil, errors.New("invalid access token")
	}
	if !parsed.Valid || parsedClaims.IssuedAt == nil || parsedClaims.NotBefore == nil || parsedClaims.ExpiresAt == nil || parsedClaims.Type != "app" {
		return nil, errors.New("invalid access token claims")
	}
	if !v.allowAdditionalAudiences && (len(parsedClaims.Audience) != 1 || parsedClaims.Audience[0] != v.audience) {
		return nil, errors.New("invalid access token audience")
	}
	if parsedClaims.IssuedAt.Time.After(v.now()) {
		return nil, errors.New("future access token")
	}
	username, ok := v.users[parsedClaims.Email]
	if !ok || parsedClaims.Email == "" {
		return nil, errors.New("unmapped access identity")
	}
	return &Identity{Email: parsedClaims.Email, Username: username}, nil
}

type keyFetchError struct{ cause error }

func (e *keyFetchError) Error() string { return "cloudflare access key fetch failed" }
func (e *keyFetchError) Unwrap() error { return ErrUnavailable }

func (v *Verifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	keys, err := v.cachedKeys(ctx)
	if err != nil {
		return nil, err
	}
	if key := keys[kid]; key != nil {
		return key, nil
	}
	if err := v.refreshKeys(ctx); err != nil {
		return nil, err
	}
	keys, err = v.cachedKeys(ctx)
	if err != nil {
		return nil, err
	}
	if key := keys[kid]; key != nil {
		return key, nil
	}
	return nil, errors.New("unknown signing key")
}

func (v *Verifier) cachedKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.keys) != 0 && v.now().Sub(v.fetched) < v.cacheTTL {
		return v.keys, nil
	}
	if err := v.fetchKeysLocked(ctx); err != nil {
		return nil, err
	}
	return v.keys, nil
}

func (v *Verifier) refreshKeys(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.now().Sub(v.lastRefresh) < v.refreshInterval {
		return nil
	}
	v.lastRefresh = v.now()
	return v.fetchKeysLocked(ctx)
}

func (v *Verifier) fetchKeysLocked(ctx context.Context) error {
	if v.retryAfter.After(v.now()) {
		return &keyFetchError{cause: errors.New("JWKS retry backoff active")}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return v.fetchFailed(err)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return v.fetchFailed(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return v.fetchFailed(fmt.Errorf("status %d", resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return v.fetchFailed(err)
	}
	if len(body) > 1<<20 {
		return v.fetchFailed(errors.New("JWKS too large"))
	}
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return v.fetchFailed(err)
	}
	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, item := range doc.Keys {
		if item.Kty != "RSA" || item.Kid == "" || (item.Use != "" && item.Use != "sig") || (item.Alg != "" && item.Alg != "RS256") {
			continue
		}
		if _, exists := keys[item.Kid]; exists {
			return v.fetchFailed(errors.New("duplicate kid"))
		}
		key, err := item.publicKey()
		if err != nil {
			return v.fetchFailed(err)
		}
		keys[item.Kid] = key
	}
	if len(keys) == 0 {
		return v.fetchFailed(errors.New("no usable signing keys"))
	}
	v.keys, v.fetched = keys, v.now()
	v.retryAfter = time.Time{}
	return nil
}

func (v *Verifier) fetchFailed(cause error) error {
	v.retryAfter = v.now().Add(v.fetchBackoff)
	return &keyFetchError{cause: cause}
}

type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func (j jwk) publicKey() (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(j.N)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(j.E)
	if err != nil {
		return nil, err
	}
	e := 0
	for _, b := range eBytes {
		e = e<<8 | int(b)
	}
	if e < 3 || e%2 == 0 || len(eBytes) > 4 {
		return nil, errors.New("invalid jwk exponent")
	}
	n := new(big.Int).SetBytes(nBytes)
	if n.BitLen() < 2048 || n.BitLen() > 8192 {
		return nil, errors.New("invalid jwk modulus")
	}
	return &rsa.PublicKey{N: n, E: e}, nil
}
