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

func registerHealthIngest(ctx context.Context, mux *http.ServeMux, mgr *tenants.Manager, ingest *handler.Handler, sleepEnabled bool) func(context.Context) error {
	ingest.Register(mux)
	registerSleepRollout(mux, mgr, sleepEnabled)
	if !mgr.UsesIsolatedPools() {
		return ingest.Shutdown
	}
	ingest.EnableSleepCandidateBatches(sleepEnabled)
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSleepRolloutWorker(workerCtx, mgr, ingest, sleepEnabled)
	}()
	return func(ctx context.Context) error {
		stop()
		select {
		case <-done:
			return ingest.Shutdown(ctx)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
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
		case <-ticker.C:
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
				_, err := db.RunSleepRolloutNext(unit, time.Now)
				cancel()
				if err != nil && ctx.Err() == nil {
					log.Print("sleep rollout: a prepared receipt is pending retry")
				}
			}
		}
	}
}
