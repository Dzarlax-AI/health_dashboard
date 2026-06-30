package testdb

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const EnableEnv = "HEALTH_DB_TESTS"

type TB interface {
	Helper()
	Skipf(format string, args ...any)
	Fatalf(format string, args ...any)
}

func DSN(t TB) string {
	t.Helper()
	if os.Getenv(EnableEnv) != "1" {
		t.Skipf("%s=1 not set; skipping DB integration test", EnableEnv)
	}
	dsn := os.Getenv("READINESS_TEST_DSN")
	if dsn != "" {
		return dsn
	}
	if os.Getenv("PGHOST") == "" && os.Getenv("PGDATABASE") == "" {
		t.Fatalf("%s=1 but READINESS_TEST_DSN is unset and libpq env vars are missing", EnableEnv)
	}
	return ""
}

func SchemaName(prefix string) string {
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixNano(), os.Getpid())
}

func NewPool(ctx context.Context, dsn, schema string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse pg config: %w", err)
	}
	config.MaxConns = 2
	config.MinConns = 0
	config.MaxConnIdleTime = 30 * time.Second
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	if schema != "" {
		quotedSchema := pgx.Identifier{schema}.Sanitize()
		config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
			_, err := conn.Exec(ctx, "SET search_path = "+quotedSchema)
			return err
		}
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("connect to pg: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping pg: %w", err)
	}
	return pool, nil
}

func CreateSchema(ctx context.Context, pool *pgxpool.Pool, schema string) error {
	_, err := pool.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize())
	return err
}

func DropSchema(ctx context.Context, pool *pgxpool.Pool, schema string) error {
	_, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	return err
}

func TruncateCurrentSchema(ctx context.Context, pool *pgxpool.Pool) error {
	var schema string
	if err := pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return fmt.Errorf("read current_schema: %w", err)
	}
	rows, err := pool.Query(ctx, `
		SELECT table_name
		  FROM information_schema.tables
		 WHERE table_schema = current_schema()
		   AND table_type = 'BASE TABLE'
		 ORDER BY table_name
	`)
	if err != nil {
		return fmt.Errorf("list schema tables: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return fmt.Errorf("scan schema table: %w", err)
		}
		tables = append(tables, pgx.Identifier{schema, table}.Sanitize())
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate schema tables: %w", err)
	}
	if len(tables) == 0 {
		return nil
	}
	_, err = pool.Exec(ctx, "TRUNCATE "+strings.Join(tables, ", ")+" RESTART IDENTITY CASCADE")
	if err != nil {
		return fmt.Errorf("truncate schema tables: %w", err)
	}
	return nil
}
