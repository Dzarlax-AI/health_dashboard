package ui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/accessauth"
	"health-receiver/internal/ctxdb"
	"health-receiver/internal/registry"
	"health-receiver/internal/tenants"
	"health-receiver/internal/testdb"
)

// This integration test uses only a database explicitly marked disposable by
// the HEALTH_DB_TESTS contract. It creates short-lived schemas and registry
// users, then removes those exact records during cleanup.
func TestCloudflareGuardUsesActiveRegistryTenantRoleAndAPIKey(t *testing.T) {
	dsn := testdb.DSN(t)
	registryDSN := os.Getenv("REGISTRY_TEST_DSN")
	if registryDSN == "" {
		registryDSN = dsn
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	checkPool, err := testdb.NewPool(ctx, registryDSN, "")
	if err != nil {
		t.Fatal(err)
	}
	var disposable bool
	if err := checkPool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM health_test_metadata WHERE key='disposable_database' AND value='true')`).Scan(&disposable); err != nil || !disposable {
		t.Fatalf("Cloudflare auth integration requires disposable registry database marker (query error: %v)", err)
	}

	reg, err := registry.New(ctx, registryDSN)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.EnsureSchema(ctx); err != nil {
		reg.Close()
		t.Fatal(err)
	}
	db, err := testdb.NewPool(ctx, dsn, "")
	if err != nil {
		reg.Close()
		checkPool.Close()
		t.Fatal(err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	adminName, memberName := "cfadmin_"+suffix[len(suffix)-12:], "cfmember_"+suffix[len(suffix)-12:]
	adminSchema, memberSchema := "health_"+adminName, "health_"+memberName
	adminEmail, memberEmail := "cf-admin-"+suffix+"@example.test", "cf-member-"+suffix+"@example.test"
	for _, schema := range []string{adminSchema, memberSchema} {
		if err := testdb.CreateSchema(ctx, db, schema); err != nil {
			reg.Close()
			t.Fatal(err)
		}
	}
	var mgr *tenants.Manager
	t.Cleanup(func() {
		if mgr != nil {
			mgr.Close()
		}
		_, _ = checkPool.Exec(context.Background(), `DELETE FROM health_registry.users WHERE username = $1 OR username = $2`, adminName, memberName)
		_ = testdb.DropSchema(context.Background(), db, adminSchema)
		_ = testdb.DropSchema(context.Background(), db, memberSchema)
		reg.Close()
		db.Close()
		checkPool.Close()
	})
	admin, err := reg.CreateLegacyUser(ctx, registry.CreateUserReq{Username: adminName, SchemaName: adminSchema, Email: adminEmail, IsAdmin: true, InitialAPIKey: "api-" + suffix})
	if err != nil {
		t.Fatal(err)
	}
	member, err := reg.CreateLegacyUser(ctx, registry.CreateUserReq{Username: memberName, SchemaName: memberSchema, Email: memberEmail, IsAdmin: false, InitialAPIKey: "api-member-" + suffix})
	if err != nil {
		t.Fatal(err)
	}
	mgr = tenants.New(reg, dsn)
	adminDB, err := mgr.GetOrCreate(ctx, adminSchema)
	if err != nil {
		t.Fatal(err)
	}
	if err := adminDB.EnsureAuthSessionsTable(); err != nil {
		t.Fatal(err)
	}
	_, err = adminDB.CreateAuthSession(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	memberDB, err := mgr.GetOrCreate(ctx, memberSchema)
	if err != nil {
		t.Fatal(err)
	}
	if err := memberDB.EnsureAuthSessionsTable(); err != nil {
		t.Fatal(err)
	}
	oldSession, err := reg.CreateSession(ctx, adminName, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sessionSnapshot := func() string {
		t.Helper()
		var result string
		for _, schema := range []string{adminSchema, memberSchema} {
			var rows string
			query := "SELECT COALESCE(jsonb_agg(to_jsonb(s) ORDER BY id_hash)::text, '[]') FROM " + pgx.Identifier{schema, "auth_sessions"}.Sanitize() + " s"
			if err := db.QueryRow(ctx, query).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			result += rows
		}
		var registryRows string
		if err := checkPool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(s) ORDER BY id_hash)::text, '[]') FROM health_registry.sessions s WHERE username=$1 OR username=$2`, adminName, memberName).Scan(&registryRows); err != nil {
			t.Fatal(err)
		}
		result += registryRows
		return result
	}
	beforeSessions := sessionSnapshot()
	h := New(mgr, reg, false)
	verifier := &fakeAccessVerifier{identities: map[string]*accessauth.Identity{
		"jwt-admin":  {Email: adminEmail, Username: adminName},
		"jwt-member": {Email: memberEmail, Username: memberName},
	}}
	h.ConfigureCloudflareAccess(verifier, "https://health.example.test")
	var sessionsBefore int64
	if err := checkPool.QueryRow(ctx, `SELECT count(*) FROM health_registry.sessions`).Scan(&sessionsBefore); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, token, key, username, schema string
		admin                              bool
	}{
		{"admin JWT", "jwt-admin", "", adminName, adminSchema, true},
		{"member JWT overrides spoofed selector", "jwt-member", "", memberName, memberSchema, false},
		{"member native key", "", member.APIKey, memberName, memberSchema, false},
		{"switch back to admin JWT", "jwt-admin", "", adminName, adminSchema, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
			r.AddCookie(&http.Cookie{Name: "auth", Value: oldSession})
			if tc.token != "" {
				r.Header.Set("Cf-Access-Jwt-Assertion", tc.token)
			}
			if tc.token == "jwt-member" {
				r.AddCookie(&http.Cookie{Name: "auth", Value: "alice-old-session"})
			}
			if tc.key != "" {
				r.Header.Set("X-API-Key", tc.key)
			}
			if tc.name == "member JWT overrides spoofed selector" {
				r.Header.Set("X-authentik-username", adminName)
				r.Header.Set("X-authentik-email", adminEmail)
			}
			w := httptest.NewRecorder()
			h.guard(func(w http.ResponseWriter, r *http.Request) {
				if got := ctxdb.SchemaFromContext(r.Context()); got != tc.schema {
					t.Errorf("tenant schema=%q want %q", got, tc.schema)
				}
				if got := ctxdb.IsAdminFromContext(r.Context()); got != tc.admin {
					t.Errorf("admin=%v want %v", got, tc.admin)
				}
				w.WriteHeader(http.StatusNoContent)
			}).ServeHTTP(w, r)
			if w.Code != http.StatusNoContent {
				t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
			}
			if len(w.Header().Values("Set-Cookie")) != 0 {
				t.Fatalf("unexpected local auth session: %v", w.Header().Values("Set-Cookie"))
			}
			if len(w.Header().Values("Set-Cookie")) != 0 {
				t.Fatal("Cloudflare request created a local session cookie")
			}
		})
	}
	t.Run("member cannot use admin handler", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
		r.Header.Set("Cf-Access-Jwt-Assertion", "jwt-member")
		w := httptest.NewRecorder()
		h.adminGuard(func(http.ResponseWriter, *http.Request) { t.Fatal("member reached admin handler") }).ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status=%d want 403", w.Code)
		}
	})
	var sessionsAfter int64
	if err := checkPool.QueryRow(ctx, `SELECT count(*) FROM health_registry.sessions`).Scan(&sessionsAfter); err != nil {
		t.Fatal(err)
	}
	if sessionsAfter != sessionsBefore {
		t.Fatalf("Cloudflare request changed registry session count: before=%d after=%d", sessionsBefore, sessionsAfter)
	}
	before := verifier.calls
	r := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	r.Header.Set("X-API-Key", "invalid-"+suffix)
	r.Header.Set("Cf-Access-Jwt-Assertion", "jwt-admin")
	w := httptest.NewRecorder()
	h.guard(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid key fell through to valid JWT") }).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || verifier.calls != before {
		t.Fatalf("status=%d verifier calls=%d want %d", w.Code, verifier.calls, before)
	}

	// Verify that the configured usernames still refer to active registry rows.
	if _, err := reg.GetByUsername(ctx, admin.Username); err != nil {
		t.Fatalf("admin registry row not active: %v", err)
	}
	if _, err := checkPool.Exec(ctx, `UPDATE health_registry.users SET provisioning_state='failed' WHERE username=$1`, memberName); err != nil {
		t.Fatal(err)
	}
	for _, useKey := range []bool{false, true} {
		r := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
		if useKey {
			r.Header.Set("X-API-Key", member.APIKey)
		} else {
			r.Header.Set("Cf-Access-Jwt-Assertion", "jwt-member")
		}
		w := httptest.NewRecorder()
		h.guard(func(http.ResponseWriter, *http.Request) { t.Fatal("inactive user authorized") }).ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("inactive user key=%v status=%d", useKey, w.Code)
		}
	}
	if got := sessionSnapshot(); got != beforeSessions {
		t.Fatal("Cloudflare authentication mutated local session rows")
	}
}
