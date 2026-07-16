package registry

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
	conn *pgxpool.Conn
}

// AcquireProvisioningGuard waits until no fleet migration owns the exclusive
// lock, then pins a registry connection for the lifetime of the guard.
func (r *Registry) AcquireProvisioningGuard(ctx context.Context) (*ProvisioningGuard, error) {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire provisioning lock connection: %w", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock_shared($1)`, FleetMigrationAdvisoryLockKey); err != nil {
		conn.Release()
		return nil, fmt.Errorf("acquire provisioning fleet lock: %w", err)
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
	if err == nil && unlocked {
		conn.Release()
		return nil
	}

	// Never return a possibly locked session to the pool. Hijacking removes it
	// from pool ownership; closing the physical connection releases all of its
	// PostgreSQL session locks even when the explicit unlock failed.
	raw := conn.Hijack()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), fleetLockReleaseTimeout)
	closeErr := raw.Close(closeCtx)
	closeCancel()
	if err == nil && !unlocked {
		err = errors.New("provisioning fleet lock was not owned by its guard")
	}
	return errors.Join(err, closeErr)
}
