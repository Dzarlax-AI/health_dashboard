package ui

import (
	"testing"

	"health-receiver/internal/registry"
)

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

func TestSortUsersForAdminTabsKeepsCurrentSchemaFirstWithoutDroppingOthers(t *testing.T) {
	users := []registry.User{
		{Username: "mariia", SchemaName: "health_mariia"},
		{Username: "admin", SchemaName: "health"},
	}

	sortUsersForAdminTabs(users, "health")

	if users[0].SchemaName != "health" {
		t.Fatalf("first schema = %q, want current schema health", users[0].SchemaName)
	}
	if len(users) != 2 || users[1].SchemaName != "health_mariia" {
		t.Fatalf("users after sort = %+v, want both schemas preserved with non-current second", users)
	}
}

func TestMaskAPIKeyKeepsOnlySuffix(t *testing.T) {
	got := maskAPIKey("abcdefghijklmnopqrstuvwxyz1234567890")
	if got != "...34567890" {
		t.Fatalf("maskAPIKey() = %q, want suffix-only mask", got)
	}
}

func TestMaskAPIKeyDoesNotExposeShortKeys(t *testing.T) {
	got := maskAPIKey("short")
	if got != "********" {
		t.Fatalf("maskAPIKey(short) = %q, want full mask", got)
	}
}

func TestMaskAPIKeyEightCharsBoundary(t *testing.T) {
	got := maskAPIKey("12345678")
	if got != "********" {
		t.Fatalf("maskAPIKey(8 chars) = %q, want full mask", got)
	}
}
