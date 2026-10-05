package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"health-receiver/internal/testdb"
)

func TestCacheTransactionRollbackAndVisibility(t *testing.T) {
	db, cleanup := newCacheMaintenanceTestDB(t, testdb.DSN(t), "cache_atomic")
	defer cleanup()
	ctx := context.Background()
	if _, err := db.pool.Exec(ctx, `INSERT INTO settings(key,value) VALUES('probe','old')`); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected dependent-stage failure")
	err := db.runCacheTransaction(ctx, func(unit *DB) error {
		if _, err := unit.pool.Exec(ctx, `UPDATE settings SET value='partial' WHERE key='probe'`); err != nil {
			return err
		}
		var visible string
		if err := db.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key='probe'`).Scan(&visible); err != nil {
			return err
		}
		if visible != "old" {
			t.Fatalf("uncommitted maintenance published %q", visible)
		}
		nested, err := unit.pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err = nested.Exec(ctx, `UPDATE settings SET value='nested' WHERE key='probe'`); err != nil {
			return err
		}
		if err = nested.Commit(ctx); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("got %v", err)
	}
	var value string
	if err := db.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key='probe'`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "old" {
		t.Fatalf("failed unit left %q", value)
	}
	if err := db.runCacheTransaction(ctx, func(unit *DB) error {
		_, err := unit.pool.Exec(ctx, `UPDATE settings SET value='complete' WHERE key='probe'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key='probe'`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "complete" {
		t.Fatalf("committed unit left %q", value)
	}
}

func TestCacheTransactionUsesUnitDeadline(t *testing.T) {
	db, cleanup := newCacheMaintenanceTestDB(t, testdb.DSN(t), "cache_cancel")
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := db.runCacheTransaction(ctx, func(unit *DB) error {
		// Simulate a legacy writer creating an independent background context.
		_, err := unit.pool.Exec(context.Background(), `SELECT pg_sleep(5)`)
		return err
	})
	if err == nil {
		t.Fatal("unit deadline ignored")
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("legacy writer exceeded unit deadline")
	}
}
