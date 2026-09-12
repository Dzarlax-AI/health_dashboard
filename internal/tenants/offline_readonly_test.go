package tenants

import (
	"context"
	"testing"
)

func TestOpenReadOnlyTenantRejectsDisabledIsolation(t *testing.T) {
	db, closeSource, err := OpenReadOnlyTenant(context.Background(), TenantIsolationConfig{}, "health")
	if err == nil {
		t.Fatal("OpenReadOnlyTenant succeeded with isolation disabled")
	}
	if db != nil || closeSource != nil {
		t.Fatal("OpenReadOnlyTenant returned a source with isolation disabled")
	}
}
