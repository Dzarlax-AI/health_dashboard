package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

const (
	metricCatalogPollInterval    = 10 * time.Second
	metricCatalogRefreshInterval = 2 * time.Minute
	metricCatalogBuildTimeout    = 90 * time.Second
)

func startMetricCatalogWorker(parent context.Context, mgr *tenants.Manager, reg *registry.Registry) func() {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	startedAt := time.Now()
	go func() {
		defer close(done)
		ticker := time.NewTicker(metricCatalogPollInterval)
		defer ticker.Stop()
		retryAt := make(map[string]time.Time)
		for {
			refreshMetricCatalogs(ctx, mgr, reg, startedAt, retryAt)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func refreshMetricCatalogs(ctx context.Context, mgr *tenants.Manager, reg *registry.Registry, startedAt time.Time, retryAt map[string]time.Time) {
	refresh := func(schema string, db *storage.DB) {
		if time.Now().Before(retryAt[schema]) {
			return
		}
		buildCtx, cancel := context.WithTimeout(ctx, metricCatalogBuildTimeout)
		defer cancel()
		start := time.Now()
		built, err := db.RefreshMetricCatalog(buildCtx, startedAt, metricCatalogRefreshInterval)
		switch {
		case errors.Is(err, storage.ErrMetricCatalogBusy), ctx.Err() != nil:
		case err != nil:
			retryAt[schema] = time.Now().Add(metricCatalogRefreshInterval)
			log.Printf("metric catalog refresh failed (%T) after %s", err, time.Since(start).Round(time.Millisecond))
		default:
			delete(retryAt, schema)
			if built {
				log.Printf("metric catalog refreshed in %s", time.Since(start).Round(time.Millisecond))
			}
		}
	}
	if mgr.LegacyMode() {
		refresh("", mgr.LegacyDB())
		return
	}
	listCtx, cancel := context.WithTimeout(ctx, time.Second)
	users, err := reg.ListActiveUsers(listCtx)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("metric catalog tenant discovery failed (%T)", err)
		}
		return
	}
	for _, user := range users {
		if ctx.Err() != nil {
			return
		}
		poolCtx, cancel := context.WithTimeout(ctx, time.Second)
		db, err := mgr.GetOrCreate(poolCtx, user.SchemaName)
		cancel()
		if err != nil {
			log.Printf("metric catalog tenant pool unavailable (%T)", err)
			continue
		}
		refresh(user.SchemaName, db)
	}
}

func prepareMetricCatalogs(ctx context.Context, mgr *tenants.Manager, reg *registry.Registry) (int, error) {
	users, err := reg.ListActiveUsers(ctx)
	if err != nil {
		return 0, fmt.Errorf("could not enumerate active tenants (%T)", err)
	}
	for i, user := range users {
		tenantCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		err := prepareTenantMetricCatalog(tenantCtx, mgr, user.SchemaName)
		cancel()
		if err != nil {
			return i, err
		}
	}
	return len(users), nil
}

func prepareTenantMetricCatalog(ctx context.Context, mgr *tenants.Manager, schema string) error {
	db, err := mgr.GetOrCreate(ctx, schema)
	if err != nil {
		return fmt.Errorf("could not open tenant pool (%T)", err)
	}
	if err := mgr.VerifyTenantContract(ctx, schema, db); err != nil {
		return fmt.Errorf("could not verify tenant contract (%T)", err)
	}
	return prepareMetricCatalog(ctx, db)
}

func prepareSingleMetricCatalog(ctx context.Context, dsn string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	db, err := storage.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("could not open single-user pool (%T)", err)
	}
	defer db.Close()
	if err := db.VerifyProvisionedSchemaContext(ctx); err != nil {
		return fmt.Errorf("could not verify single-user schema (%T)", err)
	}
	return prepareMetricCatalog(ctx, db)
}

func prepareMetricCatalog(ctx context.Context, db *storage.DB) error {
	if err := db.PrepareMetricCatalog(ctx); err != nil {
		return fmt.Errorf("could not prepare a verified snapshot (%T)", err)
	}
	if _, err := db.ReadMetricCatalog(ctx, false); err != nil {
		return fmt.Errorf("could not read the prepared snapshot (%T)", err)
	}
	return nil
}
