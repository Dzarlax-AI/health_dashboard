package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"health-receiver/internal/ctxdb"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

var sumMetrics = storage.SumMetrics

// DBResolver resolves a tenant DB from an API key.
type DBResolver func(ctx context.Context, key string) (*storage.DB, string, bool, bool)

// Register mounts MCP Streamable HTTP at /mcp.
func Register(mux *http.ServeMux, mgr *tenants.Manager, _ string) {
	resolver := DBResolver(mgr.DBForAPIKey)
	s := buildServer(resolver)
	h := server.NewStreamableHTTPServer(s)
	protected := withAPIKey(h, resolver)
	mux.Handle("/mcp", protected)
	mux.Handle("/mcp/", protected)
}

func withAPIKey(next http.Handler, resolve DBResolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		key := strings.TrimPrefix(auth, "Bearer ")
		if key == "" {
			key = r.Header.Get("X-API-Key")
		}
		db, schema, _, ok := resolve(r.Context(), key)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
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
	return s
}

func jsonResult(v any) (*mcp.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}
