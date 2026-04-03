package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"health-receiver/internal/ai"
	"health-receiver/internal/handler"
	"health-receiver/internal/mcpserver"
	"health-receiver/internal/notify"
	"health-receiver/internal/storage"
	"health-receiver/internal/ui"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}
	addr := getEnv("ADDR", ":8080")
	apiKey         := os.Getenv("API_KEY")
	uiPassword     := os.Getenv("UI_PASSWORD")
	trustFwdAuth   := os.Getenv("TRUST_FORWARD_AUTH") == "true"
	baseURL        := getEnv("BASE_URL", "http://localhost"+addr)
	aiDefaults := storage.AIConfig{
		APIKey: os.Getenv("GEMINI_API_KEY"),
		Model:  getEnv("GEMINI_MODEL", "gemini-2.5-flash"),
	}

	db, err := storage.New(context.Background(), dbURL)
	if err != nil {
		log.Fatalf("init db: %v", err)
	}
	defer db.Close()

	db.EnsureIndexes()
	db.EnsureAIBriefingsTable()

	// Env-derived defaults for notify config (DB settings take priority at runtime).
	notifyDefaults := storage.NotifyConfig{
		Token:              os.Getenv("TELEGRAM_TOKEN"),
		ChatID:             os.Getenv("TELEGRAM_CHAT_ID"),
		Lang:               getEnv("REPORT_LANG", "en"),
		Timezone:           getEnv("REPORT_TZ", ""),
		MorningWeekdayHour: getEnvInt("REPORT_MORNING_WEEKDAY", 8),
		MorningWeekendHour: getEnvInt("REPORT_MORNING_WEEKEND", 9),
		EveningWeekdayHour: getEnvInt("REPORT_EVENING_WEEKDAY", 20),
		EveningWeekendHour: getEnvInt("REPORT_EVENING_WEEKEND", 21),
	}

	// Start the backfill scheduler. It runs an initial backfill shortly after
	// startup and then re-runs whenever new data arrives, debouncing rapid
	// successive syncs into a single pass.
	// Force-rebuild only when caches are empty (first import). Otherwise
	// incremental: refreshes last 48h and recomputes stale readiness scores.
	// Use `cmd/backfill --force` for a manual full rebuild if needed.
	sched := newBackfillScheduler(db, 2*time.Minute)
	go func() {
		time.Sleep(5 * time.Second)
		force := db.NeedsForceBackfill()
		if force {
			log.Println("startup: caches empty, rebuilding all…")
		} else {
			log.Println("startup: incremental cache refresh…")
		}
		db.BackfillAggregates(force)
		db.BackfillScores(force)
		log.Println("startup: cache refresh done")
	}()

	// maybeFireMorningReport triggers the AI morning briefing when:
	// • time is after 05:00 in the configured timezone
	// • today's step count exceeds 300 (user is actually awake and moving)
	// • no morning report has been sent yet today
	maybeFireMorningReport := func() {
		aiCfg := db.GetAIConfig(aiDefaults)
		if !aiCfg.Enabled() {
			return // AI briefing disabled — no API key configured
		}
		cfg := db.GetNotifyConfig(notifyDefaults)
		loc := time.Local
		if cfg.Timezone != "" {
			if l, err := time.LoadLocation(cfg.Timezone); err == nil {
				loc = l
			}
		}
		now := time.Now().In(loc)
		today := now.Format("2006-01-02")

		// Only after 05:00
		if now.Hour() < 5 {
			return
		}
		// Guard: already sent today
		if db.HasSentMorningReport(today) {
			return
		}
		// Guard: not enough steps yet
		if db.GetTodayStepCount(today) < 300 {
			return
		}

		if insight := ensureTodayAIInsight(db, aiCfg); insight == "" {
			log.Println("morning trigger: AI insight unavailable, aborting")
			return
		}

		// Send Telegram morning report (now embeds the AI insight).
		if cfg.Enabled() {
			ncfg := notify.Config{
				Token: cfg.Token, ChatID: cfg.ChatID, Lang: cfg.Lang,
				Timezone:           cfg.Timezone,
				MorningWeekdayHour: cfg.MorningWeekdayHour,
				MorningWeekendHour: cfg.MorningWeekendHour,
				EveningWeekdayHour: cfg.EveningWeekdayHour,
				EveningWeekendHour: cfg.EveningWeekendHour,
			}
			bot := notify.NewBot(ncfg.Token, ncfg.ChatID)
			if err := notify.SendMorning(bot, db, ncfg); err != nil {
				log.Printf("morning trigger: send telegram: %v", err)
				return
			}
		}
		if err := db.MarkMorningReportSent(today); err != nil {
			log.Printf("morning trigger: mark sent: %v", err)
		}
		log.Printf("morning trigger: done, insight saved for %s", today)
	}

	onNewData := func() {
		// Inline cache upsert already happened in the handler (UpsertRecentCache).
		// The debounced backfill is a safety net that refreshes the last 48h and
		// recomputes readiness scores — no cache invalidation/deletion needed.
		sched.schedule()
		go maybeFireMorningReport()
	}

	// forceRunning prevents concurrent force-rebuild runs.
	var forceRunning int32
	backfillFn := func(force bool) {
		if !force {
			sched.schedule()
			return
		}
		if !atomic.CompareAndSwapInt32(&forceRunning, 0, 1) {
			log.Println("force backfill already running, skipping")
			return
		}
		go func() {
			defer atomic.StoreInt32(&forceRunning, 0)
			log.Println("force backfill: starting full rebuild…")
			db.BackfillAggregates(true)
			db.BackfillScores(true)
			log.Println("force backfill: done")
		}()
	}

	// Always start scheduler — it re-reads config from DB each iteration.
	go runReportScheduler(db, notifyDefaults, aiDefaults)

	// testNotify reads fresh config from DB on every call.
	testNotifyFn := func(kind string) error {
		scfg := db.GetNotifyConfig(notifyDefaults)
		if !scfg.Enabled() {
			return fmt.Errorf("Telegram not configured: set TELEGRAM_TOKEN and TELEGRAM_CHAT_ID")
		}
		ncfg := notify.Config{
			Token: scfg.Token, ChatID: scfg.ChatID, Lang: scfg.Lang,
			Timezone:           scfg.Timezone,
			MorningWeekdayHour: scfg.MorningWeekdayHour,
			MorningWeekendHour: scfg.MorningWeekendHour,
			EveningWeekdayHour: scfg.EveningWeekdayHour,
			EveningWeekendHour: scfg.EveningWeekendHour,
		}
		bot := notify.NewBot(ncfg.Token, ncfg.ChatID)
		if kind == "evening" {
			return notify.SendEvening(bot, db, ncfg)
		}
		// For morning test: generate AI insight if not yet done today.
		// Do NOT mark as sent — test should not block the real morning trigger.
		ensureTodayAIInsight(db, aiDefaults)
		return notify.SendMorning(bot, db, ncfg)
	}

	mux := http.NewServeMux()
	handler.New(db, apiKey, onNewData).Register(mux)
	ui.New(db, uiPassword, apiKey, trustFwdAuth, backfillFn, testNotifyFn, notifyDefaults, aiDefaults).Register(mux)
	mcpserver.Register(mux, db, baseURL, apiKey)

	logged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		mux.ServeHTTP(w, r)
		log.Printf("%s %s %s %v", r.RemoteAddr, r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})

	log.Printf("listening on %s", addr)
	log.Printf("MCP endpoint: %s/mcp", baseURL)
	if err := http.ListenAndServe(addr, logged); err != nil {
		log.Fatalf("server: %v", err)
	}
}

// backfillScheduler debounces backfill triggers so that multiple POST /health
// requests within `delay` collapse into a single backfill run.
type backfillScheduler struct {
	db      *storage.DB
	delay   time.Duration
	trigger chan struct{}
}

func newBackfillScheduler(db *storage.DB, delay time.Duration) *backfillScheduler {
	s := &backfillScheduler{
		db:      db,
		delay:   delay,
		trigger: make(chan struct{}, 1),
	}
	go s.run()
	return s
}

// schedule queues a backfill. If one is already queued, this is a no-op.
func (s *backfillScheduler) schedule() {
	select {
	case s.trigger <- struct{}{}:
	default: // already queued
	}
}

// scheduleAfter queues a backfill to start after the given duration.
func (s *backfillScheduler) scheduleAfter(d time.Duration) {
	go func() {
		time.Sleep(d)
		s.schedule()
	}()
}

func (s *backfillScheduler) run() {
	for range s.trigger {
		time.Sleep(s.delay)
		// Drain any additional triggers that arrived during the delay.
		for len(s.trigger) > 0 {
			<-s.trigger
		}
		log.Println("scheduler: running incremental backfill…")
		s.db.RunIncrementalBackfill()
		log.Println("scheduler: done")
	}
}

// runReportScheduler fires morning and evening Telegram reports on schedule.
// It re-reads config from DB on every iteration so settings changes take effect
// without a server restart.
func runReportScheduler(db *storage.DB, defaults storage.NotifyConfig, aiDefaults storage.AIConfig) {
	for {
		cfg := db.GetNotifyConfig(defaults)
		if !cfg.Enabled() {
			time.Sleep(5 * time.Minute) // retry when credentials are configured
			continue
		}

		ncfg := notify.Config{
			Token: cfg.Token, ChatID: cfg.ChatID, Lang: cfg.Lang,
			Timezone:           cfg.Timezone,
			MorningWeekdayHour: cfg.MorningWeekdayHour,
			MorningWeekendHour: cfg.MorningWeekendHour,
			EveningWeekdayHour: cfg.EveningWeekdayHour,
			EveningWeekendHour: cfg.EveningWeekendHour,
		}

		now := time.Now()
		nextMorning := ncfg.NextMorning(now)
		nextEvening := ncfg.NextEvening(now)

		isMorning := nextMorning.Before(nextEvening)
		next := nextEvening
		if isMorning {
			next = nextMorning
		}

		log.Printf("report scheduler: next %s report at %s",
			map[bool]string{true: "morning", false: "evening"}[isMorning],
			next.Format("2006-01-02 15:04"))

		time.Sleep(time.Until(next))

		// Re-read config after sleep — credentials may have changed.
		cfg = db.GetNotifyConfig(defaults)
		if !cfg.Enabled() {
			continue
		}
		bot := notify.NewBot(cfg.Token, cfg.ChatID)
		if isMorning {
			// Skip if the smart trigger already sent the morning report today.
			loc := time.Local
			if ncfg.Timezone != "" {
				if l, err := time.LoadLocation(ncfg.Timezone); err == nil {
					loc = l
				}
			}
			today := time.Now().In(loc).Format("2006-01-02")
			if db.HasSentMorningReport(today) {
				log.Println("report scheduler: morning report already sent by smart trigger, skipping")
				continue
			}
			log.Println("report scheduler: sending morning report (fallback)…")
			ensureTodayAIInsight(db, aiDefaults)
			if err := notify.SendMorning(bot, db, ncfg); err != nil {
				log.Printf("report scheduler: morning send error: %v", err)
			} else {
				db.MarkMorningReportSent(today)
			}
		} else {
			log.Println("report scheduler: sending evening report…")
			if err := notify.SendEvening(bot, db, ncfg); err != nil {
				log.Printf("report scheduler: evening send error: %v", err)
			}
		}
	}
}

// ensureTodayAIInsight returns today's AI insight, generating and saving it if not yet done.
// Returns "" if AI is not configured or generation fails.
func ensureTodayAIInsight(db *storage.DB, aiDefaults storage.AIConfig) string {
	aiCfg := db.GetAIConfig(aiDefaults)
	if !aiCfg.Enabled() {
		return ""
	}
	today := time.Now().Format("2006-01-02")
	if existing := db.GetAIBriefing(today); existing != "" {
		return existing // already generated today, reuse
	}
	raw := db.GetRawMetrics()
	if raw == nil {
		log.Println("ensureTodayAIInsight: no raw metrics available")
		return ""
	}
	rawJSON, err := json.Marshal(raw)
	if err != nil {
		log.Printf("ensureTodayAIInsight: marshal: %v", err)
		return ""
	}
	insight, err := ai.GenerateMorningBriefing(aiCfg.APIKey, aiCfg.Model, rawJSON)
	if err != nil {
		log.Printf("ensureTodayAIInsight: gemini: %v", err)
		return ""
	}
	if err := db.SaveAIBriefing(today, insight); err != nil {
		log.Printf("ensureTodayAIInsight: save: %v", err)
	}
	return insight
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
