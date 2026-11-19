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

func registerHealthIngest(ctx context.Context, mux *http.ServeMux, mgr *tenants.Manager, ingest *handler.Handler, sleepEnabled bool) {
	if mgr.UsesIsolatedPools() {
		ingest.EnableSleepCandidateBatches(sleepEnabled)
		go runSleepRolloutWorker(ctx, mgr, ingest, sleepEnabled)
	}
	ingest.Register(mux)
	registerSleepRollout(mux, mgr, sleepEnabled)
}

func registerSleepRollout(mux *http.ServeMux, mgr *tenants.Manager, enabled bool) {
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
}

func runSleepRolloutWorker(ctx context.Context, mgr *tenants.Manager, ingest *handler.Handler, prepare bool) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			for _, db := range mgr.ActiveDBs(ctx) {
				// Drain a bounded number per tick, independently of the prepare mode.
				unit, cancel := context.WithTimeout(ctx, 30*time.Second)
				for range 16 {
					processed, err := ingest.ProcessSleepCandidateMetrics(unit, db)
					if err != nil && ctx.Err() == nil {
						log.Print("sleep rollout: mixed metrics are pending retry")
					}
					if err != nil || !processed {
						break
					}
				}
				cancel()
				mode := db.GetSetting(storage.SleepRolloutModeSetting, "off")
				if !prepare || (mode != "prepare" && mode != "serve") {
					continue
				}
				unit, cancel = context.WithTimeout(ctx, 30*time.Second)
				_, err := db.RunSleepRolloutNext(unit, now)
				cancel()
				if err != nil && ctx.Err() == nil {
					log.Print("sleep rollout: a prepared receipt is pending retry")
				}
			}
		}
	}
}
