package registry

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// FleetMigrationAdvisoryLockKey coordinates fleet-wide schema migrations with
// every path that can begin tenant provisioning. Migrations take the exclusive
// form; provisioning holds the shared form for the complete operation.
const FleetMigrationAdvisoryLockKey int64 = 0x6865616c74685f66

const fleetLockReleaseTimeout = 5 * time.Second

// ProvisioningGuard holds a session-level shared advisory lock. It must remain
// live until the provisioning operation has either activated or failed.
type ProvisioningGuard struct {
	mu   sync.Mutex
	conn *pgx.Conn
}

// AcquireProvisioningGuard waits until no fleet migration owns the exclusive
// lock, then pins a dedicated registry connection for the lifetime of the
// guard. Provisioning may need every pooled connection while the guard lives,
// so operation-lifetime advisory locks must not consume pool capacity.
func (r *Registry) AcquireProvisioningGuard(ctx context.Context) (*ProvisioningGuard, error) {
	if r == nil || r.connConfig == nil {
		return nil, errors.New("registry provisioning lock configuration is missing")
	}
	conn, err := pgx.ConnectConfig(ctx, r.connConfig.Copy())
	if err != nil {
		return nil, fmt.Errorf("acquire provisioning lock connection: %w", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock_shared($1)`, FleetMigrationAdvisoryLockKey); err != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), fleetLockReleaseTimeout)
		closeErr := conn.Close(closeCtx)
		cancel()
		return nil, fmt.Errorf("acquire provisioning fleet lock: %w", errors.Join(err, closeErr))
	}
	return &ProvisioningGuard{conn: conn}, nil
}

// Release unlocks with its own bounded context so cancellation of the request
// that performed provisioning cannot leak a session lock into the pool.
func (g *ProvisioningGuard) Release() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.conn == nil {
		return nil
	}
	conn := g.conn
	g.conn = nil

	ctx, cancel := context.WithTimeout(context.Background(), fleetLockReleaseTimeout)
	var unlocked bool
	err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock_shared($1)`, FleetMigrationAdvisoryLockKey).Scan(&unlocked)
	cancel()
	if err == nil && !unlocked {
		err = errors.New("provisioning fleet lock was not owned by its guard")
	}
	// Closing the dedicated physical connection is mandatory even after a
	// successful unlock, and releases all session locks if explicit unlock
	// failed or timed out.
	closeCtx, closeCancel := context.WithTimeout(context.Background(), fleetLockReleaseTimeout)
	closeErr := conn.Close(closeCtx)
	closeCancel()
	return errors.Join(err, closeErr)
}
