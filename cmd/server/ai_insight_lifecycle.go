package main

import (
	"context"
	"log"
	"time"

	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

// Independent recovery keeps optional provider work away from report timing.
func runAIInsightLifecycle(ctx context.Context, db *storage.DB, mgr *tenants.Manager, schema string, defaults storage.NotifyConfig) {
	tick := func() {
		cfg := db.GetAIConfig(mgr.AIDefaultsFor(ctx, schema))
		lang := db.GetNotifyConfig(defaults).Lang
		if lang != "ru" && lang != "sr" {
			lang = "en"
		}
		bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if err := db.RefreshCurrentAIInsights(bounded, cfg, lang, "lifecycle"); err != nil && ctx.Err() == nil {
			log.Printf("AI insight lifecycle: refresh: %v", err)
		}
	}
	tick()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}
