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
