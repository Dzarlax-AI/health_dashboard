package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	// Embed the IANA timezone database directly in the binary. The
	// build is CGO_ENABLED=0 and the alpine runtime image does not
	// install the `tzdata` package, so without this import every
	// time.LoadLocation("Europe/Belgrade") (and friends) fails with
	// "unknown time zone …" and falls back to UTC. That broke the
	// EnergyBank v2 orchestrator (PR #38) on the first deploy and
	// silently makes report scheduling, morning-cap timing, and
	// energy_snapshots.date all run under UTC across the whole
	// codebase. Adds ~450KB to the binary; cheaper and more portable
	// than installing tzdata into the runtime image.
	_ "time/tzdata"

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
		legacyDB.EnsureEnergySnapshotsTable()
		legacyDB.EnsureReadinessRedesignTables()
		legacyDB.EnsureSubjectiveCheckinsTable()

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
		db.EnsureEnergySnapshotsTable()
		db.EnsureReadinessRedesignTables()
		db.EnsureSubjectiveCheckinsTable()
		startTenant(ctx, mgr, reg, db, u.SchemaName, envNotifyDefaults, envAIDefaults, baseURL)
	}

	if len(users) == 0 {
		log.Println("No users registered. Visit /setup to create your account.")
	}

	mux := http.NewServeMux()

	// EnergyBank v2 orchestrator: runs in parallel with the v1
	// dashboard rendering. Snapshots accumulate silently for
	// observation; PR8 will flip the UI over once v2 is validated.
	energyV2 := storage.NewEnergyV2Orchestrator()

	onNewData := func(db *storage.DB, dates []string) {
		// The tenant schema is encoded in the DB pool's search_path.
		// We rely on the manager to find the right backfill scheduler.
		for schema, tdb := range mgr.AllDBs() {
			if tdb == db {
				if fn := mgr.BackfillDatesFor(schema); fn != nil {
					fn(dates)
				}
				energyV2.Trigger(ctx, db, schema,
					tenantTZOrUTC(db, envNotifyDefaults, schema))
				// Ingest-driven morning report trigger: fires earlier
				// than the scheduled morning hour when fresh sleep +
				// activity data arrives, mirroring the single-tenant
				// path in runSingleTenant.onNewData. Goroutine so a
				// slow Telegram send never blocks the 200-response.
				if trigger := mgr.MorningTriggerFor(schema); trigger != nil {
					go trigger()
				}
				break
			}
		}
	}

	handler.New(mgr, onNewData, hrZones).Register(mux)

	uiHandler := ui.New(mgr, reg, trustFwdAuth)
	uiHandler.ConfigureWebhook(notify.NewTelegramWebhookRegistrar(), baseURL)
	uiHandler.OnTenantCreated(func(schema string) {
		db, err := mgr.GetOrCreate(ctx, schema)
		if err != nil {
			log.Printf("onTenantCreated: open pool for %s: %v", schema, err)
			return
		}
		db.EnsureIndexes()
		db.EnsureAIBriefingsTable()
		db.EnsureAIBriefingBlocksTable()
		db.EnsureEnergySnapshotsTable()
		db.EnsureReadinessRedesignTables()
		db.EnsureSubjectiveCheckinsTable()
		startTenant(ctx, mgr, reg, db, schema, envNotifyDefaults, envAIDefaults, baseURL)
	})
	uiHandler.Register(mux)
	mcpserver.Register(mux, mgr, baseURL)

	registerCheckinWebhook(mux, mgr, reg, envNotifyDefaults, baseURL)

	// Crash-recovery: any webhook_status rows still in `pending` were
	// left there by a previous process that died (kill -9 / OOM /
	// container restart) before the registrar goroutine could
	// finalise. Transition those to failed:restart_interrupted so the
	// operator sees a clear badge + Retry button. Best-effort —
	// malformed rows are left untouched and logged; we never crash
	// startup on bad data.
	if reg != nil {
		if reset, skipped, err := reg.ResetPendingOnStartup(ctx); err != nil {
			log.Printf("webhook recovery: %v (continuing)", err)
		} else {
			log.Printf("webhook recovery: reset=%d skipped=%d", reset, skipped)
		}
	}

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

	var morningSendMu sync.Mutex
	maybeFireMorningReport := makeMorningTrigger(db, &morningSendMu, mgr, reg, schema, notifyDefaults)
	backfillDatesFn := makeBackfillDatesFn(db, schema)
	// EnergyBank v2 orchestrator: same role as in multi-tenant mode —
	// passive snapshot accumulation alongside the live v1 dashboard.
	energyV2 := storage.NewEnergyV2Orchestrator()
	onNewData := func(_ *storage.DB, dates []string) {
		backfillDatesFn(dates)
		energyV2.Trigger(ctx, db, schema, tenantTZOrUTC(db, notifyDefaults, schema))
		go maybeFireMorningReport()
	}

	backfillFn := makeBackfillFn(db)
	testNotifyFn := makeTestNotifyFn(db, mgr, schema, notifyDefaults)

	mgr.RegisterCallbacks(schema, tenants.TenantCallbacks{
		Backfill:       backfillFn,
		BackfillDates:  backfillDatesFn,
		MorningSendMu:  &morningSendMu,
		TestNotify:     testNotifyFn,
		NotifyDefaults: notifyDefaults,
		AIDefaults:     aiDefaults,
	})

	go runReportScheduler(db, mgr, reg, schema, notifyDefaults, baseURL)
	go runDailyQualityScan(db, schema, notifyDefaults)

	mux := http.NewServeMux()
	handler.New(mgr, onNewData, hrZones).Register(mux)
	legacyUI := ui.New(mgr, reg, trustFwdAuth)
	legacyUI.ConfigureWebhook(notify.NewTelegramWebhookRegistrar(), baseURL)
	legacyUI.Register(mux)
	mcpserver.Register(mux, mgr, baseURL)
	registerCheckinWebhook(mux, mgr, reg, notifyDefaults, baseURL)

	// Crash-recovery (legacy path mirrors multi-tenant — see main()).
	if reg != nil {
		if reset, skipped, err := reg.ResetPendingOnStartup(ctx); err != nil {
			log.Printf("webhook recovery: %v (continuing)", err)
		} else {
			log.Printf("webhook recovery: reset=%d skipped=%d", reset, skipped)
		}
	}

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
// startup cache refresh. baseURL is plumbed through to the scheduler so
// the EnergyBank-backfill onboarding nudge can embed a clickable
// link back to the tenant's /settings page.
func startTenant(ctx context.Context, mgr *tenants.Manager, reg *registry.Registry, db *storage.DB, schema string,
	notifyDefaults storage.NotifyConfig, aiDefaults storage.AIConfig, baseURL string) {

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

	var morningSendMu sync.Mutex
	maybeFireMorningReport := makeMorningTrigger(db, &morningSendMu, mgr, reg, schema, notifyDefaults)

	backfillFn := makeBackfillFn(db)
	backfillDatesFn := makeBackfillDatesFn(db, schema)
	testNotifyFn := makeTestNotifyFn(db, mgr, schema, notifyDefaults)

	mgr.RegisterCallbacks(schema, tenants.TenantCallbacks{
		Backfill:       backfillFn,
		BackfillDates:  backfillDatesFn,
		MorningTrigger: maybeFireMorningReport,
		MorningSendMu:  &morningSendMu,
		TestNotify:     testNotifyFn,
		NotifyDefaults: notifyDefaults,
		AIDefaults:     aiDefaults,
	})

	go runReportScheduler(db, mgr, reg, schema, notifyDefaults, baseURL)
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
// tenantTZOrUTC resolves the tenant's report timezone, falling back to
// "UTC" (and warning once on first observation) when neither the
// tenant's settings nor envNotifyDefaults supply one. The fallback
// keeps EnergyBank v2 functional on tenants that haven't configured a
// TZ yet — running under UTC is a defensible default — while the
// warning surfaces the misconfiguration so an operator can fix it.
//
// Used at the EnergyBank v2 trigger boundary in onNewData. Both
// downstream consumers (ComputeBankForToday and UpsertEnergySnapshot)
// call time.LoadLocation, which silently coerces "" to UTC; the
// boundary normalisation here makes that coercion explicit and
// auditable rather than implicit.
var tenantTZWarned sync.Map // schema → struct{}, "warned about empty TZ already"

func tenantTZOrUTC(db *storage.DB, defaults storage.NotifyConfig, schema string) string {
	tz := db.GetNotifyConfig(defaults).Timezone
	if tz != "" {
		return tz
	}
	if _, loaded := tenantTZWarned.LoadOrStore(schema, struct{}{}); !loaded {
		log.Printf("[ENERGY_V2] schema=%s timezone unset (no settings.timezone, no REPORT_TZ env) — falling back to UTC", schema)
	}
	return "UTC"
}

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
		// Test-notify renders the morning report from whatever AI
		// blocks are already cached for today — no Gemini call.
		//
		// Before the v2 verdict cutover (PR #47) the recommendation
		// hash was stable (action_verdict was always "rest" in v1's
		// degenerate state), so calling ensureTodayAIInsight here
		// was a near-free no-op. Post-cutover, action_verdict
		// realistically rotates 1-3 times per day as bank crosses
		// personal-band thresholds — and ensureTodayAIInsight then
		// regenerates the recommendation block on every test click.
		// That burned Gemini quota for what users reasonably expect
		// to be a free "preview the morning report" button.
		//
		// The live morning scheduler still calls EnsureTodayAIInsight
		// on its own tick (runMorningSmartRetry in this file), so the
		// real morning report still gets the freshest AI blocks. The
		// /api/ai-briefing endpoint also still drives async regen
		// when the dashboard polls a cold cache. The "what's my
		// state right now" query path (planned, not yet built) will
		// regenerate intentionally. Only the explicit "test"
		// admin button is now cache-only.
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

// registerCheckinWebhook mounts the Telegram callback handler on mux.
// Three-step secret lookup via registry.ResolveOrGenerateWebhookSecrets:
//   1. health_registry.global_settings (persisted from previous boot)
//   2. env (TELEGRAM_WEBHOOK_SECRET / TELEGRAM_WEBHOOK_TOKEN_HEADER)
//   3. generate fresh pair and persist
//
// When reg is nil (legacy bootstrap without registry — e.g. forced
// single-user mode), falls back to env-only: webhook stays disabled
// if env not set, preserving the previous behaviour.
//
// baseURL is the publicly-reachable scheme+host the webhook URL is
// constructed from. If it's not HTTPS we log a startup warning —
// Telegram setWebhook will reject every Register call, and surfacing
// this at boot beats letting the operator discover it on their first
// failed save.
//
// Used from both runSingleTenant (legacy mode) and the multi-tenant
// mux build so neither path falls through silently.
func registerCheckinWebhook(mux *http.ServeMux, mgr *tenants.Manager, reg *registry.Registry, notifyDefaults storage.NotifyConfig, baseURL string) {
	envSecret := os.Getenv("TELEGRAM_WEBHOOK_SECRET")
	envToken := os.Getenv("TELEGRAM_WEBHOOK_TOKEN_HEADER")

	var secret, tokenHeader, source string
	if reg != nil {
		secret, tokenHeader, source = reg.ResolveOrGenerateWebhookSecrets(context.Background(), envSecret, envToken)
	} else {
		// Legacy bootstrap path: registry unavailable. Use env only;
		// no auto-generation (we have nowhere to persist generated
		// values). When env not set, webhook stays disabled.
		secret, tokenHeader, source = envSecret, envToken, "env"
		if secret == "" || tokenHeader == "" {
			return
		}
	}
	if secret == "" || tokenHeader == "" {
		log.Printf("registerCheckinWebhook: secrets unavailable (source=%s); webhook disabled", source)
		return
	}

	// Defensive: Telegram setWebhook rejects non-HTTPS URLs. If the
	// operator's BASE_URL isn't https://, the handler still mounts
	// (so cleanup deleteWebhook calls keep working — those don't
	// need a URL), but the registrar will fail every Register call
	// with reason=bad_request. Surface this at startup so the
	// operator sees it without waiting for the first save → failed
	// → log dig.
	if !strings.HasPrefix(baseURL, "https://") {
		log.Printf("WARN registerCheckinWebhook: BASE_URL=%q is not HTTPS — Telegram setWebhook will reject Register calls. Set BASE_URL=https://<your-domain> in env. (Webhook handler still mounted; existing registrations work, but rotation/new-token saves will fail until BASE_URL is fixed.)", baseURL)
	}

	mux.HandleFunc("/api/telegram/webhook/", notify.NewWebhookHandler(notify.WebhookConfig{
		Secret:      secret,
		TokenHeader: tokenHeader,
		TenantFinder: func(chat string) (notify.CheckinTenant, bool) {
			db, schema, ok := mgr.DBForTelegramChatID(context.Background(), notifyDefaults, chat)
			if !ok {
				return notify.CheckinTenant{}, false
			}
			cfg := db.GetNotifyConfig(notifyDefaults)
			loc := time.Local
			if l, err := time.LoadLocation(cfg.Timezone); err == nil && cfg.Timezone != "" {
				loc = l
			}
			bot := notify.NewBot(cfg.Token, cfg.ChatID)
			return notify.CheckinTenant{
				Schema:    schema,
				Lang:      cfg.Lang,
				TodayInTZ: time.Now().In(loc).Format("2006-01-02"),
				Router:    &liveCheckinRouter{db: db, bot: bot, triggerReport: makeReportTrigger(mgr, schema, notifyDefaults)},
			}, true
		},
	}))
	log.Printf("Telegram webhook registered at /api/telegram/webhook/<secret> (source=%s)", source)
}

// liveCheckinRouter is the production notify.CheckinAnswerRouter for
// one tenant. Created per inbound webhook update by the TenantFinder
// closure so each instance binds the right DB + Bot + report trigger.
type liveCheckinRouter struct {
	db            *storage.DB
	bot           *notify.Bot
	triggerReport func()
}

func (r *liveCheckinRouter) SaveAnswer(date, source, answer string, answeredAt time.Time) (string, error) {
	return r.db.SaveCheckinAnswer(date, source, answer, answeredAt)
}
func (r *liveCheckinRouter) AnswerCallbackQuery(qid, text string) error {
	return r.bot.AnswerCallbackQuery(qid, text)
}
func (r *liveCheckinRouter) TriggerReport(_ string) {
	if r.triggerReport != nil {
		go r.triggerReport()
	}
}

// makeReportTrigger captures the dependencies needed to (re)run the
// morning trigger for a tenant. Used by the webhook router to fire
// the report async after a successful in-time answer.
//
// Acquires the same per-tenant sendMu as the scheduler + ingest paths
// so the three-way race (webhook answer arriving while scheduler is
// mid-tick while a fresh ingest fires) can't produce duplicate sends.
// Sendmu nil → no other senders exist (legacy single-mode), original
// lock-free behaviour preserved.
func makeReportTrigger(mgr *tenants.Manager, schema string, defaults storage.NotifyConfig) func() {
	return func() {
		db, err := mgr.GetOrCreate(context.Background(), schema)
		if err != nil || db == nil {
			return
		}
		scfg := db.GetNotifyConfig(defaults)
		if !scfg.Enabled() {
			return
		}
		loc := time.Local
		if l, lerr := time.LoadLocation(scfg.Timezone); lerr == nil && scfg.Timezone != "" {
			loc = l
		}
		today := time.Now().In(loc).Format("2006-01-02")
		ncfg := buildNotifyCfg(db, scfg)
		bot := notify.NewBot(ncfg.Token, ncfg.ChatID)

		sendMu := mgr.MorningSendMuFor(schema)
		if sendMu != nil {
			sendMu.Lock()
			defer sendMu.Unlock()
		}
		if db.HasSentMorningReport(today) {
			return
		}
		sent, reason, err := notify.SendMorningSmart(bot, db, ncfg, false)
		if err != nil {
			log.Printf("checkin-trigger: send: %v", err)
			return
		}
		if sent {
			if perr := db.MarkMorningReportSent(today); perr != nil {
				log.Printf("checkin-trigger: mark sent: %v", perr)
			}
			log.Printf("checkin-trigger: sent (reason=%s) for %s", reason, today)
		}
	}
}

func makeMorningTrigger(db *storage.DB, sendMu *sync.Mutex, mgr *tenants.Manager, reg *registry.Registry, schema string, notifyDefaults storage.NotifyConfig) func() {
	return func() {
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

		// Route through the same check-in gate as the scheduler so an
		// ingest-driven send doesn't bypass the subjective prompt path.
		// Single-shot: if the gate picks Wait, we return and let the
		// next ingest (or the scheduler at morning_hour) retry. Cap is
		// notify.EffectiveMorningCap — honours row.ExpiresAt over a freshly-
		// floored cap so an ingest-saved prompt deadline isn't silently
		// extended on later ticks.
		settled := db.SleepSettled(today).Settled
		row, rerr := db.GetTodayCheckin(today, storage.CheckinSourceTelegram)
		if rerr != nil {
			log.Printf("morning trigger: read checkin: %v", rerr)
			row = nil
		}
		cap := notify.EffectiveMorningCap(ncfg.MorningCapTime(now), row)
		checkinEnabled := morningCheckinEnabled(reg)

		inputs := notify.MorningGateInputs{
			Now:            now,
			Cap:            cap,
			SleepSettled:   settled,
			HasCheckin:     row != nil,
			CheckinEnabled: checkinEnabled,
		}
		if row != nil {
			inputs.CheckinStatus = row.Status
		}
		action := notify.DecideMorningAction(inputs)
		log.Printf("morning trigger: action=%s settled=%v checkin_status=%q", action, settled, inputs.CheckinStatus)

		switch action {
		case notify.MorningActionNoop, notify.MorningActionWait:
			return

		case notify.MorningActionPrompt:
			// Serialise prompt sends across concurrent ingest goroutines
			// (multiple POST /health within seconds) and with the
			// scheduler. SendCheckinPrompt POSTs to Telegram BEFORE the
			// SaveCheckinPrompted upsert, so a parallel goroutine that
			// also read row==nil would dup the Telegram message before
			// either save commits. Re-read inside the lock to drop the
			// loser of the race.
			sendMu.Lock()
			defer sendMu.Unlock()
			if r2, _ := db.GetTodayCheckin(today, storage.CheckinSourceTelegram); r2 != nil {
				log.Println("morning trigger: prompt already sent by other path, skipping")
				return
			}
			if err := notify.SendCheckinPrompt(bot, db, ncfg.Lang, today, now, cap); err != nil {
				log.Printf("morning trigger: prompt: %v", err)
				return
			}
			log.Printf("morning trigger: check-in prompt sent for %s", today)
			return

		case notify.MorningActionExpireAndForce:
			if _, err := db.ExpireCheckin(today, storage.CheckinSourceTelegram, now); err != nil {
				log.Printf("morning trigger: expire checkin: %v", err)
			}
			fallthrough

		case notify.MorningActionForce, notify.MorningActionSendReport:
			force := action == notify.MorningActionForce || action == notify.MorningActionExpireAndForce
			// Critical section: serialise the HasSent re-check + Send +
			// MarkSent triple with the scheduler so the two paths can't
			// both observe HasSent=false in the narrow window between
			// the outer check and the actual Telegram POST.
			sendMu.Lock()
			defer sendMu.Unlock()
			if db.HasSentMorningReport(today) {
				return
			}
			sent, reason, err := notify.SendMorningSmartOpts(bot, db, ncfg, notify.MorningSendOpts{
				Force:          force,
				CheckinExpired: action == notify.MorningActionExpireAndForce,
			})
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
			log.Printf("morning trigger: sent (reason=%s, forced=%v, action=%s)", reason, force, action)
		}
	}
}


// morningCheckinEnabled mirrors the feature-flag check in
// runMorningSmartRetry exactly. Source of truth depends on whether the
// registry is initialised: when reg != nil, health_registry.global_settings
// is authoritative (lazy-init at startup writes there); otherwise env
// vars are the only source the webhook registrar could have used.
// Drift between the two callers silently disables check-in, so the
// implementation lives in one place.
func morningCheckinEnabled(reg *registry.Registry) bool {
	if reg != nil {
		return reg.GetGlobalSetting(context.Background(), "webhook_secret") != "" &&
			reg.GetGlobalSetting(context.Background(), "webhook_token_header") != ""
	}
	return os.Getenv("TELEGRAM_WEBHOOK_SECRET") != "" &&
		os.Getenv("TELEGRAM_WEBHOOK_TOKEN_HEADER") != ""
}

func runReportScheduler(db *storage.DB, mgr *tenants.Manager, reg *registry.Registry, schema string, defaults storage.NotifyConfig, baseURL string) {
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
			runMorningSmartRetry(bot, db, mgr, reg, schema, ncfg, baseURL)
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
func runMorningSmartRetry(bot *notify.Bot, db *storage.DB, mgr *tenants.Manager, reg *registry.Registry, schema string, ncfg notify.Config, baseURL string) {
	const tick = 15 * time.Minute

	loc := time.Local
	if ncfg.Timezone != "" {
		if l, err := time.LoadLocation(ncfg.Timezone); err == nil {
			loc = l
		}
	}
	// sendMu is the per-tenant TOCTOU lock shared with the ingest
	// trigger. nil for tenants without a registered mutex — those
	// retain the original lock-free behaviour (single-sender path
	// only). Both code paths grab it before the HasSent re-check.
	sendMu := mgr.MorningSendMuFor(schema)
	// entryCap is computed ONCE at scheduler entry and reused across
	// every tick. Per-tick MorningCapTime would slide forward each
	// iteration when no checkin row exists (MinPromptWindow floor
	// always pushes cap = now + 60min), so the force-send branch
	// would never see past=true and the loop would never terminate
	// on watch-off days. Fixed entry cap lets the gate hit past=true
	// at exactly the moment the user-promised deadline elapses.
	// Pinned by codex review on PR #124.
	entryCap := ncfg.MorningCapTime(time.Now())
	log.Printf("morning smart-retry: window until %s", entryCap.Format("15:04"))

	// Proactive notifications — registered at init() time by each
	// rule's own file (weekly digest, EnergyBank backfill nudge,
	// future illness / HRV crash / streak rules). MaybeFireAll
	// walks the registry, honours per-rule cadence + eligibility,
	// and never bubbles errors so a misbehaving rule can't break
	// the morning report path. Sent before the morning report so
	// each lands in its own Telegram notification rather than
	// mingling with sleep numbers.
	//
	// See internal/notify/proactive.go for the registration
	// contract and the migration story (LegacyKey field is the
	// "don't re-fire today" lifeline for pre-framework data).
	notify.MaybeFireAll(bot, db, ncfg, baseURL)

	for {
		today := time.Now().In(loc).Format("2006-01-02")
		if db.HasSentMorningReport(today) {
			log.Println("morning smart-retry: already sent (likely by ingest trigger or webhook), exiting loop")
			return
		}

		// Try to (re)generate AI insight on each tick — cheap if cached.
		// Resolve AI defaults fresh per-tick so admin-managed global
		// config is honoured even when it was set mid-day.
		ensureTodayAIInsight(db, mgr.AIDefaultsFor(context.Background(), schema), ncfg.Lang)

		// Resolve all the per-tick state the gate consults. The check-in
		// row lookup tolerates "no row" via GetTodayCheckin returning
		// (nil, nil); any DB error is logged and treated as "no row" so
		// the scheduler doesn't get stuck.
		now := time.Now()
		settled := db.SleepSettled(today).Settled
		row, rerr := db.GetTodayCheckin(today, storage.CheckinSourceTelegram)
		if rerr != nil {
			log.Printf("morning smart-retry: read checkin: %v", rerr)
			row = nil
		}
		// Per-tick cap: prefers row.ExpiresAt over the FIXED entryCap
		// (computed once at scheduler entry, never recomputed). The
		// nil-row case falls through to entryCap so the force-send
		// branch can actually fire when sleep never settles — a per-
		// tick MorningCapTime call would re-floor cap to now+60min
		// every iteration, deferring the loop indefinitely. See
		// notify.EffectiveMorningCap.
		effectiveCap := notify.EffectiveMorningCap(entryCap, row)
		checkinEnabled := morningCheckinEnabled(reg)

		inputs := notify.MorningGateInputs{
			Now:            now,
			Cap:            effectiveCap,
			SleepSettled:   settled,
			HasCheckin:     row != nil,
			CheckinEnabled: checkinEnabled,
		}
		if row != nil {
			inputs.CheckinStatus = row.Status
		}
		action := notify.DecideMorningAction(inputs)
		log.Printf("morning smart-retry: action=%s settled=%v checkin_status=%q", action, settled, inputs.CheckinStatus)

		switch action {
		case notify.MorningActionNoop:
			return

		case notify.MorningActionWait:
			time.Sleep(tick)
			continue

		case notify.MorningActionPrompt:
			// Serialise prompt sends with the ingest trigger so we
			// don't deliver two Telegram prompts when concurrent
			// goroutines both saw row==nil. Re-read the row INSIDE
			// the mutex; SendCheckinPrompt POSTs to Telegram before
			// SaveCheckinPrompted writes the row, so without the
			// double-check the second sender would also POST before
			// the first sender's SaveCheckinPrompted commits.
			if sendMu != nil {
				sendMu.Lock()
			}
			r2, _ := db.GetTodayCheckin(today, storage.CheckinSourceTelegram)
			if r2 != nil {
				if sendMu != nil {
					sendMu.Unlock()
				}
				log.Println("morning smart-retry: prompt already sent by other path, skipping")
				time.Sleep(tick)
				continue
			}
			err := notify.SendCheckinPrompt(bot, db, ncfg.Lang, today, now, effectiveCap)
			if sendMu != nil {
				sendMu.Unlock()
			}
			if err != nil {
				log.Printf("morning smart-retry: prompt: %v", err)
			} else {
				log.Printf("morning smart-retry: check-in prompt sent for %s", today)
			}
			time.Sleep(tick)
			continue

		case notify.MorningActionExpireAndForce:
			if _, err := db.ExpireCheckin(today, storage.CheckinSourceTelegram, now); err != nil {
				log.Printf("morning smart-retry: expire checkin: %v", err)
			}
			fallthrough

		case notify.MorningActionForce, notify.MorningActionSendReport:
			past := action == notify.MorningActionForce || action == notify.MorningActionExpireAndForce
			// TOCTOU critical section: re-check HasSent inside the
			// per-tenant mutex so the ingest goroutine can't slip a
			// second send between the loop-top check and the actual
			// Telegram POST. Manual lock/unlock (not defer) because
			// the surrounding for-loop accumulates defers per iter.
			// sendMu nil → single-sender legacy mode, skip locking.
			if sendMu != nil {
				sendMu.Lock()
			}
			alreadySent := db.HasSentMorningReport(today)
			var sent bool
			var reason string
			var err error
			if !alreadySent {
				sent, reason, err = notify.SendMorningSmartOpts(bot, db, ncfg, notify.MorningSendOpts{
					Force:          past,
					CheckinExpired: action == notify.MorningActionExpireAndForce,
				})
				if err == nil && sent {
					if perr := db.MarkMorningReportSent(today); perr != nil {
						log.Printf("morning smart-retry: mark sent: %v", perr)
					}
				}
			}
			if sendMu != nil {
				sendMu.Unlock()
			}

			if alreadySent {
				log.Println("morning smart-retry: already sent by other path between tick start and lock, exiting")
				return
			}
			if err != nil {
				log.Printf("morning smart-retry: send error: %v", err)
			}
			if sent {
				log.Printf("morning smart-retry: sent (reason=%s, forced=%v, action=%s)", reason, past, action)
				return
			}
			if past {
				// Force-send returned not-sent without an error — only happens when
				// the bot is somehow disabled mid-loop. Bail to avoid spinning.
				log.Printf("morning smart-retry: past cap but not sent (reason=%s), giving up", reason)
				return
			}
			// SendMorningSmart deferred (e.g. sleep not yet settled). Retry next tick.
			log.Printf("morning smart-retry: deferring (reason=%s), retry in %s", reason, tick)
			time.Sleep(tick)
		}
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
