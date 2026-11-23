package dbtmetrics

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
)

// The socket path preserves the owner-only private shadow workflow. The
// synthetic Docker runner uses a separate named database and marker.
func semanticSleepReadTransaction(t *testing.T) pgx.Tx {
	t.Helper()
	socket := os.Getenv("SEMANTIC_SHADOW_PG_SOCKET")
	synthetic := os.Getenv("SLEEP_DOCKER_TOKEN")
	if socket == "" && synthetic == "" {
		t.Skip("sleep shadow not configured")
	}
	port, err := strconv.ParseUint(os.Getenv("SEMANTIC_SHADOW_PG_PORT"), 10, 16)
	if err != nil || port == 0 {
		t.Fatal("invalid port")
	}
	target := url.URL{Scheme: "postgres", Host: "localhost", User: url.User(os.Getenv("SEMANTIC_SHADOW_PG_USER")), Path: "/" + os.Getenv("SEMANTIC_SHADOW_PG_DB")}
	args := url.Values{"port": {strconv.Itoa(int(port))}, "sslmode": {"disable"}}
	if synthetic != "" {
		if socket != "" {
			t.Fatal("conflicting sleep shadow transports")
		}
		target.Host = "127.0.0.1"
		target.Path = "/sem_synthetic"
		target.User = url.UserPassword("shadow_reader", "synthetic-only")
	} else {
		if !filepath.IsAbs(socket) {
			t.Fatal("absolute local socket required")
		}
		args.Set("host", socket)
	}
	target.RawQuery = args.Encode()
	ctx := t.Context()
	conn, err := pgx.Connect(ctx, target.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if synthetic != "" {
		var token string
		if err := tx.QueryRow(ctx, "SELECT token FROM public.sleep_fixture_identity").Scan(&token); err != nil || token != synthetic {
			t.Fatal("sleep fixture identity mismatch")
		}
	} else {
		var local bool
		if err := tx.QueryRow(ctx, "SELECT inet_server_addr() IS NULL").Scan(&local); err != nil || !local {
			t.Fatal("expected local socket")
		}
	}
	return tx
}
