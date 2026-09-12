package tenants

import (
	"context"
	"fmt"

	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
)

// OpenReadOnlyTenant opens one active isolated tenant through the same
// registry metadata and derived role credentials as the runtime Manager. It
// is for offline aggregate/report utilities: callers must not use the
// returned DB to mutate tenant state.
func OpenReadOnlyTenant(ctx context.Context, cfg TenantIsolationConfig, schema string) (*storage.DB, func(), error) {
	if !cfg.Enabled {
		return nil, nil, fmt.Errorf("tenant database isolation is disabled")
	}
	reg, err := registry.New(ctx, cfg.RegistryDSN)
	if err != nil {
		return nil, nil, fmt.Errorf("open tenant registry: %w", err)
	}
	manager, err := NewIsolated(reg, cfg.TenantDSNBase, cfg.Credentials)
	if err != nil {
		reg.Close()
		return nil, nil, fmt.Errorf("configure isolated tenant source: %w", err)
	}
	db, err := manager.GetOrCreate(ctx, schema)
	if err != nil {
		manager.Close()
		reg.Close()
		return nil, nil, fmt.Errorf("open isolated tenant source: %w", err)
	}
	return db, func() {
		manager.Close()
		reg.Close()
	}, nil
}
