package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"health-receiver/internal/handler"
	"health-receiver/internal/health"
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
		MorningCapHour:     getEnvInt("REPORT_MORNING_CAP", 0),
	}
	envAIDefaults := storage.AIConfig{
		APIKey:          os.Getenv("GEMINI_API_KEY"),
		Model:           getEnv("GEMINI_MODEL", "gemini-2.5-flash"),
		MaxOutputTokens: getEnvInt("GEMINI_MAX_TOKENS", 5000),
	}

	// HR zones for /health/workouts time-in-zone computation. Optional —
	// when unset, hr_z*_sec columns stay NULL and ingest still succeeds.
	hrZones, err := health.ParseHRZones(os.Getenv("HEALTH_HR_ZONES_BPM"))
	if err != nil {
		log.Printf("HEALTH_HR_ZONES_BPM: %v (workouts will ingest without zone breakdown)", err)
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
		legacyDB.EnsureAIBriefingBlocksTable()

		passwordHash := ""
		if uiPassword != "" {
			passwordHash = registry.HashPassword(uiPassword)
		}
		mgr.SetLegacyMode(legacyDB, apiKey, passwordHash)

		runSingleTenant(ctx, addr, baseURL, trustFwdAuth, apiKey, mgr, nil,
			legacyDB, "health", envNotifyDefaults, envAIDefaults, hrZones)
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

	// One-time backfill of installation-wide Gemini config for installs
	// that pre-date PR #16 (where AI settings were per-tenant). When the
	// global table is empty but an admin tenant already has gemini_* rows,
	// copy them up so non-admin tenants (Maria-style accounts) inherit.
	if err := migrateGlobalAIIfNeeded(ctx, reg, mgr, users); err != nil {
		log.Printf("global AI migration: %v", err)
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
		db.EnsureAIBriefingBlocksTable()
		startTenant(ctx, mgr, db, u.SchemaName, envNotifyDefaults, envAIDefaults)
	}

	if len(users) == 0 {
		log.Println("No users registered. Visit /setup to create your account.")
	}

	mux := http.NewServeMux()

	onNewData := func(db *storage.DB, dates []string) {
		// The tenant schema is encoded in the DB pool's search_path.
		// We rely on the manager to find the right backfill scheduler.
		for schema, tdb := range mgr.AllDBs() {
			if tdb == db {
				if fn := mgr.BackfillDatesFor(schema); fn != nil {
					fn(dates)
				}
				break
			}
		}
	}

	handler.New(mgr, onNewData, hrZones).Register(mux)

	uiHandler := ui.New(mgr, reg, trustFwdAuth)
	uiHandler.OnTenantCreated(func(schema string) {
		db, err := mgr.GetOrCreate(ctx, schema)
		if err != nil {
			log.Printf("onTenantCreated: open pool for %s: %v", schema, err)
			return
		}
		db.EnsureIndexes()
		db.EnsureAIBriefingsTable()
		db.EnsureAIBriefingBlocksTable()
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
	notifyDefaults storage.NotifyConfig, aiDefaults storage.AIConfig, hrZones health.HRZones) {

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
	maybeFireMorningReport := makeMorningTrigger(db, &morningLock, mgr, schema, notifyDefaults)
	backfillDatesFn := makeBackfillDatesFn(db, schema)
	onNewData := func(_ *storage.DB, dates []string) {
		backfillDatesFn(dates)
		go maybeFireMorningReport()
	}

	backfillFn := makeBackfillFn(db)
	testNotifyFn := makeTestNotifyFn(db, mgr, schema, notifyDefaults)

	mgr.RegisterCallbacks(schema, tenants.TenantCallbacks{
		Backfill:       backfillFn,
		BackfillDates:  backfillDatesFn,
		TestNotify:     testNotifyFn,
		NotifyDefaults: notifyDefaults,
		AIDefaults:     aiDefaults,
	})

	go runReportScheduler(db, mgr, schema, notifyDefaults)
	go runDailyQualityScan(db, schema, notifyDefaults)

	mux := http.NewServeMux()
	handler.New(mgr, onNewData, hrZones).Register(mux)
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

// startTenant launches the report scheduler for one tenant and runs a one-shot
// startup cache refresh.
func startTenant(ctx context.Context, mgr *tenants.Manager, db *storage.DB, schema string,
	notifyDefaults storage.NotifyConfig, aiDefaults storage.AIConfig) {

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
	maybeFireMorningReport := makeMorningTrigger(db, &morningLock, mgr, schema, notifyDefaults)

	backfillFn := makeBackfillFn(db)
	backfillDatesFn := makeBackfillDatesFn(db, schema)
	testNotifyFn := makeTestNotifyFn(db, mgr, schema, notifyDefaults)

	mgr.RegisterCallbacks(schema, tenants.TenantCallbacks{
		Backfill:       backfillFn,
		BackfillDates:  backfillDatesFn,
		TestNotify:     testNotifyFn,
		NotifyDefaults: notifyDefaults,
		AIDefaults:     aiDefaults,
	})

	_ = maybeFireMorningReport // triggered via onNewData in main mux
	go runReportScheduler(db, mgr, schema, notifyDefaults)
	go runDailyQualityScan(db, schema, notifyDefaults)
}

// makeBackfillFn returns the admin/import callback that recomputes caches.
// Per-POST cache work is now inline (storage.UpsertRecentCache + readiness
// recompute), so this is only invoked from the admin UI button or post-import.
func makeBackfillFn(db *storage.DB) func(bool) {
	var forceRunning, incrRunning int32
	return func(force bool) {
		if !force {
			if !atomic.CompareAndSwapInt32(&incrRunning, 0, 1) {
				log.Println("incremental backfill already running, skipping")
				return
			}
			go func() {
				defer atomic.StoreInt32(&incrRunning, 0)
				log.Println("incremental backfill: starting…")
				db.RunIncrementalBackfill()
				log.Println("incremental backfill: done")
			}()
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

// backfillDatesDebounce is the window over which incoming POST dates are
// accumulated before the safety-net rebuild fires. Long enough to absorb a
// full chunked iOS sync (typically tens of seconds), short enough that a
// failed inline UpsertRecentCache is repaired quickly.
const backfillDatesDebounce = 60 * time.Second

// makeBackfillDatesFn returns a debounced trigger that accumulates the union
// of dates reported by POST /health bursts and rebuilds caches for exactly
// that set after the burst settles. Replaces the old "last 7 days" safety net
// so backfills cover the actual dates that came in, not a fixed window.
func makeBackfillDatesFn(db *storage.DB, schema string) func([]string) {
	var (
		mu      sync.Mutex
		pending = make(map[string]struct{})
		timer   *time.Timer
	)
	flush := func() {
		mu.Lock()
		dates := make([]string, 0, len(pending))
		for d := range pending {
			dates = append(dates, d)
		}
		pending = make(map[string]struct{})
		timer = nil
		mu.Unlock()
		if len(dates) == 0 {
			return
		}
		log.Printf("[%s] backfill (date-aware): rebuilding %d date(s)", schema, len(dates))
		db.RunIncrementalBackfillForDates(dates)
		log.Printf("[%s] backfill (date-aware): done", schema)
	}
	return func(dates []string) {
		if len(dates) == 0 {
			return
		}
		mu.Lock()
		for _, d := range dates {
			if len(d) >= 10 {
				pending[d[:10]] = struct{}{}
			}
		}
		if timer == nil {
			timer = time.AfterFunc(backfillDatesDebounce, flush)
		} else {
			timer.Reset(backfillDatesDebounce)
		}
		mu.Unlock()
	}
}

// buildNotifyCfg copies storage NotifyConfig (DB-backed) into notify.Config
// (consumed by the bot). Centralised so adding a new field doesn't require
// finding all three call sites.
func buildNotifyCfg(db *storage.DB, c storage.NotifyConfig) notify.Config {
	cfg := notify.Config{
		Token:              c.Token,
		ChatID:             c.ChatID,
		Lang:               c.Lang,
		Timezone:           c.Timezone,
		MorningWeekdayHour: c.MorningWeekdayHour,
		MorningWeekendHour: c.MorningWeekendHour,
		EveningWeekdayHour: c.EveningWeekdayHour,
		EveningWeekendHour: c.EveningWeekendHour,
		MorningCapHour:     c.MorningCapHour,
	}
	if h, m, ok := db.GetTypicalWakeTime(14); ok {
		cfg.TypicalWakeHour = h
		cfg.TypicalWakeMinute = m
		cfg.TypicalWakeOK = true
	}
	return cfg
}

func makeTestNotifyFn(db *storage.DB, mgr *tenants.Manager, schema string, notifyDefaults storage.NotifyConfig) func(string) error {
	return func(kind string) error {
		scfg := db.GetNotifyConfig(notifyDefaults)
		if !scfg.Enabled() {
			return fmt.Errorf("Telegram not configured: set TELEGRAM_TOKEN and TELEGRAM_CHAT_ID")
		}
		ncfg := buildNotifyCfg(db, scfg)
		bot := notify.NewBot(ncfg.Token, ncfg.ChatID)
		if kind == "evening" {
			return notify.SendEvening(bot, db, ncfg)
		}
		// Resolve AI defaults fresh — picks up admin's global config
		// even if the env was empty at process start.
		ensureTodayAIInsight(db, mgr.AIDefaultsFor(context.Background(), schema), scfg.Lang)
		return notify.SendMorning(bot, db, ncfg)
	}
}

// makeMorningTrigger returns the opportunistic, ingest-driven morning report
// trigger. Fires from onNewData on every batch of incoming health data; bails
// quickly when conditions aren't met so it's safe to call frequently.
//
// Gates (in order):
//   1. AI configured (otherwise no insight to layer on top — we still want the
//      user to see something, but the rule-based path is the fallback scheduler's
//      job; this opportunistic trigger is the "AI is ready" path).
//   2. Past the morning floor (05:00 in tz) — don't ping at 3 a.m.
//   3. Not already sent today.
//   4. Today's step count > 300 — proxy for "user is up and moving". Without
//      this, the trigger could fire at 5:01 because the watch did a sync.
//   5. Sleep data has settled (storage.SleepSettled). This is the new gate:
//      previously we relied on the AI-insight check + step count, which let
//      the report fire while the watch was still recording the second half
//      of a wake-walk-sleep-again cycle. Now we wait for the watch to stop
//      writing.
func makeMorningTrigger(db *storage.DB, lock *int32, mgr *tenants.Manager, schema string, notifyDefaults storage.NotifyConfig) func() {
	return func() {
		if !atomic.CompareAndSwapInt32(lock, 0, 1) {
			return
		}
		defer atomic.StoreInt32(lock, 0)

		// AIDefaultsFor on each tick so the admin's installation-wide
		// Gemini key is honoured even if it was set after process start.
		aiDefaults := mgr.AIDefaultsFor(context.Background(), schema)
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

		if !cfg.Enabled() {
			return
		}
		ncfg := buildNotifyCfg(db, cfg)
		bot := notify.NewBot(ncfg.Token, ncfg.ChatID)
		sent, reason, err := notify.SendMorningSmart(bot, db, ncfg, false)
		if err != nil {
			log.Printf("morning trigger: send telegram: %v", err)
			return
		}
		if !sent {
			log.Printf("morning trigger: deferring — %s", reason)
			return
		}
		if err := db.MarkMorningReportSent(today); err != nil {
			log.Printf("morning trigger: mark sent: %v", err)
		}
		log.Printf("morning trigger: sent (reason=%s) for %s", reason, today)
	}
}

func runReportScheduler(db *storage.DB, mgr *tenants.Manager, schema string, defaults storage.NotifyConfig) {
	for {
		cfg := db.GetNotifyConfig(defaults)
		if !cfg.Enabled() {
			time.Sleep(5 * time.Minute)
			continue
		}

		ncfg := buildNotifyCfg(db, cfg)

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
		ncfg = buildNotifyCfg(db, cfg)
		bot := notify.NewBot(cfg.Token, cfg.ChatID)
		if isMorning {
			runMorningSmartRetry(bot, db, mgr, schema, ncfg)
		} else {
			log.Println("report scheduler: sending evening report…")
			if err := notify.SendEvening(bot, db, ncfg); err != nil {
				log.Printf("report scheduler: evening send error: %v", err)
			}
		}
	}
}

// runMorningSmartRetry implements the scheduler-side smart-retry loop. It is
// entered at the configured morning hour and ticks every 15 minutes until
// either the report has been sent (by this loop, or by the opportunistic
// ingest trigger) or the cap time is reached. At the cap, it force-sends with
// a stale-data banner so we never go a day without a morning report.
func runMorningSmartRetry(bot *notify.Bot, db *storage.DB, mgr *tenants.Manager, schema string, ncfg notify.Config) {
	const tick = 15 * time.Minute

	loc := time.Local
	if ncfg.Timezone != "" {
		if l, err := time.LoadLocation(ncfg.Timezone); err == nil {
			loc = l
		}
	}
	cap := ncfg.MorningCapTime(time.Now())
	log.Printf("morning smart-retry: window until %s", cap.Format("15:04"))

	// Weekly data-quality digest — fires once on the configured day-of-week
	// (default Monday). Sent before the morning report so it lands in its own
	// notification rather than mingling with sleep numbers.
	notify.MaybeSendWeeklyDigest(bot, db, ncfg)

	for {
		today := time.Now().In(loc).Format("2006-01-02")
		if db.HasSentMorningReport(today) {
			log.Println("morning smart-retry: already sent (likely by ingest trigger), exiting loop")
			return
		}

		// Try to (re)generate AI insight on each tick — cheap if cached.
		// Resolve AI defaults fresh per-tick so admin-managed global
		// config is honoured even when it was set mid-day.
		ensureTodayAIInsight(db, mgr.AIDefaultsFor(context.Background(), schema), ncfg.Lang)

		past := time.Now().After(cap)
		sent, reason, err := notify.SendMorningSmart(bot, db, ncfg, past)
		if err != nil {
			log.Printf("morning smart-retry: send error: %v", err)
		}
		if sent {
			if perr := db.MarkMorningReportSent(today); perr != nil {
				log.Printf("morning smart-retry: mark sent: %v", perr)
			}
			log.Printf("morning smart-retry: sent (reason=%s, forced=%v)", reason, past)
			return
		}
		if past {
			// Force-send returned not-sent without an error — only happens when
			// the bot is somehow disabled mid-loop. Bail to avoid spinning.
			log.Printf("morning smart-retry: past cap but not sent (reason=%s), giving up", reason)
			return
		}

		log.Printf("morning smart-retry: deferring (reason=%s), retry in %s", reason, tick)
		time.Sleep(tick)
	}
}

// runDailyQualityScan ticks once per day at 03:00 (REPORT_TZ or system local)
// and calls MarkSuspectPoints to flag z-score outliers in the last 7 days of
// autonomic metrics. Cheap (~one query per metric) and idempotent — re-running
// only flips quality='ok' rows whose deviation exceeds 3σ.
func runDailyQualityScan(db *storage.DB, schema string, defaults storage.NotifyConfig) {
	for {
		// Resolve tz per iteration so per-tenant settings.timezone overrides
		// the env default — same pattern as runReportScheduler.
		cfg := db.GetNotifyConfig(defaults)
		loc := time.Local
		if tz := cfg.Timezone; tz != "" {
			if l, err := time.LoadLocation(tz); err == nil {
				loc = l
			}
		}
		now := time.Now().In(loc)
		next := time.Date(now.Year(), now.Month(), now.Day(), 3, 0, 0, 0, loc)
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		log.Printf("[%s] daily quality scan: next run at %s", schema, next.Format("2006-01-02 15:04 MST"))
		time.Sleep(time.Until(next))

		flagged, err := db.MarkSuspectPoints(7, 3)
		if err != nil {
			log.Printf("[%s] daily quality scan: %v", schema, err)
			continue
		}
		total := 0
		for _, n := range flagged {
			total += n
		}
		if total == 0 {
			log.Printf("[%s] daily quality scan: no suspect points", schema)
		} else {
			log.Printf("[%s] daily quality scan: flagged %d points across %d metrics: %v", schema, total, len(flagged), flagged)
		}
	}
}

// ensureTodayAIInsight is a thin wrapper around storage.DB.EnsureTodayAIInsight
// kept for caller convenience.
func ensureTodayAIInsight(db *storage.DB, aiDefaults storage.AIConfig, lang string) string {
	return db.EnsureTodayAIInsight(db.GetAIConfig(aiDefaults), lang)
}

// migrateGlobalAIIfNeeded copies an admin tenant's per-tenant Gemini
// settings into installation-wide global_settings on first startup after
// the global-config switchover (PR #16). No-op when global already has
// a key (idempotent), when there's no admin user, or when the admin's
// own tenant has no gemini_api_key. Picks the first admin in
// creation order so the choice is deterministic.
func migrateGlobalAIIfNeeded(
	ctx context.Context, reg *registry.Registry, mgr *tenants.Manager, users []registry.User,
) error {
	if reg == nil {
		return nil
	}
	g := reg.GetAllGlobalSettings(ctx)
	if g["gemini_api_key"] != "" {
		return nil // already migrated or set explicitly
	}
	var admin *registry.User
	for i, u := range users {
		if u.IsAdmin {
			admin = &users[i]
			break
		}
	}
	if admin == nil {
		return nil
	}
	db, err := mgr.GetOrCreate(ctx, admin.SchemaName)
	if err != nil {
		return fmt.Errorf("open admin pool: %w", err)
	}
	apiKey := db.GetSetting("gemini_api_key", "")
	if apiKey == "" {
		return nil
	}
	out := map[string]string{"gemini_api_key": apiKey}
	if v := db.GetSetting("gemini_model", ""); v != "" {
		out["gemini_model"] = v
	}
	if v := db.GetSetting("gemini_max_tokens", ""); v != "" {
		out["gemini_max_tokens"] = v
	}
	if err := reg.SaveGlobalSettings(ctx, out); err != nil {
		return fmt.Errorf("save global: %w", err)
	}
	log.Printf("global AI migration: copied %d gemini_* keys from %s tenant",
		len(out), admin.SchemaName)
	return nil
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
