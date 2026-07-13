package tenants

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
)

// LegacySetup is explicit shared-login compatibility for isolation-disabled
// deployments. It creates schemas but never roles and has no DROP capability.
type LegacySetup struct {
	reg *registry.Registry
	dsn string
}

func (l *LegacySetup) ReconcileNonterminal(ctx context.Context) error {
	ops, err := l.reg.ListNonterminalProvisioningOperations(ctx)
	if err != nil {
		return err
	}
	for _, op := range ops {
		if err := registry.ValidateSchemaName(op.SchemaName); err != nil {
			return err
		}
		if err := l.initialize(ctx, op.SchemaName); err != nil {
			return fmt.Errorf("resume legacy tenant %s: %w", op.Username, err)
		}
		if err := l.reg.ActivateLegacyReservation(ctx, op); err != nil {
			return fmt.Errorf("activate resumed legacy tenant %s: %w", op.Username, err)
		}
	}
	return nil
}

func NewLegacySetup(reg *registry.Registry, dsn string) *LegacySetup {
	return &LegacySetup{reg: reg, dsn: dsn}
}
func (l *LegacySetup) initialize(ctx context.Context, schema string) error {
	if err := registry.ValidateSchemaName(schema); err != nil {
		return err
	}
	cfg, err := pgxpool.ParseConfig(l.dsn)
	if err != nil {
		return err
	}
	delete(cfg.ConnConfig.RuntimeParams, "search_path")
	cfg.AfterConnect = nil
	cfg.MaxConns = 1
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer p.Close()
	if _, err = p.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{schema}.Sanitize()); err != nil {
		return err
	}
	db, err := storage.NewWithSchema(ctx, l.dsn, schema)
	if err != nil {
		return err
	}
	defer db.Close()
	return ensureTenantTables(db)
}
func (l *LegacySetup) CreateFirstTenant(ctx context.Context, req registry.CreateUserReq) (*registry.User, error) {
	if req.SchemaName == "" {
		req.SchemaName = "health_" + req.Username
	}
	u, op, err := l.reg.ReserveFirstUser(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := l.initialize(ctx, req.SchemaName); err != nil {
		return nil, fmt.Errorf("legacy schema initialization: %w", err)
	}
	if err := l.reg.ActivateLegacyReservation(ctx, op); err != nil {
		return nil, err
	}
	u.ProvisioningState = registry.ProvisioningStateActive
	return u, nil
}
func (l *LegacySetup) CreateTenant(ctx context.Context, req registry.CreateUserReq) (*registry.User, error) {
	if req.SchemaName == "" {
		req.SchemaName = "health_" + req.Username
	}
	u, op, err := l.reg.ReserveUser(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := l.initialize(ctx, req.SchemaName); err != nil {
		return nil, err
	}
	if err := l.reg.ActivateLegacyReservation(ctx, op); err != nil {
		return nil, err
	}
	u.ProvisioningState = registry.ProvisioningStateActive
	return u, nil
}
