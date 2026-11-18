package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"health-receiver/internal/accessauth"
	"health-receiver/internal/api"
	"health-receiver/internal/ctxdb"
	"health-receiver/internal/health"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
	"health-receiver/internal/testdb"
	"health-receiver/internal/ui"
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
	// DSN may be empty when testdb uses libpq environment variables (as CI
	// does). Build the credential-free base from the effective connection.
	connection := root.Config().ConnConfig
	base := url.URL{Scheme: "postgres", Host: net.JoinHostPort(connection.Host, strconv.Itoa(int(connection.Port))), Path: "/" + connection.Database}
	query := base.Query()
	query.Set("sslmode", "disable")
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
	wakeDate, _ := time.Parse(time.DateOnly, "2026-10-26")
	for index, suffix := range []string{"a", "b"} {
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
		if err := db.SaveSettings(map[string]string{"mcp_fixture_marker": suffix, "sleep_duration_comparison_enabled": "true"}); err != nil {
			t.Fatal(err)
		}
		for offset := 0; offset <= 7; offset++ {
			day := wakeDate.AddDate(0, 0, -offset)
			night := health.CompletedNightSleep{
				WakeDate: day.Format(time.DateOnly), DurationHours: 7.5 + float64(index), Source: "synthetic-" + suffix,
				SourceEpoch: "fixture", AlgorithmVersion: "fixture", InputHash: day.Format(time.DateOnly),
				CaptureCompleteness: health.NightCaptureComplete, DurationAssessment: health.NightDurationPlausible,
				FinalizationState: health.NightFinalFinal, ClaimEligibility: health.NightClaimEligible, ObservedAt: day,
			}
			if offset == 0 {
				night.DurationHours = 6.8
				night.FinalizationState, night.ClaimEligibility = health.NightFinalProvisional, health.NightClaimIneligible
			}
			if err := db.SaveCompletedNightSleep(ctx, night, nil); err != nil {
				t.Fatal(err)
			}
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
	registerSleepComparisonTool(mcpServer)
	handler := withAccessAuth(newStatelessTransport(mcpServer), DBResolver(manager.DBForUsername), verifier, cfg)
	call := func(assertion, body string) *httptest.ResponseRecorder {
		t.Helper()
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
	markerBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fixture_marker","arguments":{"schema":"` + users[1].SchemaName + `","username":"` + users[1].Username + `","api_key":"` + users[1].APIKey + `"}}}`
	httpRoutes := http.NewServeMux()
	ui.New(manager, reg, false).Register(httpRoutes)
	comparisonBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_sleep_duration_comparison","arguments":{"date":"2026-10-26"}}}`

	for _, index := range []int{0, 1, 0} {
		assertion, want := "alice.jwt.fixture", "a"
		if index == 1 {
			assertion, want = "bob.jwt.fixture", "b"
		}
		w := call(assertion, markerBody)
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
		request := httptest.NewRequest(http.MethodGet, "/api/sleep/comparison?date=2026-10-26", nil)
		request.Header.Set("X-API-Key", users[index].APIKey)
		httpResponse := httptest.NewRecorder()
		httpRoutes.ServeHTTP(httpResponse, request)
		var rawHTTP api.SleepDurationComparisonResponse
		if httpResponse.Code != http.StatusOK || json.Unmarshal(httpResponse.Body.Bytes(), &rawHTTP) != nil || rawHTTP.Comparison == nil {
			t.Fatalf("protected comparison status=%d", httpResponse.Code)
		}
		var rawMCP struct {
			Result struct {
				Structured api.SleepDurationComparisonResponse `json:"structuredContent"`
			} `json:"result"`
		}
		comparisonResponse := call(assertion, comparisonBody)
		mcpPayload := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(comparisonResponse.Body.String()), "data:"))
		if i := strings.Index(mcpPayload, "\ndata:"); i >= 0 {
			mcpPayload = strings.TrimSpace(mcpPayload[i+len("\ndata:"):])
		}
		if err := json.Unmarshal([]byte(mcpPayload), &rawMCP); err != nil {
			t.Fatalf("decode comparison MCP: %v; %s", err, mcpPayload)
		}
		fact := rawHTTP.Comparison
		if !reflect.DeepEqual(fact, rawMCP.Result.Structured.Comparison) || fact.Source != "synthetic-"+want ||
			fact.Status != health.SleepComparisonAvailable || *fact.DeltaMinutes != -42-60*index || *fact.EligibleNights != 7 {
			t.Fatalf("HTTP/MCP fact mismatch for tenant %d: %+v / %+v", index, fact, rawMCP.Result.Structured.Comparison)
		}
		db, _, _, ok := manager.DBForUsername(ctx, users[index].Username)
		if !ok {
			t.Fatal("fixture tenant did not resolve for report preparation")
		}
		report, _, err := db.MorningReportEvidence(ctx, storage.AIConfig{}, "en", "2026-10-26",
			&health.BriefingResponse{Date: "2026-10-26"})
		if err != nil || !reflect.DeepEqual(fact, report.Comparison) {
			t.Fatalf("report facts differ from HTTP and MCP: %v", err)
		}
		page := httptest.NewRequest(http.MethodGet, "/sleep?date=2026-10-26", nil)
		page.Header.Set("X-API-Key", users[index].APIKey)
		rendered := httptest.NewRecorder()
		httpRoutes.ServeHTTP(rendered, page)
		if rendered.Code != http.StatusOK || !strings.Contains(rendered.Body.String(), fmt.Sprintf("%d min", -42-60*index)) {
			t.Fatalf("Go Sleep route missed comparison for tenant %d, status=%d", index, rendered.Code)
		}

		user := users[index]
		password, err := deriver.Derive(user.TenantID, user.DBRole, deriver.Current.Version)
		if err != nil {
			t.Fatal(err)
		}
		poolConfig, err := storage.RestrictedTenantPoolConfig(base.String(), user.DBRole, password, user.SchemaName)
		if err != nil {
			t.Fatal(err)
		}
		pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
		if err != nil {
			t.Fatal(err)
		}
		peerView := pgx.Identifier{users[1-index].SchemaName, "sleep_duration_reference_v1"}.Sanitize()
		err = pool.QueryRow(ctx, "SELECT count(*) FROM "+peerView).Scan(new(int))
		pool.Close()
		var denied *pgconn.PgError
		if !errors.As(err, &denied) || denied.Code != "42501" {
			t.Fatalf("cross-tenant view was not denied: %v", err)
		}

	}
	if _, err := root.Exec(ctx, `UPDATE health_registry.users SET provisioning_state='failed' WHERE username=$1`, users[0].Username); err != nil {
		t.Fatal(err)
	}
	if got := call("alice.jwt.fixture", markerBody).Code; got != http.StatusUnauthorized && got != http.StatusForbidden {
		t.Fatalf("inactive tenant reused cached MCP pool: status=%d", got)
	}
}
