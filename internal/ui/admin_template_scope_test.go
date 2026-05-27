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
