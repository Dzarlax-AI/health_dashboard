package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"health-receiver/internal/apnsrelay"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dbURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dbURL == "" {
		log.Fatal("apns_relay: DATABASE_URL is required")
	}
	cipher, err := apnsrelay.ParseTokenCipherKeys(os.Getenv("RELAY_TOKEN_ENCRYPTION_KEYS"))
	if err != nil {
		log.Fatalf("apns_relay: %v", err)
	}
	registry, err := apnsrelay.LoadRegistryFromEnv()
	if err != nil {
		log.Fatalf("apns_relay: %v", err)
	}
	verifier, err := apnsrelay.NewAppAttestVerifier()
	if err != nil {
		log.Fatalf("apns_relay: %v", err)
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("apns_relay: connect database: %v", err)
	}
	defer pool.Close()

	store := apnsrelay.NewStore(pool, cipher, verifier)
	if err := store.EnsureSchema(ctx); err != nil {
		log.Fatalf("apns_relay: ensure schema: %v", err)
	}

	srv := apnsrelay.NewServer(registry, store)
	if strings.EqualFold(strings.TrimSpace(os.Getenv("APNS_RELAY_TRUST_PROXY")), "true") {
		srv.TrustProxy = true
	}

	// Hourly background cleanup of expired challenges and long-expired/revoked grants.
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				if err := store.Cleanup(cleanCtx); err != nil {
					log.Printf("apns_relay: cleanup error: %v", err)
				}
				cancel()
			}
		}
	}()

	addr := strings.TrimSpace(os.Getenv("ADDR"))
	if addr == "" {
		addr = ":8081"
	}
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutCtx)
	}()

	log.Printf("apns_relay: listening on %s", addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("apns_relay: listen: %v", err)
	}
}
