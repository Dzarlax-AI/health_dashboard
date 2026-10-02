package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"health-receiver/internal/ctxdb"
	"health-receiver/internal/oauth"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

var sumMetrics = storage.SumMetrics

// DBResolver resolves a tenant DB from an authenticated credential or username.
type DBResolver func(ctx context.Context, key string) (*storage.DB, string, bool, bool)

// Register mounts MCP Streamable HTTP at /mcp.
func Register(mux *http.ServeMux, mgr *tenants.Manager, cfg *oauth.Config) error {
	apiKeys := DBResolver(mgr.DBForAPIKey)
	s := buildServer(apiKeys)
	// OAuth requests are independent and authenticated on every HTTP call.
	// The SDK's default generated session IDs are not bound to a principal;
	// stateless transport avoids reusing another tenant's session state.
	var h *server.StreamableHTTPServer
	if cfg != nil {
		h = server.NewStreamableHTTPServer(s, server.WithStateLess(true))
	} else {
		h = server.NewStreamableHTTPServer(s)
	}
	var verifier oauth.TokenVerifier
	if cfg != nil {
		if err := cfg.Validate(); err != nil {
			return err
		}
		v, err := oauth.NewJWTVerifier(oauth.JWTVerifierConfig{
			Issuer: cfg.Issuer, Audience: cfg.Resource, JWKSURL: cfg.JWKSURL, Scopes: []string{cfg.ReadScope},
		})
		if err != nil {
			return err
		}
		verifier = v
		metadata := oauth.NewProtectedResourceMetadata(oauth.MetadataConfig{
			Resource: cfg.Resource, AuthorizationServers: []string{cfg.Issuer}, Scopes: []string{cfg.ReadScope},
		})
		mux.Handle(oauth.MetadataPath(cfg.Resource), oauth.MetadataHandler(metadata))
	}
	protected := withAuth(h, apiKeys, DBResolver(mgr.DBForUsername), verifier, cfg)
	mux.Handle("/mcp", protected)
	mux.Handle("/mcp/", protected)
	return nil
}

func withAuth(next http.Handler, apiKeys, usernames DBResolver, verifier oauth.TokenVerifier, cfg *oauth.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bad := func(status int, code string) {
			if cfg != nil {
				challenge := oauth.Challenge(cfg.Resource, []string{cfg.ReadScope})
				if code != "" {
					challenge += fmt.Sprintf(`, error="%s"`, code)
				}
				w.Header().Set("WWW-Authenticate", challenge)
			}
			http.Error(w, http.StatusText(status), status)
		}
		if _, supplied := r.URL.Query()["access_token"]; supplied {
			bad(http.StatusBadRequest, "invalid_request")
			return
		}
		auths, keys := r.Header.Values("Authorization"), r.Header.Values("X-API-Key")
		if len(auths) > 1 || len(keys) > 1 || (len(auths) != 0 && len(keys) != 0) {
			bad(http.StatusBadRequest, "invalid_request")
			return
		}
		var db *storage.DB
		var schema string
		var ok bool
		switch {
		case len(keys) == 1:
			if keys[0] == "" || strings.TrimSpace(keys[0]) != keys[0] || strings.Contains(keys[0], ",") {
				bad(http.StatusBadRequest, "invalid_request")
				return
			}
			db, schema, _, ok = apiKeys(r.Context(), keys[0])
		case len(auths) == 1:
			parts := strings.Split(auths[0], " ")
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" || strings.TrimSpace(parts[1]) != parts[1] || strings.Contains(parts[1], ",") {
				bad(http.StatusBadRequest, "invalid_request")
				return
			}
			token := parts[1]
			if cfg != nil && strings.Count(token, ".") == 2 {
				claims, err := verifier.Verify(r.Context(), token)
				if err != nil {
					if errors.Is(err, oauth.ErrInsufficientScope) {
						bad(http.StatusForbidden, "insufficient_scope")
					} else {
						bad(http.StatusUnauthorized, "invalid_token")
					}
					return
				}
				username, mapped := cfg.SubjectMap[claims.Subject]
				if !mapped {
					bad(http.StatusForbidden, "insufficient_scope")
					return
				}
				db, schema, _, ok = usernames(r.Context(), username)
			} else {
				db, schema, _, ok = apiKeys(r.Context(), token)
			}
		default:
			bad(http.StatusUnauthorized, "")
			return
		}
		if !ok || db == nil || schema == "" {
			bad(http.StatusUnauthorized, "invalid_token")
			return
		}
		next.ServeHTTP(w, r.WithContext(ctxdb.WithDB(r.Context(), db, schema)))
	})
}

func buildServer(resolve DBResolver) *server.MCPServer {
	s := server.NewMCPServer("health-mcp", "1.0.0",
		server.WithToolCapabilities(true),
	)
	registerMetricTools(s, resolve)
	registerAnalysisTools(s, resolve)
	registerWorkoutTools(s, resolve)
	registerSleepBalanceTool(s)
	return s
}

func jsonResult(v any) (*mcp.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}
