package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"health-receiver/internal/ctxdb"
	"health-receiver/internal/oauth"
	"health-receiver/internal/storage"
)

type tokenVerifierFunc func(context.Context, string) (*oauth.Claims, error)

func (f tokenVerifierFunc) Verify(ctx context.Context, token string) (*oauth.Claims, error) {
	return f(ctx, token)
}

func TestAuthBindsVerifiedSubjectToTenant(t *testing.T) {
	aliceDB, bobDB := &storage.DB{}, &storage.DB{}
	apiKeys := DBResolver(func(_ context.Context, key string) (*storage.DB, string, bool, bool) {
		if key == "legacy-key" {
			return aliceDB, "health_alice", false, true
		}
		return nil, "", false, false
	})
	usernames := DBResolver(func(_ context.Context, username string) (*storage.DB, string, bool, bool) {
		switch username {
		case "alice":
			return aliceDB, "health_alice", false, true
		case "bob":
			return bobDB, "health_bob", false, true
		default:
			return nil, "", false, false
		}
	})
	verifier := tokenVerifierFunc(func(_ context.Context, token string) (*oauth.Claims, error) {
		switch token {
		case "alice.jwt.sig":
			return &oauth.Claims{Subject: "subject-alice", Scopes: []string{"health:read"}}, nil
		case "bob.jwt.sig":
			return &oauth.Claims{Subject: "subject-bob", Scopes: []string{"health:read"}}, nil
		case "unmapped.jwt.sig":
			return &oauth.Claims{Subject: "unknown", Scopes: []string{"health:read"}}, nil
		case "no-scope.jwt.sig":
			return nil, oauth.ErrInsufficientScope
		default:
			return nil, errors.New("invalid token")
		}
	})
	cfg := &oauth.Config{Issuer: "https://auth.example.test", JWKSURL: "https://auth.example.test/jwks", Resource: "https://health.example.test/mcp", ReadScope: "health:read", SubjectMap: map[string]string{"subject-alice": "alice", "subject-bob": "bob"}}
	h := withAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ctxdb.FromContext(r.Context()) == aliceDB {
			_, _ = w.Write([]byte(ctxdb.SchemaFromContext(r.Context())))
		} else if ctxdb.FromContext(r.Context()) == bobDB {
			_, _ = w.Write([]byte(ctxdb.SchemaFromContext(r.Context())))
		} else {
			http.Error(w, "missing tenant", http.StatusInternalServerError)
		}
	}), apiKeys, usernames, verifier, cfg)
	tests := []struct {
		name, auth, key, wantBody string
		wantStatus                int
	}{
		{"alice OAuth", "Bearer alice.jwt.sig", "", "health_alice", 200},
		{"bob OAuth", "Bearer bob.jwt.sig", "", "health_bob", 200},
		{"unmapped subject", "Bearer unmapped.jwt.sig", "", "", 403},
		{"insufficient scope", "Bearer no-scope.jwt.sig", "", "", 403},
		{"invalid OAuth", "Bearer invalid.jwt.sig", "", "", 401},
		{"legacy bearer", "Bearer legacy-key", "", "health_alice", 200},
		{"legacy header", "", "legacy-key", "health_alice", 200},
		{"mixed credentials", "Bearer invalid.jwt.sig", "legacy-key", "", 400},
		{"missing", "", "", "", 401},
		{"malformed bearer", "Bearer  alice.jwt.sig", "", "", 400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			if tc.key != "" {
				req.Header.Set("X-API-Key", tc.key)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d", w.Code, tc.wantStatus)
			}
			if tc.wantBody != "" && w.Body.String() != tc.wantBody {
				t.Fatalf("body=%q want=%q", w.Body.String(), tc.wantBody)
			}
			if tc.wantStatus == 401 && !strings.Contains(w.Header().Get("WWW-Authenticate"), `resource_metadata="https://health.example.test/.well-known/oauth-protected-resource/mcp"`) {
				t.Fatalf("missing metadata challenge: %q", w.Header().Get("WWW-Authenticate"))
			}
		})
	}
}

func TestDisabledOAuthPreservesAPIKey(t *testing.T) {
	db := &storage.DB{}
	resolve := DBResolver(func(_ context.Context, key string) (*storage.DB, string, bool, bool) {
		if key == "legacy-key" {
			return db, "health", true, true
		}
		return nil, "", false, false
	})
	h := withAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ctxdb.FromContext(r.Context()) != db {
			t.Fatal("wrong DB")
		}
		w.WriteHeader(http.StatusNoContent)
	}), resolve, nil, nil, nil)
	for _, header := range []string{"Authorization", "X-API-Key"} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if header == "Authorization" {
			req.Header.Set(header, "Bearer legacy-key")
		} else {
			req.Header.Set(header, "legacy-key")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusNoContent {
			t.Fatalf("%s status=%d", header, w.Code)
		}
	}
}

func TestStatelessMCPCallUsesCurrentAuthenticatedTenant(t *testing.T) {
	aliceDB, bobDB := &storage.DB{}, &storage.DB{}
	s := server.NewMCPServer("tenant-test", "1.0")
	s.AddTool(mcp.NewTool("tenant"), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(ctxdb.SchemaFromContext(ctx)), nil
	})
	transport := server.NewStreamableHTTPServer(s, server.WithStateLess(true))
	usernames := DBResolver(func(_ context.Context, username string) (*storage.DB, string, bool, bool) {
		if username == "alice" {
			return aliceDB, "health_alice", false, true
		}
		if username == "bob" {
			return bobDB, "health_bob", false, true
		}
		return nil, "", false, false
	})
	verifier := tokenVerifierFunc(func(_ context.Context, token string) (*oauth.Claims, error) {
		if token == "alice.jwt.sig" {
			return &oauth.Claims{Subject: "subject-alice"}, nil
		}
		if token == "bob.jwt.sig" {
			return &oauth.Claims{Subject: "subject-bob"}, nil
		}
		return nil, errors.New("invalid")
	})
	cfg := &oauth.Config{Resource: "https://health.example.test/mcp", ReadScope: "health:read", SubjectMap: map[string]string{"subject-alice": "alice", "subject-bob": "bob"}}
	h := withAuth(transport, nil, usernames, verifier, cfg)
	for _, tc := range []struct{ token, schema string }{{"alice.jwt.sig", "health_alice"}, {"bob.jwt.sig", "health_bob"}} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"tenant","arguments":{}}}`))
		req.Header.Set("Authorization", "Bearer "+tc.token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Session-Id", "same-client-supplied-session")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), tc.schema) {
			t.Fatalf("token=%q status=%d body=%q", tc.token, w.Code, w.Body.String())
		}
		other := "health_alice"
		if tc.schema == other {
			other = "health_bob"
		}
		if strings.Contains(w.Body.String(), other) {
			t.Fatalf("cross-tenant response: %q", w.Body.String())
		}
	}
}
