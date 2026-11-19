// Command sleep_shadow serves the synthetic sleep replay API over a local socket.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/sleepshadow"
	"health-receiver/internal/storage"
)

func main() {
	if err := run(os.Args[1:], time.Now); err != nil {
		log.Fatal(err)
	}
}

func run(args []string, clock func() time.Time) error {
	flags := flag.NewFlagSet("sleep_shadow", flag.ContinueOnError)
	confirmProfile := flags.String("confirm-legacy-profile", "", "profile whose unbound pending packets should be confirmed")
	confirmSchema := flags.String("confirm-legacy-schema", "", "verified original EnergyBank schema for those packets")
	rebuildFile := flags.String("rebuild-all", "", "JSON file mapping every local shadow profile to its tenant schema")
	rebuildVersion := flags.String("rebuild-version", "sleep-r9-v1", "checkpoint identity for the full local rebuild")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	confirmLegacy := *confirmProfile != "" || *confirmSchema != ""
	if confirmLegacy && *rebuildFile != "" {
		return errors.New("legacy pending confirmation and full rebuild are separate commands")
	}

	dsn := os.Getenv("SLEEP_SHADOW_DATABASE_URL")
	if dsn == "" {
		return errors.New("set SLEEP_SHADOW_DATABASE_URL for an isolated PostgreSQL database")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return errors.New("invalid shadow database configuration")
	}
	if !filepath.IsAbs(config.ConnConfig.Host) || len(config.ConnConfig.Fallbacks) != 0 {
		return errors.New("the shadow database must use a single local PostgreSQL Unix socket")
	}
	schema, profile := os.Getenv("SLEEP_SHADOW_READINESS_SCHEMA"), os.Getenv("SLEEP_SHADOW_READINESS_PROFILE")
	if *rebuildFile == "" && (schema == "") != (profile == "") {
		return errors.New("set both SLEEP_SHADOW_READINESS_SCHEMA and SLEEP_SHADOW_READINESS_PROFILE")
	}
	energy := os.Getenv("SLEEP_SHADOW_ENERGY") == "1"
	if *rebuildFile == "" && energy && profile == "" {
		return errors.New("local EnergyBank requires a readiness schema and profile binding")
	}
	if confirmLegacy && (!energy || *confirmProfile != profile || *confirmSchema != schema) {
		return errors.New("legacy pending confirmation requires enabled EnergyBank and an explicit matching profile and schema")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(startup, config)
	if err != nil {
		return err
	}
	defer pool.Close()
	if *rebuildFile != "" {
		payload, err := os.ReadFile(*rebuildFile)
		if err != nil {
			return err
		}
		bindings, err := decodeRebuildBindings(payload)
		if err != nil {
			return err
		}
		results, err := sleepshadow.RebuildAll(ctx, pool, bindings, *rebuildVersion, clock())
		for _, result := range results {
			log.Printf("rebuild profile=%q sleep_dates=%d derived_dates=%d complete=%t",
				result.Profile, result.SleepDates, result.DerivedDates, result.Complete)
		}
		return err
	}
	var db *storage.DB
	if profile != "" {
		db, err = storage.NewWithSchema(startup, dsn, schema)
		if err != nil {
			return err
		}
		defer db.Close()
		if err := db.AssertIdentity(startup, config.ConnConfig.User, schema); err != nil {
			return err
		}
	}
	if confirmLegacy {
		count, err := sleepshadow.ConfirmLegacyPending(startup, pool, profile, schema)
		if err != nil {
			return err
		}
		log.Printf("confirmed %d legacy pending sleep packets", count)
		return nil
	}
	var refresh *sleepshadow.DailyRefresh
	if energy {
		refresh = localEnergyRefresh(schema, profile, clock)
	}
	handler, err := sleepshadow.NewWithDailyRefresh(startup, pool, refresh)
	if err != nil {
		return err
	}
	if db != nil {
		handler = withLocalReadiness(handler, db, profile)
	}
	if energy {
		handler = withLocalEnergyBriefing(handler, db, schema, profile, clock)
	}
	directory, err := os.MkdirTemp("", "health-sleep-shadow-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	socket := filepath.Join(directory, "http.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	log.Printf("sleep shadow HTTP socket %s", socket)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func decodeRebuildBindings(payload []byte) (map[string]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("rebuild bindings must map profile IDs to schema names in a JSON object")
	}
	bindings := make(map[string]string)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		profile, ok := token.(string)
		if !ok {
			return nil, errors.New("expected a profile ID in rebuild bindings")
		}
		if _, exists := bindings[profile]; exists {
			return nil, fmt.Errorf("duplicate profile %q in rebuild bindings", profile)
		}
		var schema string
		if err := decoder.Decode(&schema); err != nil {
			return nil, err
		}
		bindings[profile] = schema
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("unexpected input after rebuild bindings")
	}
	return bindings, nil
}

func localEnergyRefresh(schema, profile string, clock func() time.Time) *sleepshadow.DailyRefresh {
	return &sleepshadow.DailyRefresh{
		Profile: profile,
		Schema:  schema,
		Apply: func(ctx context.Context, tx pgx.Tx, dates []string) error {
			return storage.RefreshLocalShadowEnergy(ctx, tx, schema, profile, dates, clock())
		},
		Reconcile: func(ctx context.Context, tx pgx.Tx) error {
			return storage.ReconcileLocalShadowEnergy(ctx, tx, schema, profile, clock())
		},
	}
}
