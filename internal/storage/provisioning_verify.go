package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// VerifyTenantIsolation performs narrow denial probes used when validating a
// newly opened restricted tenant pool. It exposes no general query facility.
func (s *DB) VerifyTenantIsolation(ctx context.Context, forbiddenSchemas ...string) error {
	probes := []string{`SELECT count(*) FROM health_registry.users`}
	for _, schema := range forbiddenSchemas {
		probes = append(probes, "SELECT count(*) FROM "+pgx.Identifier{schema, "isolation_probe"}.Sanitize())
	}
	for _, probe := range probes {
		_, err := s.pool.Exec(ctx, probe)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			return fmt.Errorf("tenant isolation denial probe did not fail with SQLSTATE 42501: %v", err)
		}
	}
	return nil
}

// VerifyProvisionedSchema is the fail-closed catalog contract used before a
// tenant is activated. Startup's compatibility wrappers may log DDL errors;
// provisioning must observe the resulting incompleteness explicitly.
func (s *DB) VerifyProvisionedSchema() error {
	return s.VerifySchemaContract()
}

func (s *DB) VerifyProvisionedSchemaContext(ctx context.Context) error {
	return s.VerifySchemaContractContext(ctx)
}
