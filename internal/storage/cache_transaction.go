package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// storagePool is the existing SQL surface of DB. Normal runtimes still use
// pgxpool.Pool. A maintenance unit uses a fresh DB bound to one transaction;
// synchronization fields and serving caches are never copied between DBs.
type storagePool interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Begin(context.Context) (pgx.Tx, error)
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
	CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error)
	Close()
}

// cacheTransaction forces every nested writer/read to share the unit's deadline,
// including legacy helpers which create their own context.Background deadlines.
type cacheTransaction struct {
	pgx.Tx
	ctx context.Context
}

func (t *cacheTransaction) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return t.Tx.Exec(t.ctx, sql, args...)
}
func (t *cacheTransaction) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	return t.Tx.Query(t.ctx, sql, args...)
}
func (t *cacheTransaction) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	return t.Tx.QueryRow(t.ctx, sql, args...)
}
func (t *cacheTransaction) CopyFrom(_ context.Context, table pgx.Identifier, columns []string, source pgx.CopyFromSource) (int64, error) {
	return t.Tx.CopyFrom(t.ctx, table, columns, source)
}
func (t *cacheTransaction) Begin(_ context.Context) (pgx.Tx, error) {
	nested, err := t.Tx.Begin(t.ctx)
	if err != nil {
		return nil, err
	}
	return &cacheTransaction{Tx: nested, ctx: t.ctx}, nil
}
func (t *cacheTransaction) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	if options != (pgx.TxOptions{}) {
		return nil, fmt.Errorf("maintenance savepoint does not support transaction options")
	}
	return t.Begin(ctx)
}
func (t *cacheTransaction) SendBatch(_ context.Context, batch *pgx.Batch) pgx.BatchResults {
	return t.Tx.SendBatch(t.ctx, batch)
}
func (t *cacheTransaction) Commit(_ context.Context) error     { return t.Tx.Commit(t.ctx) }
func (t *cacheTransaction) Rollback(ctx context.Context) error { return t.Tx.Rollback(ctx) }
func (t *cacheTransaction) Close()                             {} // A unit does not own the runtime pool.

func (s *DB) runCacheTransaction(ctx context.Context, run func(*DB) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	if err := run(unit); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
