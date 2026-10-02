package main

import (
	"context"
	"log"
	"sync"
	"time"

	"health-receiver/internal/notify"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

// morningLifecycleDue recovers a scheduled report after restart and keeps an
// existing check-in alive independently of the report scheduler's next timer.
func morningLifecycleDue(cfg notify.Config, now time.Time, reportSent, hasCheckin bool) bool {
	loc := reportTimezone(cfg)
	now = now.In(loc)
	if now.Hour() < 5 {
		return false
	}
	if reportSent {
		return now.Hour() < 20
	}
	if hasCheckin && now.Hour() < 20 {
		return true
	}
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	scheduled := cfg.NextMorning(midnight.Add(-time.Nanosecond))
	return !now.Before(scheduled) || !now.Before(cfg.MorningCapTime(now))
}

func runMorningLifecycle(ctx context.Context, db *storage.DB, mgr *tenants.Manager, reg *registry.Registry, schema string, defaults storage.NotifyConfig) {
	tick := func() {
		// Expiry is persistence-only and must run through quiet hours and restart.
		if err := expireOverdueMorningCheckins(db, mgr.CheckinSendMuFor(schema), time.Now()); err != nil {
			log.Printf("check-in: overdue expiry: %v", err)
		}
		cfg := db.GetNotifyConfig(defaults)
		if !cfg.Enabled() {
			return
		}
		ncfg := buildNotifyCfg(db, cfg)
		now := time.Now().In(reportTimezone(ncfg))
		today := now.Format("2006-01-02")
		row, err := db.GetTodayCheckin(today, storage.CheckinSourceTelegram)
		// A check-in read failure must not block the report lane.
		if err != nil {
			log.Printf("morning lifecycle: check-in state unavailable: %v", err)
		}
		reportSent := db.HasSettledMorningReport(today)
		if morningLifecycleDue(ncfg, now, reportSent, row != nil) || (!reportSent && morningAIShouldPrewarm(ncfg, now, false)) {
			makeMorningTrigger(ctx, db, mgr.MorningSendMuFor(schema), mgr, reg, schema, defaults)()
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

// runTenantCheckinCycle shares only its own lock with callback persistence, so
// a slow check-in send cannot hold the report's delivery lock.
func runTenantCheckinCycle(db *storage.DB, mgr *tenants.Manager, reg *registry.Registry, schema string, bot *notify.Bot, cfg notify.Config, now time.Time, eligible bool) {
	if now.Hour() < 5 || now.Hour() >= 20 {
		return
	}
	today := now.Format("2006-01-02")
	checkinMu := mgr.CheckinSendMuFor(schema)
	if checkinMu != nil {
		checkinMu.Lock()
		defer checkinMu.Unlock()
	}
	row, err := db.GetTodayCheckin(today, storage.CheckinSourceTelegram)
	if err != nil {
		log.Printf("check-in: read: %v", err)
		return
	}
	enabled := morningCheckinEnabled(reg)
	if enabled {
		if row == nil && eligible {
			if err := notify.SendCheckinPrompt(bot, db, cfg.Lang, today, now, notify.CheckinPromptExpiry(now)); err != nil {
				log.Printf("check-in: prompt: %v", err)
			}
			// Always defer ancillary messages in the initial prompt cycle, including
			// ambiguous delivery and persistence failure.
			return
		}
		if notify.CheckinReminderDue(row, now, now.Location()) {
			attempted, err := notify.SendCheckinReminderOnce(bot, db, cfg.Lang, today, now)
			if err != nil {
				log.Printf("check-in: reminder: %v", err)
			}
			if attempted || err != nil {
				return
			}
		}
		if row != nil && row.Status == storage.CheckinStatusPrompted && !now.Before(row.ExpiresAt) {
			if _, err := db.ExpireCheckin(today, storage.CheckinSourceTelegram, now); err != nil {
				log.Printf("check-in: expire: %v", err)
				return
			}
		}
		if notify.CheckinPending(row, now) {
			return
		}
	}
	if !db.HasSentMorningReport(today) {
		return
	}
	if !trySendWakeFeedbackAfterMorning(bot, db, cfg, today, now, enabled) {
		trySendContextPromptAfterMorning(bot, db, cfg, today, now)
	}
}

// runMorningReportAttempt serializes the report lane and only records a
// confirmed send. Durable delivery reservations own ambiguous outcomes.
func runMorningReportAttempt(mu *sync.Mutex, alreadySent func() bool, send func() (bool, string, error), markSent func() error) {
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	if alreadySent() {
		return
	}
	attempted, reason, err := send()
	if err != nil {
		log.Printf("morning report: send: %v", err)
		return
	}
	if !attempted {
		return
	}
	if err := markSent(); err != nil {
		log.Printf("morning report: mark sent: %v", err)
		return
	}
	log.Printf("morning report: sent reason=%s", reason)
}

const morningAIWaitBudget = 3 * time.Minute

type morningAIWaitKey struct {
	db   *storage.DB
	lang string
}
type morningAIWaitEntry struct {
	date     string
	deadline time.Time
}

var morningAIWaits = struct {
	sync.Mutex
	entries map[morningAIWaitKey]morningAIWaitEntry
}{entries: make(map[morningAIWaitKey]morningAIWaitEntry)}

// morningAIAllowsReport waits via scheduler ticks, never by blocking the
// check-in lane. The first eligible tick starts a fixed, non-sliding budget.
func morningAIAllowsReport(enabled, ready, failed bool, now, deadline time.Time) bool {
	return !enabled || ready || failed || !now.Before(deadline)
}

func morningAIReportDeadline(db *storage.DB, lang, date string, now time.Time) time.Time {
	morningAIWaits.Lock()
	defer morningAIWaits.Unlock()
	key := morningAIWaitKey{db, lang}
	entry, ok := morningAIWaits.entries[key]
	if !ok || entry.date != date {
		entry = morningAIWaitEntry{date, now.Add(morningAIWaitBudget)}
		morningAIWaits.entries[key] = entry
	}
	return entry.deadline
}

func morningAIShouldPrewarm(cfg notify.Config, now time.Time, eligible bool) bool {
	if eligible {
		return true
	}
	loc := reportTimezone(cfg)
	local := now.In(loc)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	scheduled := cfg.NextMorning(midnight.Add(-time.Nanosecond))
	cap := cfg.MorningCapTime(local)
	if cap.Before(scheduled) {
		scheduled = cap
	}
	return !local.Before(scheduled.Add(-30 * time.Minute))
}

type overdueMorningCheckinStore interface{ ExpireOverdueTelegramCheckins(time.Time) error }

func expireOverdueMorningCheckins(store overdueMorningCheckinStore, mu *sync.Mutex, now time.Time) error {
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	return store.ExpireOverdueTelegramCheckins(now)
}
