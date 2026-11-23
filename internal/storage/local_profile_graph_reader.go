package storage

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewLocalProfileGraphReader opens an independent handle to a local dbt build.
// Choosing the ordinary tenant handle again rolls back subsequent reads. In-flight
// reads finish on the pool where they started. The application
// never installs these connection-private projections in a serving tenant.
func NewLocalProfileGraphReader(ctx context.Context, dsn, sourceSchema, graphSchema string) (*DB, error) {
	if sourceSchema == "" || graphSchema == "" || sourceSchema == graphSchema {
		return nil, fmt.Errorf("expected distinct source and graph schemas")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse graph reader connection: %w", err)
	}
	local := func(host string) bool {
		return filepath.IsAbs(host) || host == "localhost" || net.ParseIP(host).IsLoopback()
	}
	if !local(config.ConnConfig.Host) {
		return nil, fmt.Errorf("graph comparison requires a local PostgreSQL connection")
	}
	for _, fallback := range config.ConnConfig.Fallbacks {
		if !local(fallback.Host) {
			return nil, fmt.Errorf("graph comparison requires local PostgreSQL fallbacks")
		}
	}
	config.ConnConfig.RuntimeParams["search_path"] = pgx.Identifier{sourceSchema}.Sanitize()
	config.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	config.MaxConns = 4
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		var scoped bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_catalog.pg_roles r
			JOIN pg_catalog.pg_namespace s ON s.nspowner=r.oid AND s.nspname=$1
			JOIN pg_catalog.pg_namespace g ON g.nspowner=r.oid AND g.nspname=$2
			WHERE r.rolname=current_user AND NOT (r.rolsuper OR r.rolcreaterole OR r.rolcreatedb OR r.rolbypassrls OR r.rolreplication)
			AND current_schema()=s.nspname)`, sourceSchema, graphSchema).Scan(&scoped); err != nil {
			return err
		}
		if !scoped {
			return fmt.Errorf("local graph and source must belong to the same restricted profile login")
		}
		return createGraphReadViews(ctx, conn, sourceSchema, graphSchema)
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &DB{pool: pool, profileGraphSchema: graphSchema}, nil
}

func (s *DB) profileGraphRelation(name string) string {
	return pgx.Identifier{s.profileGraphSchema, name}.Sanitize()
}

// PostgreSQL resolves these two relation names in pg_temp before the tenant
// schema. Other tables, functions and settings keep their normal search path.
// The graph supplies every rollup, including SQL NULL, while the source table
// still owns additional fields such as readiness and the receipt timestamp.
func createGraphReadViews(ctx context.Context, conn *pgx.Conn, source, graph string) error {
	legacy := pgx.Identifier{source, "daily_scores"}.Sanitize()
	daily := pgx.Identifier{graph, "daily_metrics"}.Sanitize()
	hourly := pgx.Identifier{graph, "hourly_metrics"}.Sanitize()
	// PostgreSQL requires a writable transaction even for CREATE TEMP VIEW.
	// This is the only such transaction used by this constructor.
	return pgx.BeginTxFunc(ctx, conn, pgx.TxOptions{AccessMode: pgx.ReadWrite}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT attname FROM pg_catalog.pg_attribute
			WHERE attrelid=$1::regclass AND attnum>0 AND NOT attisdropped ORDER BY attnum`, legacy)
		if err != nil {
			return err
		}
		columns, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		var projection []string
		for _, column := range columns {
			switch {
			case column == "date":
				projection = append(projection, "coalesce(d.date, l.date) as date")
			case allowedDailyMetricColumn(column) || slices.Contains(sleepMetricNames, column):
				projection = append(projection, "d."+pgx.Identifier{column}.Sanitize())
			default:
				projection = append(projection, "l."+pgx.Identifier{column}.Sanitize())
			}
		}
		if _, err := tx.Exec(ctx, "CREATE TEMP VIEW daily_scores AS SELECT "+strings.Join(projection, ",")+" FROM "+daily+" d FULL JOIN "+legacy+" l USING(date)"); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "CREATE TEMP VIEW hourly_metrics AS SELECT metric_name,hour,source,avg_val,min_val,max_val,sample_count FROM "+hourly)
		return err
	})
}
