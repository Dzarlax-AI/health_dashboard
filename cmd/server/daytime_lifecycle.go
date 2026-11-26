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
		inboxEnabled := storage.IsInAppInboxEnabled(db)
		if !cfg.DaytimeEnabled || (!inboxEnabled && !cfg.Enabled()) {
			return
		}
		var bot *notify.Bot
		if cfg.Enabled() {
			bot = notify.NewBot(cfg.Token, cfg.ChatID)
		}
		bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		notify.MaybeFireDaytime(bounded, bot, db, cfg, baseURL, time.Now())
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
