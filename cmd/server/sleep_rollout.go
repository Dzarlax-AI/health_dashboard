package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"health-receiver/internal/handler"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

func registerSleepRollout(ctx context.Context, mux *http.ServeMux, mgr *tenants.Manager, enabled bool) {
	if !enabled || !mgr.UsesIsolatedPools() {
		return
	}
	mux.HandleFunc("POST /health/sleep/v2", handler.SleepRolloutHandler(func(r *http.Request) (*storage.DB, bool) {
		db, schema, _, ok := mgr.DBForAPIKey(r.Context(), r.Header.Get("X-API-Key"))
		if !ok || mgr.VerifyTenantContract(r.Context(), schema, db) != nil {
			return nil, false
		}
		return db, true
	}))
	go runSleepRolloutWorker(ctx, mgr)
}

func runSleepRolloutWorker(ctx context.Context, mgr *tenants.Manager) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			for _, db := range mgr.ActiveDBs(ctx) {
				if db.GetSetting(storage.SleepRolloutModeSetting, "off") != "prepare" {
					continue
				}
				unit, cancel := context.WithTimeout(ctx, 30*time.Second)
				_, err := db.RunSleepRolloutNext(unit, now)
				cancel()
				if err != nil && ctx.Err() == nil {
					log.Print("sleep rollout: a prepared receipt is pending retry")
				}
			}
		}
	}
}
