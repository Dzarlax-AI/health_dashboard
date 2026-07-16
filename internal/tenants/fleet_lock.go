package tenants

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/registry"
)

const fleetMigrationLockReleaseTimeout = 5 * time.Second

// FleetMigrationLock pins the administrator connection that owns the
// exclusive PostgreSQL advisory lock. One instance covers the complete CLI
// migration run rather than an individual tenant transaction.
type FleetMigrationLock struct {
	mu   sync.Mutex
	conn *pgx.Conn
}

// AcquireFleetMigrationLock waits for all in-flight provisioning guards and
// prevents any reservation or provisioning path from starting until Release.
func (m *Migrator) AcquireFleetMigrationLock(ctx context.Context) (*FleetMigrationLock, error) {
	// Do not borrow from m.admin: the migration work needs that one-connection
	// pool while this lock remains held. This direct connection is dedicated to
	// lock ownership and is always closed on Release.
	if m.registryLockConfig == nil {
		return nil, errors.New("registry fleet lock configuration is missing")
	}
	conn, err := pgx.ConnectConfig(ctx, m.registryLockConfig.Copy())
	if err != nil {
		return nil, fmt.Errorf("acquire fleet migration connection: %w", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, registry.FleetMigrationAdvisoryLockKey); err != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), fleetMigrationLockReleaseTimeout)
		_ = conn.Close(closeCtx)
		cancel()
		return nil, fmt.Errorf("acquire fleet migration lock: %w", err)
	}
	return &FleetMigrationLock{conn: conn}, nil
}

// Release uses a detached bounded context. If explicit unlock fails, the
// physical connection is removed from the pool and closed so a locked session
// can never be reused by a later migration command.
func (l *FleetMigrationLock) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return nil
	}
	conn := l.conn
	l.conn = nil

	ctx, cancel := context.WithTimeout(context.Background(), fleetMigrationLockReleaseTimeout)
	var unlocked bool
	err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, registry.FleetMigrationAdvisoryLockKey).Scan(&unlocked)
	cancel()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), fleetMigrationLockReleaseTimeout)
	closeErr := conn.Close(closeCtx)
	closeCancel()
	if err == nil && !unlocked {
		err = errors.New("fleet migration lock was not owned by its guard")
	}
	return errors.Join(err, closeErr)
}
