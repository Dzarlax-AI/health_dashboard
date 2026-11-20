package sleepshadow

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/testdb"
)

func TestRebuildCompletionRechecksLocalDateAfterFence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(testdb.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(config.ConnConfig.Host) || len(config.ConnConfig.Fallbacks) != 0 {
		t.Fatal("completion test requires one disposable local PostgreSQL Unix socket")
	}
	admin, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	database := testdb.SchemaName("shadow_completion")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{database}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{database}.Sanitize()); err != nil {
			t.Error(err)
		}
	}()
	config.ConnConfig.Database = database
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, rebuildInputTrackingSQL); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `CREATE SCHEMA tenant;
		CREATE TABLE tenant.settings(key text PRIMARY KEY,value text);
		INSERT INTO tenant.settings VALUES('timezone','Europe/Belgrade');
		INSERT INTO sleep_shadow.profile(id) VALUES('p');
		INSERT INTO sleep_shadow.rebuild_input_revision(tenant_schema,revision) VALUES('tenant',0)`)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := fingerprintRebuildInputs(0)
	_, err = pool.Exec(ctx, `INSERT INTO sleep_shadow.rebuild_checkpoint
		(profile,tenant_schema,version,source_revision,through_date,phase,input_fingerprint,observation_pending)
		VALUES ('p','tenant','v',0,'2026-03-10','done',$1,false)`, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Date(2026, 3, 10, 22, 59, 59, 0, time.UTC)
	s := store{pool: pool}
	valid, err := s.rebuildStillCurrent(ctx, "p", "tenant", "v", fingerprint, func() time.Time { return before })
	if err != nil || !valid {
		t.Fatalf("same-day checkpoint: valid=%t err=%v", valid, err)
	}

	fence, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Rollback(context.Background())
	if _, err := fence.Exec(ctx, `SELECT id FROM sleep_shadow.profile WHERE id='p' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		valid bool
		err   error
	}
	result := make(chan outcome, 1)
	clockCalled := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		valid, err := s.rebuildStillCurrent(ctx, "p", "tenant", "v", fingerprint, func() time.Time {
			clockCalled <- struct{}{}
			return before.Add(2 * time.Second)
		})
		result <- outcome{valid, err}
	}()
	defer func() {
		cancel()
		_ = fence.Rollback(context.Background())
		<-done
	}()
	// Verify the completion transaction is waiting on the profile lock.
	for {
		var waiting bool
		if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname=$1 AND wait_event_type='Lock' AND query LIKE '%sleep_shadow.profile%FOR UPDATE%')`, database).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	select {
	case <-clockCalled:
		t.Fatal("completion read clock before obtaining profile fence")
	default:
	}
	if err := fence.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := <-result
	if got.err != nil || got.valid {
		t.Fatalf("next-day checkpoint: valid=%t err=%v", got.valid, got.err)
	}
}
