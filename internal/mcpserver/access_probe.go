package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/mail"
	"net/url"
	"path"
	"regexp"
	"strings"

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
		if len(assertions) != 1 || assertions[0] == "" || strings.TrimSpace(assertions[0]) != assertions[0] || strings.Contains(assertions[0], ",") {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		if !probeCredentialHeadersValid(r) {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		identity, err := verifier.Verify(r.Context(), assertions[0])
		if err != nil {
			if errors.Is(err, accessauth.ErrUnavailable) {
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		if identity == nil || identity.Email == "" || identity.Username == "" || cfg.UserMap[identity.Email] != identity.Username {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		log.Print("MCP Access assertion probe verified")
		w.WriteHeader(http.StatusNoContent)
	})
}

func probeCredentialHeadersValid(r *http.Request) bool {
	for _, name := range []string{"Authorization", "X-API-Key"} {
		values := r.Header.Values(name)
		if len(values) > 1 || (name == "X-API-Key" && len(values) != 0) {
			return false
		}
		if name == "Authorization" && len(values) == 1 && !validOpaqueAccessBearer(values[0]) {
			return false
		}
	}
	// Ancillary identity headers may be added by Access. Only the signed
	// assertion determines identity; these unsigned values are ignored.
	for key := range r.URL.Query() {
		switch strings.ToLower(key) {
		case "access_token", "token", "api_key", "apikey", "key", "authorization", "assertion", "cf-access-jwt-assertion":
			return false
		}
	}
	// Cookies are ambient browser state. This endpoint never reads them as identity.
	return true
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
