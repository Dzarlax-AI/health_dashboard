package registry

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProvisioningGuardDoesNotConsumeRegistryPoolCapacity(t *testing.T) {
	r, ctx := newEmptyTestRegistry(t)
	held := make([]*pgxpool.Conn, 0, r.pool.Config().MaxConns-1)
	for i := int32(1); i < r.pool.Config().MaxConns; i++ {
		conn, err := r.pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
	}
	defer func() {
		for _, conn := range held {
			conn.Release()
		}
	}()

	guard, err := r.AcquireProvisioningGuard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = guard.Release() }()

	queryCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var one int
	if err := r.pool.QueryRow(queryCtx, `SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("registry pool exhausted by provisioning guard: %v", err)
	}
}
