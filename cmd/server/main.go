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
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
	"health-receiver/internal/ui"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}
	addr := getEnv("ADDR", ":8080")
	apiKey := os.Getenv("API_KEY")
	uiPassword := os.Getenv("UI_PASSWORD")
	adminEmail := os.Getenv("ADMIN_EMAIL")
	trustFwdAuth := os.Getenv("TRUST_FORWARD_AUTH") == "true"
	baseURL := getEnv("BASE_URL", "http://localhost"+addr)

	// Env-level defaults for the first/only tenant.
	envNotifyDefaults := storage.NotifyConfig{
		Token:              os.Getenv("TELEGRAM_TOKEN"),
		ChatID:             os.Getenv("TELEGRAM_CHAT_ID"),
		Lang:               getEnv("REPORT_LANG", "en"),
		Timezone:           getEnv("REPORT_TZ", ""),
		MorningWeekdayHour: getEnvInt("REPORT_MORNING_WEEKDAY", 8),
		MorningWeekendHour: getEnvInt("REPORT_MORNING_WEEKEND", 9),
		EveningWeekdayHour: getEnvInt("REPORT_EVENING_WEEKDAY", 20),
		EveningWeekendHour: getEnvInt("REPORT_EVENING_WEEKEND", 21),
	}
	envAIDefaults := storage.AIConfig{
		APIKey:          os.Getenv("GEMINI_API_KEY"),
		Model:           getEnv("GEMINI_MODEL", "gemini-2.5-flash"),
		MaxOutputTokens: getEnvInt("GEMINI_MAX_TOKENS", 5000),
	}

	ctx := context.Background()

	// --- Registry ---
	reg, err := registry.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("init registry: %v", err)
	}
	defer reg.Close()

	mgr := tenants.New(reg, dbURL)
	defer mgr.Close()

	// Attempt to create health_registry schema and users table.
	schemaErr := reg.EnsureSchema(ctx)
	if schemaErr != nil {
		log.Printf("⚠️  MULTI-USER SETUP REQUIRED")
		log.Printf("    %v", schemaErr)
		log.Printf("    After running that SQL, restart the server.")
		log.Printf("    Falling back to single-user mode using API_KEY / UI_PASSWORD env vars.")

		// Fall back to single-user mode: use the DATABASE_URL schema directly.
		legacyDB, err := storage.New(ctx, dbURL)
		if err != nil {
			log.Fatalf("init db: %v", err)
		}
		legacyDB.EnsureIndexes()
		legacyDB.EnsureAIBriefingsTable()

		passwordHash := ""
		if uiPassword != "" {
			passwordHash = registry.HashPassword(uiPassword)
		}
		mgr.SetLegacyMode(legacyDB, apiKey, passwordHash)

		runSingleTenant(ctx, addr, baseURL, trustFwdAuth, apiKey, mgr, nil,
			legacyDB, "health", envNotifyDefaults, envAIDefaults)
		return
	}

	// Seed admin from env vars when the registry is empty and credentials are configured.
	// Covers two cases:
	//   1. Upgrade from single-user mode (health.metric_points exists, no users yet)
	//   2. Fresh install with credentials pre-set in .env / docker-compose environment
	// When neither API_KEY nor UI_PASSWORD is set, the setup wizard handles first-run.
	if reg.IsEmpty(ctx) && (apiKey != "" || uiPassword != "") {
		log.Println("Registry empty — seeding admin user from API_KEY / UI_PASSWORD env vars…")
		passwordHash := ""
		if uiPassword != "" {
			passwordHash = registry.HashPassword(uiPassword)
		}
		const adminSchema = "health"
		if err := reg.MigrateFromEnv(ctx, apiKey, passwordHash, adminSchema, adminEmail); err != nil {
			log.Printf("seed admin: %v", err)
		} else {
			// Create schema + tables if this is a fresh install (legacy upgrade already has them).
			if err := mgr.CreateUserSchema(ctx, adminSchema); err != nil {
				log.Printf("ensure schema for admin: %v", err)
			}
			log.Printf("Admin user created (username: admin, schema: %s)", adminSchema)
		}
	}

	// Load all registered users and initialise their DB pools.
	users, err := reg.ListUsers(ctx)
	if err != nil {
		log.Fatalf("list users: %v", err)
	}

	for _, u := range users {
		db, err := mgr.GetOrCreate(ctx, u.SchemaName)
		if err != nil {
			log.Printf("open pool for %s: %v", u.SchemaName, err)
			continue
		}
		if err := db.EnsureAllTables(); err != nil {
			log.Printf("ensure tables for %s: %v", u.SchemaName, err)
		}
		db.EnsureIndexes()
		db.EnsureAIBriefingsTable()
		startTenant(ctx, mgr, db, u.SchemaName, envNotifyDefaults, envAIDefaults)
	}

	if len(users) == 0 {
		log.Println("No users registered. Visit /setup to create your account.")
	}

	mux := http.NewServeMux()

	onNewData := func(db *storage.DB) {
		// The tenant schema is encoded in the DB pool's search_path.
		// We rely on the manager to find the right backfill scheduler.
		for schema, tdb := range mgr.AllDBs() {
			if tdb == db {
				if fn := mgr.BackfillFor(schema); fn != nil {
					fn(false)
				}
				break
			}
		}
	}

	handler.New(mgr, onNewData).Register(mux)

	uiHandler := ui.New(mgr, reg, trustFwdAuth)
	uiHandler.OnTenantCreated(func(schema string) {
		db, err := mgr.GetOrCreate(ctx, schema)
		if err != nil {
			log.Printf("onTenantCreated: open pool for %s: %v", schema, err)
			return
		}
		db.EnsureIndexes()
		db.EnsureAIBriefingsTable()
		startTenant(ctx, mgr, db, schema, envNotifyDefaults, envAIDefaults)
	})
	uiHandler.Register(mux)
	mcpserver.Register(mux, mgr, baseURL)

	logged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		mux.ServeHTTP(w, r)
		log.Printf("%s %s %s %v", r.RemoteAddr, r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})

	log.Printf("listening on %s (multi-user mode, %d user(s))", addr, len(users))
	log.Printf("MCP endpoint: %s/mcp", baseURL)
	if err := http.ListenAndServe(addr, logged); err != nil {
		log.Fatalf("server: %v", err)
	}
}

// runSingleTenant runs the server in legacy single-user mode.
func runSingleTenant(ctx context.Context, addr, baseURL string, trustFwdAuth bool, apiKey string,
	mgr *tenants.Manager, reg *registry.Registry,
	db *storage.DB, schema string,
	notifyDefaults storage.NotifyConfig, aiDefaults storage.AIConfig) {

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

	var morningLock int32
	maybeFireMorningReport := makeMorningTrigger(db, &morningLock, aiDefaults, notifyDefaults)
	onNewData := func(_ *storage.DB) {
		sched.schedule()
		go maybeFireMorningReport()
	}

	backfillFn := makeBackfillFn(db, sched)
	testNotifyFn := makeTestNotifyFn(db, notifyDefaults, aiDefaults)

	mgr.RegisterCallbacks(schema, tenants.TenantCallbacks{
		Backfill:       backfillFn,
		TestNotify:     testNotifyFn,
		NotifyDefaults: notifyDefaults,
		AIDefaults:     aiDefaults,
	})

	go runReportScheduler(db, notifyDefaults, aiDefaults)

	mux := http.NewServeMux()
	handler.New(mgr, onNewData).Register(mux)
	ui.New(mgr, reg, trustFwdAuth).Register(mux)
	mcpserver.Register(mux, mgr, baseURL)

	logged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		mux.ServeHTTP(w, r)
		log.Printf("%s %s %s %v", r.RemoteAddr, r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})

	log.Printf("listening on %s (single-user legacy mode)", addr)
	log.Printf("MCP endpoint: %s/mcp", baseURL)
	if err := http.ListenAndServe(addr, logged); err != nil {
		log.Fatalf("server: %v", err)
	}
}

// startTenant launches backfill scheduler and report scheduler for one tenant.
func startTenant(ctx context.Context, mgr *tenants.Manager, db *storage.DB, schema string,
	notifyDefaults storage.NotifyConfig, aiDefaults storage.AIConfig) {

	sched := newBackfillScheduler(db, 2*time.Minute)
	go func() {
		time.Sleep(5 * time.Second)
		force := db.NeedsForceBackfill()
		if force {
			log.Printf("[%s] startup: caches empty, rebuilding all…", schema)
		} else {
			log.Printf("[%s] startup: incremental cache refresh…", schema)
		}
		db.BackfillAggregates(force)
		db.BackfillScores(force)
		log.Printf("[%s] startup: cache refresh done", schema)
	}()

	var morningLock int32
	maybeFireMorningReport := makeMorningTrigger(db, &morningLock, aiDefaults, notifyDefaults)

	backfillFn := makeBackfillFn(db, sched)
	testNotifyFn := makeTestNotifyFn(db, notifyDefaults, aiDefaults)

	mgr.RegisterCallbacks(schema, tenants.TenantCallbacks{
		Backfill:   backfillFn,
		TestNotify: testNotifyFn,
		NotifyDefaults: notifyDefaults,
		AIDefaults:     aiDefaults,
	})

	_ = maybeFireMorningReport // triggered via onNewData in main mux
	go runReportScheduler(db, notifyDefaults, aiDefaults)
}

func makeBackfillFn(db *storage.DB, sched *backfillScheduler) func(bool) {
	var forceRunning int32
	return func(force bool) {
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
}

func makeTestNotifyFn(db *storage.DB, notifyDefaults storage.NotifyConfig, aiDefaults storage.AIConfig) func(string) error {
	return func(kind string) error {
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
		ensureTodayAIInsight(db, aiDefaults, scfg.Lang)
		return notify.SendMorning(bot, db, ncfg)
	}
}

func makeMorningTrigger(db *storage.DB, lock *int32, aiDefaults storage.AIConfig, notifyDefaults storage.NotifyConfig) func() {
	return func() {
		if !atomic.CompareAndSwapInt32(lock, 0, 1) {
			return
		}
		defer atomic.StoreInt32(lock, 0)

		aiCfg := db.GetAIConfig(aiDefaults)
		if !aiCfg.Enabled() {
			return
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

		if now.Hour() < 5 {
			return
		}
		if db.HasSentMorningReport(today) {
			return
		}
		if db.GetTodayStepCount(today) < 300 {
			return
		}

		if insight := ensureTodayAIInsight(db, aiCfg, cfg.Lang); insight == "" {
			log.Println("morning trigger: AI insight unavailable, aborting")
			return
		}

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
}

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

func (s *backfillScheduler) schedule() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

func (s *backfillScheduler) run() {
	for range s.trigger {
		time.Sleep(s.delay)
		for len(s.trigger) > 0 {
			<-s.trigger
		}
		log.Println("scheduler: running incremental backfill…")
		s.db.RunIncrementalBackfill()
		log.Println("scheduler: done")
	}
}

func runReportScheduler(db *storage.DB, defaults storage.NotifyConfig, aiDefaults storage.AIConfig) {
	for {
		cfg := db.GetNotifyConfig(defaults)
		if !cfg.Enabled() {
			time.Sleep(5 * time.Minute)
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

		cfg = db.GetNotifyConfig(defaults)
		if !cfg.Enabled() {
			continue
		}
		bot := notify.NewBot(cfg.Token, cfg.ChatID)
		if isMorning {
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
			ensureTodayAIInsight(db, aiDefaults, cfg.Lang)
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

func ensureTodayAIInsight(db *storage.DB, aiDefaults storage.AIConfig, lang string) string {
	aiCfg := db.GetAIConfig(aiDefaults)
	if !aiCfg.Enabled() {
		return ""
	}
	today := time.Now().Format("2006-01-02")
	if existing := db.GetAIBriefing(today, lang); existing != "" {
		return existing
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
	insight, fullPayload, err := ai.GenerateMorningBriefing(aiCfg.APIKey, aiCfg.Model, aiCfg.MaxOutputTokens, rawJSON, lang)
	if err != nil {
		log.Printf("ensureTodayAIInsight: gemini: %v", err)
		return ""
	}
	if err := db.SaveAIBriefing(today, insight, fullPayload, lang); err != nil {
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
