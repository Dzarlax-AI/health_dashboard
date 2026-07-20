package tenants

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func secureFixedPoolConfig(cfg *pgxpool.Config) error {
	if cfg == nil || cfg.ConnConfig == nil {
		return errors.New("fixed database configuration is missing (details redacted)")
	}
	if err := secureFixedConnConfig(cfg.ConnConfig); err != nil {
		return err
	}
	// Parsed DSNs cannot install a callback, but explicitly clear it so callers
	// cannot later reuse this helper with a role-changing pool hook.
	cfg.AfterConnect = nil
	return nil
}

func secureFixedConnConfig(cfg *pgx.ConnConfig) error {
	if cfg == nil {
		return errors.New("fixed database configuration is missing (details redacted)")
	}
	delete(cfg.RuntimeParams, "search_path")
	for _, key := range []string{"role", "session_authorization", "options"} {
		if _, exists := cfg.RuntimeParams[key]; exists {
			return errors.New("fixed database configuration contains forbidden startup parameters (details redacted)")
		}
	}
	return nil
}

func requireExactPoolIdentity(ctx context.Context, pool *pgxpool.Pool, expected string) error {
	var sessionUser, currentUser string
	if err := pool.QueryRow(ctx, `SELECT session_user,current_user`).Scan(&sessionUser, &currentUser); err != nil {
		return errors.New("verify fixed database identity (details redacted)")
	}
	if sessionUser != expected || currentUser != expected {
		return errors.New("fixed database identity mismatch (details redacted)")
	}
	return nil
}

func requireExactConnIdentity(ctx context.Context, conn *pgx.Conn, expected string) error {
	var sessionUser, currentUser string
	if err := conn.QueryRow(ctx, `SELECT session_user,current_user`).Scan(&sessionUser, &currentUser); err != nil {
		return errors.New("verify fixed database identity (details redacted)")
	}
	if sessionUser != expected || currentUser != expected {
		return errors.New("fixed database identity mismatch (details redacted)")
	}
	return nil
}
