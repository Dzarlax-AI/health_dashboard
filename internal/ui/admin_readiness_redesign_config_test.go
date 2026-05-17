// Handler-level tests for /api/admin/readiness-redesign/config.
//
// These exist specifically to close the silent-failure mode that
// motivated the endpoint in the first place: the previous runbook
// pointed operators at /api/admin/settings, which silently filtered
// any non-`gemini_*` key and replied {"status":"ok"}. The new POST
// path must:
//
//   1. write through to <schema>.settings so the chronic_load writer
//      sees the override on its next BackfillChronicLoadSnapshots
//      call;
//   2. reject unknown keys with 400 — typos must not silently pass;
//   3. reject non-positive integer values with 400 — a zero would
//      otherwise mark every day positive;
//   4. echo the post-write effective config so the operator confirms
//      the override took in one round-trip.
//
// We exercise the handler method directly (bypassing the admin guard
// chain) because the validation logic is the load-bearing part — the
// admin auth wrapper is shared boilerplate already covered elsewhere.

package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"health-receiver/internal/ctxdb"
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

// testTenantDB spins up a fresh schema in the configured test database
// and returns a DB pool pinned to it. Mirrors the storage package's
// testDB helper but is package-private to ui so the two test trees
// stay independent.
func testTenantDB(t *testing.T) (*storage.DB, string, func()) {
	t.Helper()

	dsn := os.Getenv("READINESS_TEST_DSN")
	if dsn == "" {
		if os.Getenv("PGHOST") == "" && os.Getenv("PGDATABASE") == "" {
			t.Skip("READINESS_TEST_DSN unset and no libpq env vars; skipping handler test")
		}
	}

	schema := fmt.Sprintf("ui_test_%d_%d", time.Now().UnixNano(), os.Getpid())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	bootstrap, err := storage.New(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to test DB: %v", err)
	}
	if err := bootstrap.CreateSchema(ctx, schema); err != nil {
		bootstrap.Close()
		t.Fatalf("create schema %q: %v", schema, err)
	}
	bootstrap.Close()

	db, err := storage.NewWithSchema(ctx, dsn, schema)
	if err != nil {
		t.Fatalf("open pool on schema %q: %v", schema, err)
	}
	if err := db.EnsureAllTables(); err != nil {
		db.Close()
		t.Fatalf("EnsureAllTables: %v", err)
	}
	// Phase 0 redesign tables are created lazily by their own helper
	// — call it here so handler tests can reach naive_baselines /
	// target_snapshots without each test seeding the schema itself.
	db.EnsureReadinessRedesignTables()

	cleanup := func() {
		db.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		// Reopen a bootstrap pool on the default schema to DROP — the
		// schema-pinned pool above doesn't expose raw SQL exec across
		// the package boundary.
		drop, err := storage.New(dropCtx, dsn)
		if err != nil {
			return
		}
		_ = drop.DropSchema(dropCtx, schema)
		drop.Close()
	}
	return db, schema, cleanup
}

// requestWithTenant builds a request that carries `db`/`schema` in the
// context the way `adminGuard` would in production. The handler reads
// the tenant via `h.tenantSchema(r)` / `h.tenantDB(r)` which both
// resolve from this context.
func requestWithTenant(method, body string, db *storage.DB, schema string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, "/api/admin/readiness-redesign/config", nil)
	} else {
		r = httptest.NewRequest(method, "/api/admin/readiness-redesign/config", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	ctx := ctxdb.WithDB(r.Context(), db, schema)
	ctx = ctxdb.WithIsAdmin(ctx, true)
	return r.WithContext(ctx)
}

func TestAdminReadinessRedesignConfig_POST_ValidOverridePersists(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()

	h := &Handler{}
	body := `{"chronic_load.min_acute_density": 3, "chronic_load.min_breach_days": 4}`
	w := httptest.NewRecorder()
	h.adminReadinessRedesignConfig(w, requestWithTenant(http.MethodPost, body, db, schema))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Schema            string                          `json:"schema"`
		ChronicLoadConfig storage.ChronicLoadConfigStatus `json:"chronic_load_config"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, w.Body.String())
	}
	if resp.Schema != schema {
		t.Errorf("response.schema = %q, want %q", resp.Schema, schema)
	}
	if got := resp.ChronicLoadConfig.Effective.MinAcuteDensity; got != 3 {
		t.Errorf("effective.MinAcuteDensity = %d, want 3", got)
	}
	if got := resp.ChronicLoadConfig.Effective.MinBreachDays; got != 4 {
		t.Errorf("effective.MinBreachDays = %d, want 4", got)
	}
	if resp.ChronicLoadConfig.MatchesDefaults {
		t.Errorf("MatchesDefaults = true after override; expected false")
	}
	if resp.ChronicLoadConfig.CorrectedToDef {
		t.Errorf("CorrectedToDef = true on valid override")
	}

	// Independent read-back through the loader the writer will use on
	// its next backfill. This is the "operator gets ok, override didn't
	// apply" check.
	cfg, _ := db.LoadChronicLoadConfig()
	if cfg.MinAcuteDensity != 3 || cfg.MinBreachDays != 4 {
		t.Errorf("LoadChronicLoadConfig after POST = %+v, want {3,4}", cfg)
	}
}

func TestAdminReadinessRedesignConfig_POST_RejectsUnknownKey(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()

	h := &Handler{}
	// `chronic_load.min_breach` is a near-miss for the real key — exactly
	// the kind of typo the previous silent-drop path would have swallowed.
	body := `{"chronic_load.min_breach": 4}`
	w := httptest.NewRecorder()
	h.adminReadinessRedesignConfig(w, requestWithTenant(http.MethodPost, body, db, schema))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "unknown key") {
		t.Errorf("response missing 'unknown key' marker: %s", w.Body.String())
	}

	// And nothing was written.
	cfg, status := db.LoadChronicLoadConfig()
	if !status.MatchesDefaults {
		t.Errorf("config drifted after rejected POST: %+v", cfg)
	}
}

func TestAdminReadinessRedesignConfig_POST_RejectsNonPositiveValue(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()

	cases := []struct {
		name string
		body string
	}{
		{"zero", `{"chronic_load.min_acute_density": 0}`},
		{"negative", `{"chronic_load.min_breach_days": -1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{}
			w := httptest.NewRecorder()
			h.adminReadinessRedesignConfig(w, requestWithTenant(http.MethodPost, tc.body, db, schema))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "positive integer") {
				t.Errorf("response missing 'positive integer' marker: %s", w.Body.String())
			}
		})
	}

	cfg, status := db.LoadChronicLoadConfig()
	if !status.MatchesDefaults {
		t.Errorf("config drifted after rejected POST: %+v", cfg)
	}
}

func TestAdminReadinessRedesignConfig_POST_RejectsEmptyBody(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()

	h := &Handler{}
	w := httptest.NewRecorder()
	h.adminReadinessRedesignConfig(w, requestWithTenant(http.MethodPost, `{}`, db, schema))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), storage.SettingChronicLoadMinAcuteDensity) {
		t.Errorf("error message should list allowed keys, got: %s", w.Body.String())
	}
}

func TestAdminReadinessRedesignConfig_GET_AfterPOSTReflectsOverride(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()

	h := &Handler{}
	// Write override.
	post := httptest.NewRecorder()
	h.adminReadinessRedesignConfig(post, requestWithTenant(http.MethodPost,
		`{"chronic_load.min_acute_density": 5}`, db, schema))
	if post.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want 200; body=%s", post.Code, post.Body.String())
	}

	// Independent GET round-trip — proves the override is visible to a
	// fresh request, not just the POST handler's in-memory echo.
	get := httptest.NewRecorder()
	h.adminReadinessRedesignConfig(get, requestWithTenant(http.MethodGet, "", db, schema))
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body=%s", get.Code, get.Body.String())
	}
	if !bytes.Contains(get.Body.Bytes(), []byte(`"min_acute_density":5`)) {
		t.Errorf("GET response missing min_acute_density=5: %s", get.Body.String())
	}
}

// Compile-time guard: the test relies on the production loader returning
// the package-default config so the "didn't drift" assertions are
// meaningful. If the defaults ever change, the consts test in
// internal/health will fail first; this just pins the structural
// expectation here.
var _ health.ChronicLoadConfig = health.DefaultChronicLoadConfig()
