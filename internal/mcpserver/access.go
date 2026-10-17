package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/mark3labs/mcp-go/server"

	"health-receiver/internal/accessauth"
	"health-receiver/internal/ctxdb"
	"health-receiver/internal/tenants"
)

const accessAssertionHeader = "Cf-Access-Jwt-Assertion"

var accessUsernamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)

// AccessConfig is the opt-in, independent Cloudflare Access profile for MCP.
type AccessConfig struct {
	Enabled  bool
	Issuer   string
	Audience string
	UserMap  map[string]string
}

// AccessAssertionVerifier validates a Cloudflare-signed application assertion.
type AccessAssertionVerifier interface {
	Verify(context.Context, string) (*accessauth.Identity, error)
}

type accessVerifier struct{ verifier *accessauth.Verifier }

func (v accessVerifier) Verify(ctx context.Context, assertion string) (*accessauth.Identity, error) {
	return v.verifier.Verify(ctx, assertion)
}

// ParseAccessConfig reads the opt-in profile without coupling it to browser auth.
func ParseAccessConfig(lookup func(string) (string, bool)) (*AccessConfig, error) {
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
		return &AccessConfig{}, nil
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
	cfg := &AccessConfig{Enabled: true, Issuer: issuer, Audience: audience, UserMap: users}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *AccessConfig) validate() error {
	if c == nil || !c.Enabled {
		return errors.New("enabled MCP Access config is required")
	}
	if err := validateAccessOrigin("MCP_ACCESS_ISSUER", c.Issuer); err != nil {
		return err
	}
	if c.Audience == "" || strings.TrimSpace(c.Audience) != c.Audience || strings.Contains(c.Audience, ",") {
		return errors.New("MCP_ACCESS_AUD must be one non-empty exact audience value")
	}
	if len(c.UserMap) == 0 {
		return errors.New("MCP_ACCESS_USER_MAP must contain at least one user")
	}
	for email, username := range c.UserMap {
		address, err := mail.ParseAddress(email)
		if err != nil || address.Address != email || username == "" || strings.TrimSpace(username) != username || !accessUsernamePattern.MatchString(username) {
			return errors.New("MCP_ACCESS_USER_MAP entries must contain exact email and Health username values")
		}
	}
	return nil
}

func validateAccessOrigin(name, value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Opaque != "" {
		return fmt.Errorf("%s must be an exact HTTPS origin", name)
	}
	if strings.TrimSpace(value) != value || u.String() != value {
		return fmt.Errorf("%s must use canonical origin spelling", name)
	}
	return nil
}

// NewAccessVerifier uses membership semantics for the MCP audience only. The
// browser Access verifier retains its exclusive-audience policy.
func NewAccessVerifier(cfg *AccessConfig) (AccessAssertionVerifier, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	verifier, err := accessauth.NewVerifierWithAudienceMembership(&accessauth.Config{
		Issuer: cfg.Issuer, Audience: cfg.Audience, Origin: cfg.Issuer, UserMap: cfg.UserMap,
	})
	if err != nil {
		return nil, err
	}
	return accessVerifier{verifier: verifier}, nil
}

// NewAccessHandler serves the stateless MCP transport behind assertion auth.
func NewAccessHandler(transport http.Handler, usernames DBResolver, verifier AccessAssertionVerifier, cfg *AccessConfig) http.Handler {
	if cfg == nil || !cfg.Enabled {
		return http.NotFoundHandler()
	}
	return withAccessAuth(transport, usernames, verifier, cfg)
}

// withAccessAuth authenticates each HTTP request independently and binds only
// the mapped Health user's current database to the tool request context.
func withAccessAuth(next http.Handler, usernames DBResolver, verifier AccessAssertionVerifier, cfg *AccessConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path != "/mcp/oauth" || r.URL.EscapedPath() != "/mcp/oauth" {
			http.NotFound(w, r)
			return
		}
		// This endpoint is server-to-server; any browser Origin is rejected.
		// Missing Origin is allowed for native MCP clients.
		if hasOriginHeader(r.Header) {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		if verifier == nil || usernames == nil || cfg == nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		assertions := r.Header.Values(accessAssertionHeader)
		if len(assertions) != 1 || assertions[0] == "" || strings.TrimSpace(assertions[0]) != assertions[0] || strings.Contains(assertions[0], ",") || len(assertions[0]) > 16*1024 {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		if accessCredentialConflict(r) {
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
		db, schema, _, ok := usernames(r.Context(), identity.Username)
		if !ok || db == nil || schema == "" {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(ctxdb.WithDB(r.Context(), db, schema)))
	})
}

func hasOriginHeader(header http.Header) bool {
	for name := range header {
		if strings.EqualFold(name, "Origin") {
			return true
		}
	}
	return false
}

func accessCredentialConflict(r *http.Request) bool {
	if len(r.Header.Values("X-API-Key")) > 0 {
		return true
	}
	auth := r.Header.Values("Authorization")
	if len(auth) > 1 || (len(auth) == 1 && !validOpaqueAccessBearer(auth[0])) {
		return true
	}
	for key := range r.URL.Query() {
		switch strings.ToLower(key) {
		case "access_token", "token", "api_key", "apikey", "key", "authorization", "assertion", "cf-access-jwt-assertion":
			return true
		}
	}
	return false
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

// RegisterAccess mounts /mcp/oauth as its own stateless MCP transport. It
// reserves the exact path even while disabled so it cannot fall through /mcp/.
func RegisterAccess(mux *http.ServeMux, mgr *tenants.Manager, cfg *AccessConfig, verifier AccessAssertionVerifier) {
	var handler http.Handler = http.NotFoundHandler()
	if cfg != nil && cfg.Enabled {
		transport := newStatelessTransport(buildServer(nil))
		var usernames DBResolver
		if mgr != nil {
			usernames = mgr.DBForUsername
		}
		handler = NewAccessHandler(transport, usernames, verifier, cfg)
	}
	mux.Handle("/mcp/oauth", handler)
	mux.Handle("/mcp/oauth/", http.NotFoundHandler())
}

func newStatelessTransport(s *server.MCPServer) *server.StreamableHTTPServer {
	return server.NewStreamableHTTPServer(s, server.WithStateLess(true))
}

// ProtectAccessPathEquivalents prevents ServeMux path cleaning or escaped
// segments from redirecting invalid spellings into the canonical route.
func ProtectAccessPathEquivalents(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reserved, canonical := accessPath(r)
		if reserved && !canonical {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func accessPath(r *http.Request) (reserved, canonical bool) {
	const endpoint = "/mcp/oauth"
	if r.URL.Path == endpoint && r.URL.EscapedPath() == endpoint {
		return true, true
	}
	for _, initial := range []string{r.URL.Path, r.URL.EscapedPath()} {
		candidate := initial
		for depth := 0; depth < 5; depth++ {
			clean := path.Clean(candidate)
			lower := strings.ToLower(clean)
			if strings.EqualFold(clean, endpoint) || strings.HasPrefix(lower, endpoint+"/") {
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
		if strings.Contains(candidate, "%") && strings.HasPrefix(strings.ToLower(path.Clean(candidate)), "/mcp/") {
			return true, false
		}
	}
	return false, false
}
