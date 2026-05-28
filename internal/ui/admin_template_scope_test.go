package ui

import (
	"os"
	"strings"
	"testing"
)

func TestAdminTemplateTenantPanelDoesNotDefaultReadinessToAllSchemas(t *testing.T) {
	raw, err := os.ReadFile("templates/pages/admin.html")
	if err != nil {
		t.Fatalf("read admin template: %v", err)
	}
	body := string(raw)
	for _, forbidden := range []string{
		`/fragments/admin-readiness-contract?days=14&schema=all`,
		`/fragments/admin-readiness-monitoring?schema=all`,
		`: '&schema=all'`,
		`: '?schema=all'`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("admin tenant panel must not default readiness fragments to all schemas; found %q", forbidden)
		}
	}
}

func TestAdminTemplateOperationsDoNotExposeTenantSelectorInsideProfileTabs(t *testing.T) {
	raw, err := os.ReadFile("templates/pages/admin.html")
	if err != nil {
		t.Fatalf("read admin template: %v", err)
	}
	body := string(raw)
	for _, forbidden := range []string{
		`id="backfill-target"`,
		`admin-operations-scope`,
		`admin_ops_group_desc_prefix`,
		`admin_ops_group_desc_suffix`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("admin operations should be scoped by the active profile tab only; found %q", forbidden)
		}
	}
}

func TestAdminTemplateHasSeparateAdminSettingsAndTenantPanels(t *testing.T) {
	raw, err := os.ReadFile("templates/pages/admin.html")
	if err != nil {
		t.Fatalf("read admin template: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		`data-admin-tab="admin"`,
		`data-admin-tab="settings"`,
		`data-admin-panel="admin"`,
		`data-admin-panel="settings"`,
		`data-admin-panel="tenant"`,
		`data-profile-tab="diagnostics"`,
		`data-profile-tab="readiness"`,
		`data-profile-tab="energy"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin template missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`hx-trigger="load, reload"`,
		`switchAdminTab('general')`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("admin template still uses old tab/load behavior: found %q", forbidden)
		}
	}
}

func TestAdminTemplateUsesExplicitAPIKeyReveal(t *testing.T) {
	raw, err := os.ReadFile("templates/pages/admin.html")
	if err != nil {
		t.Fatalf("read admin template: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		`u.api_key_masked`,
		`function revealAPIKey(username)`,
		`/api/admin/users/`,
		`data-action-reveal=`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin template missing explicit API key reveal behavior: %q", want)
		}
	}
	if strings.Contains(body, `escapeHtml(u.api_key)`) {
		t.Fatalf("admin template should not render full API keys in the users table")
	}
}
