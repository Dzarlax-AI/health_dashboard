# Admin User Tabs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restructure `/admin` into explicit `General settings` plus per-user tenant tabs without removing `/settings`.

**Architecture:** Add a small tenant-scope helper in `internal/ui` so admin handlers can safely resolve `schema=` overrides through `health_registry.users`. Extend the admin page data model to include registered users, then reorganize the existing admin template into top-level tabs that keep global controls separate from tenant-scoped controls. Preserve current endpoint behavior when `schema=` is omitted.

**Tech Stack:** Go `net/http`, `html/template`, embedded Go templates, vanilla JavaScript, htmx fragments, PostgreSQL tenant schemas via `internal/tenants.Manager`.

---

### Task 1: Add a Shared Admin Tenant Scope Helper

**Files:**
- Modify: `internal/ui/handler.go`
- Test: `internal/ui/admin_tenant_scope_test.go`

- [ ] **Step 1: Write failing tests for schema override behavior**

Create `internal/ui/admin_tenant_scope_test.go`:

```go
package ui

import (
	"context"
	"net/http/httptest"
	"testing"

	"health-receiver/internal/ctxdb"
	"health-receiver/internal/storage"
)

func TestResolveAdminTenantScope_DefaultsToRequestTenant(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest("GET", "/api/admin/gaps", nil)
	req = req.WithContext(ctxdb.WithDB(req.Context(), &storage.DB{}, "health"))

	scope, err := h.resolveAdminTenantScope(req)
	if err != nil {
		t.Fatalf("resolveAdminTenantScope returned error: %v", err)
	}
	if scope.Schema != "health" {
		t.Fatalf("schema = %q, want health", scope.Schema)
	}
	if scope.DB == nil {
		t.Fatalf("DB is nil")
	}
}

func TestResolveAdminTenantScope_RejectsSchemaOverrideWithoutRegistry(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest("GET", "/api/admin/gaps?schema=health_mariia", nil)
	req = req.WithContext(ctxdb.WithDB(req.Context(), &storage.DB{}, "health"))

	_, err := h.resolveAdminTenantScope(req)
	if err == nil {
		t.Fatalf("expected error")
	}
	if err.status != 503 {
		t.Fatalf("status = %d, want 503", err.status)
	}
}
```

- [ ] **Step 2: Run the targeted test and confirm it fails**

Run:

```bash
go test ./internal/ui -run 'TestResolveAdminTenantScope' -count=1
```

Expected: compile failure because `resolveAdminTenantScope` does not exist.

- [ ] **Step 3: Add the helper types and default behavior**

In `internal/ui/handler.go`, near `tenantDB` / `tenantSchema`, add:

```go
type adminTenantScope struct {
	DB       *storage.DB
	Schema   string
	Username string
}

type httpStatusError struct {
	status int
	msg    string
}

func (e httpStatusError) Error() string { return e.msg }

func (h *Handler) resolveAdminTenantScope(r *http.Request) (adminTenantScope, *httpStatusError) {
	db := h.tenantDB(r)
	schema := h.tenantSchema(r)
	if db == nil || schema == "" {
		return adminTenantScope{}, &httpStatusError{status: http.StatusServiceUnavailable, msg: "tenant DB unavailable"}
	}

	target := strings.TrimSpace(r.URL.Query().Get("schema"))
	if target == "" || target == schema {
		username := ""
		if h.reg != nil && !h.mgr.LegacyMode() {
			if u, err := h.reg.GetBySchema(r.Context(), schema); err == nil && u != nil {
				username = u.Username
			}
		}
		return adminTenantScope{DB: db, Schema: schema, Username: username}, nil
	}

	if h.reg == nil {
		return adminTenantScope{}, &httpStatusError{status: http.StatusServiceUnavailable, msg: "registry not available"}
	}

	user, err := h.reg.GetBySchema(r.Context(), target)
	if err != nil || user == nil {
		return adminTenantScope{}, &httpStatusError{status: http.StatusBadRequest, msg: "unknown schema"}
	}
	targetDB, err := h.mgr.GetOrCreate(r.Context(), user.SchemaName)
	if err != nil || targetDB == nil {
		return adminTenantScope{}, &httpStatusError{status: http.StatusServiceUnavailable, msg: "tenant DB pool not initialised"}
	}
	return adminTenantScope{DB: targetDB, Schema: user.SchemaName, Username: user.Username}, nil
}

func writeStatusError(w http.ResponseWriter, err *httpStatusError) {
	if err == nil {
		return
	}
	http.Error(w, err.msg, err.status)
}
```

Ensure `handler.go` imports already include `net/http`, `strings`, and `storage`; add only missing imports.

- [ ] **Step 4: Run the targeted tests**

Run:

```bash
go test ./internal/ui -run 'TestResolveAdminTenantScope' -count=1
```

Expected: PASS.

### Task 2: Use the Scope Helper in Existing Admin Read Endpoints

**Files:**
- Modify: `internal/ui/handler.go`
- Test: `internal/ui/admin_tenant_scope_test.go`

- [ ] **Step 1: Add a handler-level unknown schema test**

Append to `internal/ui/admin_tenant_scope_test.go`:

```go
func TestAdminGaps_RejectsSchemaOverrideWithoutRegistry(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest("GET", "/api/admin/gaps?schema=missing", nil)
	req = req.WithContext(ctxdb.WithDB(req.Context(), &storage.DB{}, "health"))
	w := httptest.NewRecorder()

	h.adminGaps(w, req)

	if w.Code != 503 {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}
```

- [ ] **Step 2: Update read endpoints to resolve tenant scope**

In `internal/ui/handler.go`, update these handlers to call `resolveAdminTenantScope(r)` and use `scope.DB` / `scope.Schema`:

```go
func (h *Handler) fragmentAdminStatus(w http.ResponseWriter, r *http.Request) {
	scope, scopeErr := h.resolveAdminTenantScope(r)
	if scopeErr != nil {
		writeStatusError(w, scopeErr)
		return
	}
	lang := langFromRequest(r)
	status, err := scope.DB.GetCacheStatus()
	// existing rendering continues unchanged
}
```

Apply the same pattern to:

- `adminStatus`
- `adminGaps`
- `adminQualityAudit`
- `adminCheckinCoverage`
- `adminStressValidation`

For `adminCheckinCoverage`, replace:

```go
today := tenantLocalToday(h, db, h.tenantSchema(r))
```

with:

```go
today := tenantLocalToday(h, scope.DB, scope.Schema)
```

For `adminStressValidation`, resolve timezone with:

```go
tz := scope.DB.GetNotifyConfig(h.mgr.NotifyDefaultsFor(scope.Schema)).Timezone
```

- [ ] **Step 3: Run targeted tests**

Run:

```bash
go test ./internal/ui -run 'TestAdminGaps|TestAdminCheckinCoverage|TestFragmentAdminReadinessContract|TestAdminReadinessRedesignOperationalContract' -count=1
```

Expected: existing tests pass.

### Task 3: Add Schema Support to Tenant-Scoped Write Endpoints

**Files:**
- Modify: `internal/ui/handler.go`
- Modify: `internal/ui/import_handler.go`
- Modify: `internal/ui/energy_backfill_handler.go`
- Test: extend existing UI tests where handlers can be exercised without a live DB.

- [ ] **Step 1: Refactor `adminBackfill` to use the shared helper**

Replace the manual `schema=` validation block in `adminBackfill` with:

```go
scope, scopeErr := h.resolveAdminTenantScope(r)
if scopeErr != nil {
	writeStatusError(w, scopeErr)
	return
}
backfill := h.mgr.BackfillFor(scope.Schema)
```

Use `scope.Schema` in response messages.

- [ ] **Step 2: Update quality write endpoints**

In `adminQualityFix` and `adminQualityDigest`, resolve scope first:

```go
scope, scopeErr := h.resolveAdminTenantScope(r)
if scopeErr != nil {
	writeStatusError(w, scopeErr)
	return
}
db := scope.DB
```

For `adminQualityDigest`, load notify config from `db` and keep behavior unchanged.

- [ ] **Step 3: Update per-tenant EnergyBank config endpoint**

Despite the old comment describing `/api/admin/energy-settings` as admin-only, it writes tenant settings. Update `adminEnergySettings` to resolve scope and use `scope.DB`; include `schema` in the JSON response:

```go
jsonResponse(w, map[string]any{
	"energy.beta":                 cfg.Beta,
	"energy.z_threshold":          cfg.ZThreshold,
	"energy.stress_drain_enabled": cfg.StressDrainEnabled,
	"effective_beta":              cfg.EffectiveBeta(),
	"schema":                      scope.Schema,
})
```

- [ ] **Step 4: Update import handlers**

In `internal/ui/import_handler.go`, resolve scope in `adminImportStatus` and `adminImportUpload`:

```go
scope, scopeErr := h.resolveAdminTenantScope(r)
if scopeErr != nil {
	writeStatusError(w, scopeErr)
	return
}
schema := scope.Schema
db := scope.DB
```

Use `h.mgr.BackfillFor(schema)` unchanged.

- [ ] **Step 5: Update energy backfill handlers**

In `internal/ui/energy_backfill_handler.go`, resolve scope in:

- `energyBackfillSummary`
- `energyBackfillStatus`
- `energyBackfillRun`

Use `scope.Schema`, `scope.DB`, and `h.mgr.NotifyDefaultsFor(scope.Schema)`.

- [ ] **Step 6: Run targeted tests**

Run:

```bash
go test ./internal/ui -count=1
```

Expected: PASS.

### Task 4: Build Admin Page Data for General and User Tabs

**Files:**
- Modify: `internal/ui/handler.go`
- Test: add/extend `internal/ui/admin_page_test.go`

- [ ] **Step 1: Add admin page view models**

Near `pageAdmin`, add:

```go
type adminUserTab struct {
	Username   string
	SchemaName string
	Email      string
	IsAdmin    bool
	Current    bool
}

type adminPageData struct {
	BasePage
	MultiUser     bool
	CurrentSchema string
	UserTabs      []adminUserTab
}
```

- [ ] **Step 2: Populate users in `pageAdmin`**

Replace the anonymous `struct` in `pageAdmin` with `adminPageData`:

```go
data := adminPageData{
	BasePage:      h.basePage(r, T(langFromRequest(r), "admin_title"), "admin"),
	MultiUser:     !h.mgr.LegacyMode(),
	CurrentSchema: h.tenantSchema(r),
}
if h.reg != nil && !h.mgr.LegacyMode() {
	users, err := h.reg.ListUsers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data.UserTabs = make([]adminUserTab, 0, len(users))
	for _, u := range users {
		data.UserTabs = append(data.UserTabs, adminUserTab{
			Username:   u.Username,
			SchemaName: u.SchemaName,
			Email:      u.Email,
			IsAdmin:    u.IsAdmin,
			Current:    u.SchemaName == data.CurrentSchema,
		})
	}
}
renderPage(w, "admin", data)
```

In legacy mode, leave `UserTabs` empty and render a single current-user scope in the template.

- [ ] **Step 3: Add a render smoke test**

Create `internal/ui/admin_page_test.go` with a lightweight data test if full registry setup is too expensive:

```go
package ui

import "testing"

func TestAdminPageDataUserTabsCanRepresentCurrentTenant(t *testing.T) {
	data := adminPageData{
		MultiUser:     true,
		CurrentSchema: "health",
		UserTabs: []adminUserTab{
			{Username: "admin", SchemaName: "health", Current: true},
			{Username: "mariia", SchemaName: "health_mariia"},
		},
	}
	if len(data.UserTabs) != 2 {
		t.Fatalf("len(UserTabs) = %d, want 2", len(data.UserTabs))
	}
	if !data.UserTabs[0].Current {
		t.Fatalf("first tab should be current")
	}
}
```

- [ ] **Step 4: Run targeted tests**

Run:

```bash
go test ./internal/ui -run 'TestAdminPageData|TestResolveAdminTenantScope' -count=1
```

Expected: PASS.

### Task 5: Reorganize `admin.html` Into General and User Tabs

**Files:**
- Modify: `internal/ui/templates/pages/admin.html`
- Modify: `internal/ui/style.go`
- Modify: `internal/ui/i18n_en.go`
- Modify: `internal/ui/i18n_ru.go`

- [ ] **Step 1: Add i18n keys**

Add English and Russian keys:

```go
"admin_tab_general":          "General settings",
"admin_tab_current_user":     "Current user",
"admin_tab_target_schema":    "Schema",
"admin_user_scope_label":     "User scope",
"admin_user_scope_desc":      "Actions in this tab affect only this user's tenant schema.",
"admin_general_scope_desc":   "These settings affect the whole Health Dashboard installation.",
"admin_section_user_settings": "User settings",
"admin_section_diagnostics":  "Diagnostics",
"admin_section_operations":   "Operations",
```

Russian:

```go
"admin_tab_general":          "Общие настройки",
"admin_tab_current_user":     "Текущий пользователь",
"admin_tab_target_schema":    "Схема",
"admin_user_scope_label":     "Область пользователя",
"admin_user_scope_desc":      "Действия в этой вкладке затрагивают только tenant-схему этого пользователя.",
"admin_general_scope_desc":   "Эти настройки влияют на всю установку Health Dashboard.",
"admin_section_user_settings": "Настройки пользователя",
"admin_section_diagnostics":  "Диагностика",
"admin_section_operations":   "Операции",
```

- [ ] **Step 2: Add tab CSS**

In `internal/ui/style.go` near admin styles, add:

```css
.admin-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin: 0 0 18px;
}
.admin-tab {
  border: 1px solid var(--border);
  background: var(--surface);
  color: var(--text-secondary);
  border-radius: 8px;
  padding: 8px 12px;
  font-size: 13px;
  cursor: pointer;
}
.admin-tab.active {
  border-color: var(--accent);
  background: var(--accent-soft);
  color: var(--text);
}
.admin-tab-panel[hidden] { display: none; }
.admin-scope-banner {
  border: 1px solid var(--card-border);
  background: var(--surface-2);
  border-radius: 8px;
  padding: 12px 14px;
  margin-bottom: 18px;
  color: var(--text-secondary);
  font-size: 13px;
}
```

Adjust variable names to existing tokens if `--accent-soft` is unavailable; use `var(--surface-2)` as fallback.

- [ ] **Step 3: Wrap global controls in the General tab**

In `admin.html`, add top-level tab buttons after `#admin-header`:

```html
<div class="admin-tabs" role="tablist" aria-label="Admin scope">
  <button type="button" class="admin-tab active" role="tab" aria-selected="true" data-admin-tab="general" onclick="switchAdminTab('general')">{{T .Lang "admin_tab_general"}}</button>
  {{range .UserTabs}}
  <button type="button" class="admin-tab" role="tab" aria-selected="false" data-admin-tab="{{.SchemaName}}" onclick="switchAdminTab('{{.SchemaName}}')">{{.Username}}</button>
  {{end}}
</div>
```

Move these existing sections into:

```html
<div class="admin-tab-panel" id="admin-tab-general" data-admin-panel="general">
  ...
</div>
```

Global sections:

- AI briefing settings.
- Registered users list.
- Add user form.

- [ ] **Step 4: Wrap tenant sections in one panel per user**

Inside `{{range .UserTabs}}`, render:

```html
<div class="admin-tab-panel" id="admin-tab-{{.SchemaName}}" data-admin-panel="{{.SchemaName}}" hidden>
  <div class="admin-scope-banner">
    <strong>{{T $.Lang "admin_user_scope_label"}}:</strong>
    {{.Username}} · <code>{{.SchemaName}}</code>
    <div>{{T $.Lang "admin_user_scope_desc"}}</div>
  </div>
  ...
</div>
```

Move tenant-scoped sections into this panel:

- Cache status.
- Data gaps.
- Quality audit.
- Check-in coverage.
- Stress validation.
- Readiness contract.
- Backfill operations.
- Readiness operations.
- Quality maintenance.
- Readiness onboarding wizard.
- Energy settings.

For the first implementation, render one copy per user. Keep element IDs schema-suffixed to avoid duplicate IDs:

```html
id="admin-cache-table-{{.SchemaName}}"
id="admin-gaps-section-{{.SchemaName}}"
id="admin-quality-section-{{.SchemaName}}"
```

- [ ] **Step 5: Add tab switching JS**

In the `scripts` block:

```js
var activeAdminSchema = '';

function switchAdminTab(tab) {
  activeAdminSchema = tab === 'general' ? '' : tab;
  document.querySelectorAll('[data-admin-tab]').forEach(function(btn) {
    var active = btn.getAttribute('data-admin-tab') === tab;
    btn.classList.toggle('active', active);
    btn.setAttribute('aria-selected', active ? 'true' : 'false');
  });
  document.querySelectorAll('[data-admin-panel]').forEach(function(panel) {
    panel.hidden = panel.getAttribute('data-admin-panel') !== tab;
  });
}

function adminSchemaQuery() {
  return activeAdminSchema ? ('?schema=' + encodeURIComponent(activeAdminSchema)) : '';
}

function adminSchemaParam(prefix) {
  return activeAdminSchema ? (prefix + 'schema=' + encodeURIComponent(activeAdminSchema)) : '';
}
```

Update fetches for tenant-scoped actions to append `schema` using `activeAdminSchema`.

- [ ] **Step 6: Run template compile tests**

Run:

```bash
go test ./internal/ui -run 'TestAdmin|TestRender|TestFragment' -count=1
```

If no render tests match, run:

```bash
go test ./internal/ui -count=1
```

Expected: PASS.

### Task 6: Port `/settings` Sections Into Tenant Tabs Without Breaking `/settings`

**Files:**
- Modify: `internal/ui/templates/pages/admin.html`
- Do not delete: `internal/ui/templates/pages/settings.html`

- [ ] **Step 1: Copy user settings markup into tenant panels**

Copy the Telegram, webhook status, import, and EnergyBank historical backfill sections from `settings.html` into each user tab panel. Suffix all IDs with `{{.SchemaName}}`:

```html
id="cfg-telegram-token-{{.SchemaName}}"
id="cfg-telegram-chat-id-{{.SchemaName}}"
id="cfg-report-lang-{{.SchemaName}}"
id="cfg-timezone-{{.SchemaName}}"
id="btn-energy-backfill-{{.SchemaName}}"
```

Keep `/settings` unchanged for non-admin/current-user access.

- [ ] **Step 2: Add schema-aware JS helpers for suffixed fields**

Add:

```js
function sid(id) {
  return activeAdminSchema ? (id + '-' + activeAdminSchema) : id;
}

function fetchWithSchema(url, options) {
  if (!activeAdminSchema) return fetch(url, options);
  var sep = url.indexOf('?') === -1 ? '?' : '&';
  return fetch(url + sep + 'schema=' + encodeURIComponent(activeAdminSchema), options);
}
```

Use `$(sid('cfg-telegram-token'))` etc. in admin-page versions of settings functions.

- [ ] **Step 3: Keep `/settings` JS untouched**

Do not edit `settings.html` unless a shared function is extracted. The admin page can duplicate small JS functions temporarily because the two pages have different DOM IDs and scope rules.

- [ ] **Step 4: Manually verify no duplicate unsuffixed IDs remain in repeated user panels**

Run:

```bash
rg -n 'id="[^"]*{{\\.SchemaName}}|id="(admin-|cfg-|btn-|import-|energy-)' internal/ui/templates/pages/admin.html
```

Expected: repeated tenant panel IDs include `{{.SchemaName}}`; global-only IDs may remain unsuffixed in the General tab.

### Task 7: Wire Tenant-Scoped JavaScript Calls

**Files:**
- Modify: `internal/ui/templates/pages/admin.html`

- [ ] **Step 1: Update tenant-scoped fetch calls**

Use `fetchWithSchema` for:

```js
fetchWithSchema('/api/admin/gaps')
fetchWithSchema('/api/admin/quality-audit')
fetchWithSchema('/api/admin/checkin-coverage?days=14')
fetchWithSchema('/api/admin/quality-fix', {method:'POST'})
fetchWithSchema('/api/admin/quality-digest', {method:'POST'})
fetchWithSchema('/api/admin/energy-settings')
fetchWithSchema('/api/admin/stress-validation?window=30')
fetchWithSchema('/api/settings')
fetchWithSchema('/api/settings/test-notify?kind=' + kind, {method:'POST'})
fetchWithSchema('/api/webhook-status')
fetchWithSchema('/api/webhook-status/retry', {method:'POST'})
fetchWithSchema('/api/settings/energy-backfill/summary')
fetchWithSchema('/api/settings/energy-backfill/status')
fetchWithSchema('/api/settings/energy-backfill', {method:'POST', ...})
```

For URLs that already contain query params, `fetchWithSchema` must append `&schema=...`.

- [ ] **Step 2: Update htmx fragment URLs**

For static htmx attributes inside ranged user panels, include schema directly:

```html
hx-get="/fragments/admin-status?schema={{.SchemaName}}"
hx-get="/fragments/admin-readiness-contract?days=14&schema={{.SchemaName}}"
```

For onboarding `hx-vals`, keep existing schema function but return `activeAdminSchema`:

```js
function onboardingSchema() {
  return activeAdminSchema || '';
}
```

- [ ] **Step 3: Process htmx after raw HTML injection**

Where admin JS uses `innerHTML = ...` with returned fragments that contain htmx attributes, add:

```js
if (window.htmx) htmx.process(targetElement);
```

Use the exact target element after injection. This preserves the known PR #115 fix pattern.

### Task 8: Run Tests and Browser Verification

**Files:**
- No source edits unless verification finds issues.

- [ ] **Step 1: Run Go tests**

Run:

```bash
go test ./internal/ui ./internal/tenants ./internal/registry
```

Expected: PASS.

Then run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 2: Build the server**

Run:

```bash
go build ./cmd/server
```

Expected: PASS.

- [ ] **Step 3: Start local server**

Use the project env convention. If DB access is needed:

```powershell
. $HOME\.health-db
make dev
```

If the preview server is flaky, ask the user to open the generated/local file manually rather than spending excessive time debugging the preview infrastructure.

- [ ] **Step 4: Browser-check `/admin`**

Verify:

- `General settings` tab is first and active.
- each registered user has a tab.
- user tabs show the username and schema near destructive actions.
- switching tabs does not leave stale data in visible panels.
- `/settings` still loads.
- mobile width does not overlap tab labels or action buttons.

### Task 9: Final Cleanup

**Files:**
- Modify if needed: `docs/superpowers/specs/2026-05-23-admin-user-tabs-design.md`
- Modify if needed: `docs/superpowers/plans/2026-05-23-admin-user-tabs.md`

- [ ] **Step 1: Review diff**

Run:

```bash
git diff --stat
git diff -- docs/superpowers internal/ui
```

Expected: changes are limited to the admin tabs implementation, tests, and the spec/plan docs.

- [ ] **Step 2: Check generated/ignored files**

Run:

```bash
git status --short
```

Expected: `.superpowers/` remains ignored. No generated build output is tracked.

- [ ] **Step 3: Do not commit without permission**

Wait for explicit user permission before staging, committing, pushing, or opening a PR.

---

## Self-Review

- Spec coverage: The plan covers tabbed `/admin`, general vs user scope, preserving `/settings`, tenant schema validation through registry, endpoint scope updates, UI grouping, and browser verification.
- Placeholder scan: No `TBD`, `TODO`, placeholder steps, or unspecified test commands.
- Type consistency: `adminTenantScope`, `httpStatusError`, `adminUserTab`, and `adminPageData` are introduced before use and reused consistently.
