// This command runs the sleep-selection study against a read-only snapshot.
// It prints a cohort report and accepts an optional inherited database socket
// so a one-off replay can run without sending intervals over SSH.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/storage"
)

func main() {
	from := flag.String("from", "", "first local wake date (YYYY-MM-DD)")
	through := flag.String("through", "", "last local wake date (YYYY-MM-DD)")
	primary := flag.Bool("primary", false, "select the unique active isolated installation administrator")
	database := flag.String("database", "", "database name for the inherited socket, if used")
	fd := flag.Int("database-fd", -1, "optional inherited PostgreSQL Unix socket")
	cases := flag.String("cases", "", "optional comma-separated dates for case duration aggregates")
	flag.Parse()
	if !*primary || *from == "" || *through == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*from, *through, *database, *fd, strings.Split(*cases, ",")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(from, through, database string, fd int, cases []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	dsn := os.Getenv("SLEEP_STUDY_READONLY_DSN")
	if fd >= 0 {
		dsn = "user=postgres sslmode=disable host=127.0.0.1 connect_timeout=5"
	}
	if dsn == "" {
		return fmt.Errorf("configure the read-only connection")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("invalid connection configuration")
	}
	if fd >= 0 {
		if database == "" {
			return fmt.Errorf("missing database name")
		}
		cfg.Database = database
		sock := os.NewFile(uintptr(fd), "postgres-sleep-study-"+strconv.Itoa(fd))
		if sock == nil {
			return fmt.Errorf("invalid inherited socket")
		}
		defer sock.Close()
		cfg.DialFunc = func(context.Context, string, string) (net.Conn, error) { return net.FileConn(sock) }
	}
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.RuntimeParams["application_name"] = "health-sleep-episode-study"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("could not open read-only connection")
	}
	defer conn.Close(context.Background())
	result, err := storage.SleepEpisodeStudy(ctx, conn, from, through, cases)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}
