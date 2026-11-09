package main

import (
	"context"
	"time"

	"health-receiver/internal/notify"
	"health-receiver/internal/storage"
)

// Daytime rules read completed derived state on an independent bounded loop.
// They never block ingestion, report scheduling, or AI generation.
func runDaytimeLifecycle(ctx context.Context, db *storage.DB, defaults storage.NotifyConfig, baseURL string) {
	tick := func() {
		cfg := buildNotifyCfg(db, db.GetNotifyConfig(defaults))
		if !cfg.DaytimeEnabled || !cfg.Enabled() {
			return
		}
		bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		notify.MaybeFireDaytime(bounded, notify.NewBot(cfg.Token, cfg.ChatID), db, cfg, baseURL, time.Now())
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
