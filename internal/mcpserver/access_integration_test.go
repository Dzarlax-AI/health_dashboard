package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"health-receiver/internal/accessauth"
	"health-receiver/internal/ctxdb"
	"health-receiver/internal/registry"
	"health-receiver/internal/tenants"
	"health-receiver/internal/testdb"
)

type integrationAccessVerifier map[string]*accessauth.Identity

func (v integrationAccessVerifier) Verify(_ context.Context, assertion string) (*accessauth.Identity, error) {
	if identity := v[assertion]; identity != nil {
		return identity, nil
	}
	return nil, errors.New("invalid fixture assertion")
}

// Real restricted PostgreSQL pools must follow the current signed identity on
// every HTTP call, even when a caller reuses a session and forges tool arguments.
func TestAccessMCPIsolatedTenantRoutingIntegration(t *testing.T) {
	dsn := testdb.DSN(t)
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	var disposable bool
	if err := root.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.health_test_metadata WHERE key='disposable_database' AND value='true')`).Scan(&disposable); err != nil || !disposable {
		t.Fatal("MCP DB fixtures require an explicitly marked disposable database")
	}
	adminDSN, registryDSN := os.Getenv("ADMIN_TEST_DSN"), os.Getenv("REGISTRY_TEST_DSN")
	if adminDSN == "" || registryDSN == "" {
		t.Fatal("MCP DB fixtures require ADMIN_TEST_DSN and REGISTRY_TEST_DSN")
	}
	base, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	base.User = nil
	query := base.Query()
	query.Del("user")
	query.Del("password")
	base.RawQuery = query.Encode()
	reg, err := registry.New(ctx, registryDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.Close)
	if err := reg.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	deriver := tenants.CredentialDeriver{Current: tenants.SecretVersion{Version: 1, Secret: []byte("disposable-mcp-fixture-master-secret-32-bytes")}}
	provisioner, err := tenants.NewProvisioner(ctx, adminDSN, base.String(), deriver, reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provisioner.Close)
	manager, err := tenants.NewIsolated(reg, base.String(), deriver)
	if err != nil {
		t.Fatal(err)
	}
	users := make([]*registry.User, 0, 2)
	t.Cleanup(func() {
		manager.Close()
		for _, user := range users {
			var owner string
			var markerTenant string
			if err := root.QueryRow(ctx, `SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname=$1`, user.SchemaName).Scan(&owner); err != nil || owner != user.DBRole {
				t.Errorf("refusing cleanup without fixture ownership: %v", err)
				continue
			}
			marker := pgx.Identifier{user.SchemaName, "__tenant_identity"}.Sanitize()
			if err := root.QueryRow(ctx, "SELECT tenant_id::text FROM "+marker+" WHERE singleton=true").Scan(&markerTenant); err != nil || markerTenant != user.TenantID.String() {
				t.Errorf("refusing cleanup without fixture identity: %v", err)
				continue
			}
			for _, statement := range []string{
				"DROP SCHEMA " + pgx.Identifier{user.SchemaName}.Sanitize() + " CASCADE",
				"DROP OWNED BY " + pgx.Identifier{user.DBRole}.Sanitize(),
				"DROP ROLE " + pgx.Identifier{user.DBRole}.Sanitize(),
			} {
				if _, err := root.Exec(ctx, statement); err != nil {
					t.Errorf("cleanup owned MCP fixture: %v", err)
				}
			}
			if err := reg.DeleteUser(ctx, user.Username); err != nil {
				t.Errorf("cleanup MCP registry fixture: %v", err)
			}
		}
	})
	for _, suffix := range []string{"a", "b"} {
		username := fmt.Sprintf("mcp%d%s", os.Getpid(), suffix)
		user, err := provisioner.CreateTenant(ctx, registry.CreateUserReq{Username: username, SchemaName: username + "_schema", Password: "disposable-mcp-fixture-password"})
		if err != nil {
			t.Fatal(err)
		}
		users = append(users, user)
		db, _, _, ok := manager.DBForUsername(ctx, username)
		if !ok || db == nil {
			t.Fatal("new fixture tenant did not resolve")
		}
		if err := db.SaveSettings(map[string]string{"mcp_fixture_marker": suffix}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &AccessConfig{Enabled: true, Issuer: "https://fixture.cloudflareaccess.com", Audience: "mcp-fixture", UserMap: map[string]string{"alice@example.test": users[0].Username, "bob@example.test": users[1].Username}}
	verifier := integrationAccessVerifier{
		"alice.jwt.fixture": {Email: "alice@example.test", Username: users[0].Username},
		"bob.jwt.fixture":   {Email: "bob@example.test", Username: users[1].Username},
	}
	mcpServer := server.NewMCPServer("mcp-db-fixture", "1.0")
	mcpServer.AddTool(mcp.NewTool("fixture_marker"), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return jsonResult(map[string]string{"marker": ctxdb.FromContext(ctx).GetSetting("mcp_fixture_marker", "MISSING"), "schema": ctxdb.SchemaFromContext(ctx)})
	})
	handler := withAccessAuth(newStatelessTransport(mcpServer), DBResolver(manager.DBForUsername), verifier, cfg)
	call := func(assertion string) *httptest.ResponseRecorder {
		t.Helper()
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fixture_marker","arguments":{"schema":"` + users[1].SchemaName + `","username":"` + users[1].Username + `","api_key":"` + users[1].APIKey + `"}}}`
		r := httptest.NewRequest(http.MethodPost, "/mcp/oauth", strings.NewReader(body))
		r.Header.Set("Cf-Access-Jwt-Assertion", assertion)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("Mcp-Session-Id", "same-untrusted-session")
		r.Header.Set("Cookie", "auth=forged; CF_Authorization=forged")
		r.Header.Set("Cf-Access-Authenticated-User-Email", "bob@example.test")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, index := range []int{0, 1, 0} {
		assertion, want := "alice.jwt.fixture", "a"
		if index == 1 {
			assertion, want = "bob.jwt.fixture", "b"
		}
		w := call(assertion)
		if w.Code != http.StatusOK || w.Header().Get("Mcp-Session-Id") != "" {
			t.Fatalf("stateless call status=%d", w.Code)
		}
		var response struct {
			Result struct {
				Content []struct{ Text string } `json:"content"`
			} `json:"result"`
		}
		payload := w.Body.String()
		if strings.HasPrefix(payload, "event:") || strings.HasPrefix(payload, "data:") {
			for _, line := range strings.Split(payload, "\n") {
				if strings.HasPrefix(line, "data:") {
					payload = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
					break
				}
			}
		}
		if err := json.Unmarshal([]byte(payload), &response); err != nil || len(response.Result.Content) != 1 {
			t.Fatal("invalid MCP fixture response")
		}
		var result map[string]string
		if err := json.Unmarshal([]byte(response.Result.Content[0].Text), &result); err != nil || result["marker"] != want || result["schema"] != users[index].SchemaName {
			t.Fatalf("MCP response did not follow current identity: %v", result)
		}
	}
	if _, err := root.Exec(ctx, `UPDATE health_registry.users SET provisioning_state='failed' WHERE username=$1`, users[0].Username); err != nil {
		t.Fatal(err)
	}
	if got := call("alice.jwt.fixture").Code; got != http.StatusUnauthorized && got != http.StatusForbidden {
		t.Fatalf("inactive tenant reused cached MCP pool: status=%d", got)
	}
}
