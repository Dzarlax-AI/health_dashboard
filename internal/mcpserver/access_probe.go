package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/mail"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"health-receiver/internal/accessauth"
)

var accessUsernamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)

// AccessProbeConfig is independent of the browser UI and MCP OAuth profiles.
type AccessProbeConfig struct {
	Enabled  bool
	Issuer   string
	Audience string
	UserMap  map[string]string
}

// ParseAccessProbeConfig reads an opt-in profile for the Cloudflare origin
// assertion probe. Empty sample values behave like unset values.
func ParseAccessProbeConfig(lookup func(string) (string, bool)) (*AccessProbeConfig, error) {
	values := map[string]string{}
	for _, key := range []string{"MCP_ACCESS_ENABLED", "MCP_ACCESS_ISSUER", "MCP_ACCESS_AUD", "MCP_ACCESS_USER_MAP"} {
		value, present := lookup(key)
		if present && value != "" {
			if strings.TrimSpace(value) != value {
				return nil, fmt.Errorf("%s must not contain surrounding whitespace", key)
			}
			values[key] = value
		}
	}

	enabled := false
	if raw, ok := values["MCP_ACCESS_ENABLED"]; ok {
		switch raw {
		case "true":
			enabled = true
		case "false":
		default:
			return nil, errors.New("MCP_ACCESS_ENABLED must be exactly true or false")
		}
	}
	configured := values["MCP_ACCESS_ISSUER"] != "" || values["MCP_ACCESS_AUD"] != "" || values["MCP_ACCESS_USER_MAP"] != ""
	if !enabled && configured {
		return nil, errors.New("MCP_ACCESS_ENABLED=true is required when MCP_ACCESS settings are configured")
	}
	if !enabled {
		return &AccessProbeConfig{}, nil
	}

	issuer, issuerOK := values["MCP_ACCESS_ISSUER"]
	audience, audienceOK := values["MCP_ACCESS_AUD"]
	mapJSON, mapOK := values["MCP_ACCESS_USER_MAP"]
	if !issuerOK || !audienceOK || !mapOK {
		return nil, errors.New("MCP_ACCESS_ISSUER, MCP_ACCESS_AUD, and MCP_ACCESS_USER_MAP are required when MCP_ACCESS_ENABLED=true")
	}
	users, err := accessauth.ParseUserMap(mapJSON)
	if err != nil {
		return nil, fmt.Errorf("invalid MCP_ACCESS_USER_MAP: %w", err)
	}
	for email, username := range users {
		address, err := mail.ParseAddress(email)
		if err != nil || address.Address != email || strings.TrimSpace(email) != email {
			return nil, errors.New("MCP_ACCESS_USER_MAP keys must be exact email addresses")
		}
		if !accessUsernamePattern.MatchString(username) {
			return nil, errors.New("MCP_ACCESS_USER_MAP values must be existing Health usernames")
		}
	}
	cfg := &AccessProbeConfig{Enabled: true, Issuer: issuer, Audience: audience, UserMap: users}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *AccessProbeConfig) validate() error {
	if c == nil || !c.Enabled {
		return errors.New("enabled MCP Access probe config is required")
	}
	u, err := url.Parse(c.Issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.String() != c.Issuer {
		return errors.New("MCP_ACCESS_ISSUER must be an exact HTTPS origin")
	}
	if c.Audience == "" || strings.TrimSpace(c.Audience) != c.Audience || strings.ContainsAny(c.Audience, ", \t\r\n") {
		return errors.New("MCP_ACCESS_AUD must be one non-empty exact audience value")
	}
	if len(c.UserMap) == 0 {
		return errors.New("MCP_ACCESS_USER_MAP must contain at least one user")
	}
	for email, username := range c.UserMap {
		if strings.TrimSpace(email) != email || strings.TrimSpace(username) != username || email == "" || !accessUsernamePattern.MatchString(username) {
			return errors.New("MCP_ACCESS_USER_MAP entries must contain exact emails and Health usernames")
		}
	}
	return nil
}

func (c *AccessProbeConfig) verifierConfig() *accessauth.Config {
	return &accessauth.Config{Issuer: c.Issuer, Audience: c.Audience, UserMap: c.UserMap, Origin: c.Issuer}
}

// AccessAssertionVerifier is injectable so handler tests can focus on request
// boundaries without reimplementing JWT signature verification.
type AccessAssertionVerifier interface {
	Verify(context.Context, string) (*accessauth.Identity, error)
}

type accessProbeVerifier struct{ verifier *accessauth.Verifier }

type probeRejectReason string

const (
	probeRejectAssertionCount      probeRejectReason = "assertion_count"
	probeRejectAssertionFormat     probeRejectReason = "assertion_format"
	probeRejectAuthorizationCount  probeRejectReason = "authorization_count"
	probeRejectAuthorizationFormat probeRejectReason = "authorization_format"
	probeRejectAPIKey              probeRejectReason = "api_key_present"
	probeRejectQueryCredential     probeRejectReason = "query_credential"
	probeRejectVerifierUnavailable probeRejectReason = "verifier_unavailable"
	probeRejectAssertionRejected   probeRejectReason = "assertion_rejected"
	probeRejectExclusiveAudience   probeRejectReason = "assertion_exclusive_audience"
	probeRejectIdentityMismatch    probeRejectReason = "identity_mismatch"
)

type unverifiedClaimFlags struct {
	Parsed                   bool
	RS256                    bool
	KIDPresent               bool
	IssuerMatch              bool
	AudienceMatch            bool
	AudienceCount            int
	AudienceUniqueCount      int
	AudienceContainsExpected bool
	AppType                  bool
	IATPresent               bool
	NBPresent                bool
	EXPPresent               bool
	IATValid                 bool
	NBValid                  bool
	EXPValid                 bool
	EmailMapped              bool
}

const probeRejectionLogInterval = 5 * time.Second

type probeRejectionLogThrottle struct {
	mu   sync.Mutex
	last map[probeRejectReason]time.Time
	now  func() time.Time
}

func newProbeRejectionLogThrottle() *probeRejectionLogThrottle {
	return newProbeRejectionLogThrottleWithClock(time.Now)
}

func newProbeRejectionLogThrottleWithClock(now func() time.Time) *probeRejectionLogThrottle {
	return &probeRejectionLogThrottle{
		last: make(map[probeRejectReason]time.Time, 10),
		now:  now,
	}
}

func (t *probeRejectionLogThrottle) allow(reason probeRejectReason) bool {
	if t == nil || !knownProbeRejectReason(reason) {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if previous, ok := t.last[reason]; ok && now.Sub(previous) < probeRejectionLogInterval {
		return false
	}
	t.last[reason] = now
	return true
}

func knownProbeRejectReason(reason probeRejectReason) bool {
	switch reason {
	case probeRejectAssertionCount, probeRejectAssertionFormat, probeRejectAuthorizationCount,
		probeRejectAuthorizationFormat, probeRejectAPIKey, probeRejectQueryCredential,
		probeRejectVerifierUnavailable, probeRejectAssertionRejected, probeRejectExclusiveAudience, probeRejectIdentityMismatch:
		return true
	default:
		return false
	}
}

func (v accessProbeVerifier) Verify(ctx context.Context, token string) (*accessauth.Identity, error) {
	return v.verifier.Verify(ctx, token)
}

func NewAccessProbeVerifier(cfg *AccessProbeConfig) (AccessAssertionVerifier, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	verifier, err := accessauth.NewVerifier(cfg.verifierConfig())
	if err != nil {
		return nil, err
	}
	return accessProbeVerifier{verifier: verifier}, nil
}

// NewAccessProbeHandler returns a protocol-only assertion probe. A successful
// assertion receives 204; this handler never resolves a tenant or dispatches MCP.
func NewAccessProbeHandler(cfg *AccessProbeConfig, verifier AccessAssertionVerifier) http.Handler {
	if cfg == nil || !cfg.Enabled {
		return http.NotFoundHandler()
	}
	logThrottle := newProbeRejectionLogThrottle()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost || r.URL.Path != "/mcp/oauth" || r.URL.EscapedPath() != "/mcp/oauth" {
			http.NotFound(w, r)
			return
		}
		if verifier == nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		assertions := r.Header.Values("Cf-Access-Jwt-Assertion")
		if len(assertions) != 1 {
			logProbeRejection(logThrottle, probeRejectAssertionCount, r, cfg, "", false)
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		assertion := assertions[0]
		if assertion == "" || strings.TrimSpace(assertion) != assertion || strings.Contains(assertion, ",") || len(assertion) > 16*1024 {
			logProbeRejection(logThrottle, probeRejectAssertionFormat, r, cfg, assertion, false)
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		if reason := probeCredentialRejectReason(r); reason != "" {
			logProbeRejection(logThrottle, reason, r, cfg, assertion, false)
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		identity, err := verifier.Verify(r.Context(), assertion)
		if err != nil {
			if errors.Is(err, accessauth.ErrUnavailable) {
				logProbeRejection(logThrottle, probeRejectVerifierUnavailable, r, cfg, assertion, false)
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			if errors.Is(err, accessauth.ErrExclusiveAudience) {
				logProbeRejection(logThrottle, probeRejectExclusiveAudience, r, cfg, assertion, true)
				http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
				return
			}
			logProbeRejection(logThrottle, probeRejectAssertionRejected, r, cfg, assertion, true)
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		if identity == nil || identity.Email == "" || identity.Username == "" || cfg.UserMap[identity.Email] != identity.Username {
			logProbeRejection(logThrottle, probeRejectIdentityMismatch, r, cfg, assertion, false)
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		log.Print("MCP Access assertion probe verified")
		w.WriteHeader(http.StatusNoContent)
	})
}

func probeCredentialRejectReason(r *http.Request) probeRejectReason {
	auth := r.Header.Values("Authorization")
	if len(auth) > 1 {
		return probeRejectAuthorizationCount
	}
	if len(r.Header.Values("X-API-Key")) > 0 {
		return probeRejectAPIKey
	}
	if len(auth) == 1 && !validOpaqueAccessBearer(auth[0]) {
		return probeRejectAuthorizationFormat
	}
	if hasQueryCredential(r) {
		return probeRejectQueryCredential
	}
	return ""
}

func hasQueryCredential(r *http.Request) bool {
	for key := range r.URL.Query() {
		switch strings.ToLower(key) {
		case "access_token", "token", "api_key", "apikey", "key", "authorization", "assertion", "cf-access-jwt-assertion":
			return true
		}
	}
	return false
}

func logProbeRejection(throttle *probeRejectionLogThrottle, reason probeRejectReason, r *http.Request, cfg *AccessProbeConfig, assertion string, inspectClaims bool) {
	if !throttle.allow(reason) {
		return
	}
	assertions := r.Header.Values("Cf-Access-Jwt-Assertion")
	auths := r.Header.Values("Authorization")
	apiKeys := r.Header.Values("X-API-Key")
	assertionShape := compactJWTShape(assertion)
	authBearer, authOAuth, authShape, authJWTShape := false, false, false, false
	authColonCount := 0
	if len(auths) == 1 {
		authBearer = strings.HasPrefix(auths[0], "Bearer ")
		token := ""
		if authBearer && strings.Count(auths[0], " ") == 1 {
			token = strings.TrimPrefix(auths[0], "Bearer ")
		}
		authOAuth = strings.HasPrefix(token, "oauth:")
		authShape = validOpaqueAccessBearer(auths[0])
		authJWTShape = strings.Count(token, ".") == 2
		authColonCount = strings.Count(token, ":")
	}
	var claims unverifiedClaimFlags
	if inspectClaims {
		claims = inspectUnverifiedAccessAssertion(assertion, cfg)
	}
	log.Printf("MCP Access assertion probe rejected reason=%s assertion_count=%d assertion_nonempty=%t assertion_unpadded=%t assertion_no_comma=%t assertion_under_16k=%t assertion_compact_shape=%t authorization_count=%d authorization_bearer_prefix=%t authorization_oauth_prefix=%t authorization_shape=%t authorization_jwt_shape=%t authorization_colon_count=%d api_key_present=%t query_credential_present=%t claims_unverified_parsed=%t claim_rs256=%t claim_kid_present=%t claim_issuer_match=%t claim_audience_match=%t claim_audience_count=%d claim_audience_unique_count=%d claim_audience_contains_expected=%t claim_app_type=%t claim_iat_present=%t claim_nbf_present=%t claim_exp_present=%t claim_iat_time_valid=%t claim_nbf_time_valid=%t claim_exp_time_valid=%t claim_email_mapped=%t",
		reason, len(assertions), assertion != "", strings.TrimSpace(assertion) == assertion, !strings.Contains(assertion, ","), len(assertion) <= 16*1024, assertionShape,
		len(auths), authBearer, authOAuth, authShape, authJWTShape, authColonCount, len(apiKeys) > 0, hasQueryCredential(r), claims.Parsed, claims.RS256, claims.KIDPresent, claims.IssuerMatch, claims.AudienceMatch, claims.AudienceCount, claims.AudienceUniqueCount, claims.AudienceContainsExpected, claims.AppType, claims.IATPresent, claims.NBPresent, claims.EXPPresent, claims.IATValid, claims.NBValid, claims.EXPValid, claims.EmailMapped)
}

// inspectUnverifiedAccessAssertion produces diagnostic hints only. Its unsigned
// input never authorizes a request, and raw values must not be emitted.
func inspectUnverifiedAccessAssertion(assertion string, cfg *AccessProbeConfig) unverifiedClaimFlags {
	var flags unverifiedClaimFlags
	if len(assertion) == 0 || len(assertion) > 16*1024 || cfg == nil {
		return flags
	}
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return flags
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(headerBytes) > 8*1024 {
		return flags
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(claimsBytes) > 8*1024 {
		return flags
	}
	var header, payload map[string]json.RawMessage
	if json.Unmarshal(headerBytes, &header) != nil || header == nil || json.Unmarshal(claimsBytes, &payload) != nil || payload == nil {
		return flags
	}
	flags.Parsed = true
	flags.RS256 = jsonString(header["alg"]) == "RS256"
	flags.KIDPresent = jsonString(header["kid"]) != ""
	flags.IssuerMatch = jsonString(payload["iss"]) == cfg.Issuer
	flags.AudienceCount, flags.AudienceUniqueCount, flags.AudienceContainsExpected, flags.AudienceMatch = audienceShape(payload["aud"], cfg.Audience)
	flags.AppType = jsonString(payload["type"]) == "app"
	flags.EmailMapped = cfg.UserMap[jsonString(payload["email"])] != ""
	now := float64(time.Now().Unix())
	if raw, ok := payload["iat"]; ok {
		flags.IATPresent = true
		if value, valid := jsonNumericDate(raw); valid {
			flags.IATValid = value <= now
		}
	}
	if raw, ok := payload["nbf"]; ok {
		flags.NBPresent = true
		if value, valid := jsonNumericDate(raw); valid {
			flags.NBValid = value <= now
		}
	}
	if raw, ok := payload["exp"]; ok {
		flags.EXPPresent = true
		if value, valid := jsonNumericDate(raw); valid {
			flags.EXPValid = value > now
		}
	}
	return flags
}

func compactJWTShape(assertion string) bool {
	parts := strings.Split(assertion, ".")
	return len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != ""
}

func jsonString(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func audienceShape(raw json.RawMessage, expected string) (count, uniqueCount int, containsExpected, exact bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return 0, 0, false, false
	}
	if trimmed[0] == '"' {
		var one string
		if json.Unmarshal(raw, &one) != nil {
			return 0, 0, false, false
		}
		return 1, 1, one == expected, one == expected
	}
	if trimmed[0] != '[' {
		return 0, 0, false, false
	}
	var rawValues []json.RawMessage
	if json.Unmarshal(raw, &rawValues) != nil || rawValues == nil {
		return 0, 0, false, false
	}
	unique := make(map[string]struct{}, len(rawValues))
	for _, rawValue := range rawValues {
		var value string
		if len(rawValue) == 0 || rawValue[0] != '"' || json.Unmarshal(rawValue, &value) != nil {
			return 0, 0, false, false
		}
		unique[value] = struct{}{}
		if value == expected {
			containsExpected = true
		}
	}
	return len(rawValues), len(unique), containsExpected, len(rawValues) == 1 && containsExpected
}

func jsonNumericDate(raw json.RawMessage) (float64, bool) {
	var number json.Number
	if json.Unmarshal(raw, &number) != nil {
		return 0, false
	}
	value, err := strconv.ParseFloat(number.String(), 64)
	return value, err == nil && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validOpaqueAccessBearer(value string) bool {
	if !strings.HasPrefix(value, "Bearer ") || strings.Count(value, " ") != 1 {
		return false
	}
	token := strings.TrimPrefix(value, "Bearer ")
	if !strings.HasPrefix(token, "oauth:") {
		return false
	}
	opaque := strings.TrimPrefix(token, "oauth:")
	if opaque == "" || strings.Contains(opaque, ",") {
		return false
	}
	for _, r := range opaque {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("-._~+/=", r)) {
			return false
		}
	}
	return true
}

// ProtectAccessProbePathEquivalents prevents ServeMux path cleaning or escaped
// segments from redirecting an invalid spelling into the canonical probe route.
func ProtectAccessProbePathEquivalents(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reserved, canonical := accessProbePath(r)
		if reserved && !canonical {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func accessProbePath(r *http.Request) (reserved, canonical bool) {
	const probe = "/mcp/oauth"
	if r.URL.Path == probe && r.URL.EscapedPath() == probe {
		return true, true
	}
	candidates := []string{r.URL.Path, r.URL.EscapedPath()}
	for _, initial := range candidates {
		candidate := initial
		for depth := 0; depth < 5; depth++ {
			clean := path.Clean(candidate)
			if strings.EqualFold(clean, probe) || strings.HasPrefix(strings.ToLower(clean), probe+"/") {
				return true, false
			}
			next, err := url.PathUnescape(candidate)
			if err != nil {
				if strings.HasPrefix(strings.ToLower(candidate), "/mcp/") && strings.Contains(strings.ToLower(candidate), "oauth") {
					return true, false
				}
				break
			}
			if next == candidate {
				break
			}
			candidate = next
		}
		// Bound decoding work, but never hand unresolved deeply encoded MCP
		// paths to the native subtree. Ordinary native paths are unaffected.
		if strings.Contains(candidate, "%") && strings.HasPrefix(strings.ToLower(path.Clean(candidate)), "/mcp/") {
			return true, false
		}
	}
	return false, false
}
