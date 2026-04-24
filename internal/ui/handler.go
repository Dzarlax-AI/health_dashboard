package ui

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"health-receiver/internal/ai"
	"health-receiver/internal/ctxdb"
	"health-receiver/internal/health"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

type Handler struct {
	mgr              *tenants.Manager
	reg              *registry.Registry
	trustFwdAuth     bool
	onTenantCreated  func(schema string)
}

func New(mgr *tenants.Manager, reg *registry.Registry, trustFwdAuth bool) *Handler {
	return &Handler{mgr: mgr, reg: reg, trustFwdAuth: trustFwdAuth}
}

// OnTenantCreated registers a callback invoked after a new user schema is provisioned.
// Called from the setup wizard. Can be used to start per-tenant schedulers.
func (h *Handler) OnTenantCreated(fn func(schema string)) {
	h.onTenantCreated = fn
}

func (h *Handler) Register(mux *http.ServeMux) {
	// Auth
	mux.HandleFunc("/login", h.login)
	mux.HandleFunc("/setup", h.setup)

	// Page routes
	mux.HandleFunc("GET /{$}", h.guard(h.pageDashboard))
	mux.HandleFunc("GET /sleep", h.guard(h.pageSleep))
	mux.HandleFunc("GET /cardio", h.guard(h.pageCardio))
	mux.HandleFunc("GET /activity", h.guard(h.pageActivity))
	mux.HandleFunc("GET /recovery", h.guard(h.pageRecovery))
	mux.HandleFunc("GET /metrics", h.guard(h.pageMetrics))
	mux.HandleFunc("GET /metrics/{name}", h.guard(h.pageMetricDetail))
	mux.HandleFunc("GET /admin", h.guard(h.pageAdmin))

	// htmx fragments
	mux.HandleFunc("GET /fragments/metrics-list", h.guard(h.fragmentMetricsList))
	mux.HandleFunc("GET /fragments/admin-status", h.guard(h.fragmentAdminStatus))

	// Static assets
	mux.HandleFunc("GET /static/", serveStatic)

	// JSON API
	mux.HandleFunc("/health/checkpoint", h.guard(h.syncCheckpoint))
	mux.HandleFunc("/api/metrics", h.guard(h.listMetrics))
	mux.HandleFunc("/api/metrics/latest", h.guard(h.latestMetricValues))
	mux.HandleFunc("/api/metrics/range", h.guard(h.metricRange))
	mux.HandleFunc("/api/metrics/data", h.guard(h.metricData))
	mux.HandleFunc("/api/dashboard", h.guard(h.dashboard))
	mux.HandleFunc("/api/health-briefing", h.guard(h.healthBriefing))
	mux.HandleFunc("/api/readiness-history", h.guard(h.readinessHistory))
	mux.HandleFunc("/api/admin/status", h.guard(h.adminStatus))
	mux.HandleFunc("/api/admin/backfill", h.guard(h.adminBackfill))
	mux.HandleFunc("/api/admin/test-notify", h.guard(h.adminTestNotify))
	mux.HandleFunc("/api/admin/settings", h.guard(h.adminSettings))
	mux.HandleFunc("/api/admin/ai-models", h.guard(h.adminAIModels))
	mux.HandleFunc("/api/admin/gaps", h.guard(h.adminGaps))
	mux.HandleFunc("/api/admin/users", h.guard(h.adminUsers))
	h.registerImportRoutes(mux)
}

// tenantDB returns the tenant DB stored in the request context by guard().
func (h *Handler) tenantDB(r *http.Request) *storage.DB {
	return ctxdb.FromContext(r.Context())
}

// tenantSchema returns the tenant schema name from the request context.
func (h *Handler) tenantSchema(r *http.Request) string {
	return ctxdb.SchemaFromContext(r.Context())
}

// guard resolves the tenant from the session and injects the DB into the context.
func (h *Handler) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Legacy single-user mode: use env-var credentials.
		if h.mgr.LegacyMode() {
			db := h.mgr.LegacyDB()

			// Authentik forward auth
			if h.trustFwdAuth && r.Header.Get("X-authentik-username") != "" {
				next(w, r.WithContext(ctxdb.WithDB(r.Context(), db, "health")))
				return
			}
			// API key
			if key := r.Header.Get("X-API-Key"); key != "" {
				if subtle.ConstantTimeCompare([]byte(key), []byte(h.mgr.LegacyAPIKey())) == 1 {
					next(w, r.WithContext(ctxdb.WithDB(r.Context(), db, "health")))
					return
				}
			}
			// Cookie
			if cookie, err := r.Cookie("auth"); err == nil {
				if subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(h.mgr.LegacyPasswordHash())) == 1 {
					next(w, r.WithContext(ctxdb.WithDB(r.Context(), db, "health")))
					return
				}
			}
			http.Redirect(w, r, "/login?next="+r.URL.RequestURI(), http.StatusFound)
			return
		}

		// Multi-user mode.

		// New install: redirect to setup wizard before anything else.
		if h.reg != nil && h.reg.IsEmpty(r.Context()) {
			http.Redirect(w, r, "/setup", http.StatusFound)
			return
		}

		// Authentik forward auth: trust X-authentik-username / X-authentik-email headers.
		if h.trustFwdAuth {
			authentikUser := r.Header.Get("X-authentik-username")
			authentikEmail := r.Header.Get("X-authentik-email")
			if authentikUser != "" || authentikEmail != "" {
				// 1. Match by username.
				if authentikUser != "" {
					if db, schema, ok := h.mgr.DBForUsername(r.Context(), authentikUser); ok {
						next(w, r.WithContext(ctxdb.WithDB(r.Context(), db, schema)))
						return
					}
				}
				// 2. Match by email.
				if authentikEmail != "" {
					if db, schema, ok := h.mgr.DBForEmail(r.Context(), authentikEmail); ok {
						next(w, r.WithContext(ctxdb.WithDB(r.Context(), db, schema)))
						return
					}
				}
				// 3. Fallback: sole registered user (handles migration where user
				//    is named 'admin' but Authentik sends a different username/email).
				if db, schema, ok := h.mgr.DBForSoleUser(r.Context()); ok {
					next(w, r.WithContext(ctxdb.WithDB(r.Context(), db, schema)))
					return
				}
			}
		}

		// API key (for /health/checkpoint called from iOS app).
		if key := r.Header.Get("X-API-Key"); key != "" {
			if db, schema, ok := h.mgr.DBForAPIKey(r.Context(), key); ok {
				next(w, r.WithContext(ctxdb.WithDB(r.Context(), db, schema)))
				return
			}
		}

		// Session cookie: "username|sha256hash"
		if cookie, err := r.Cookie("auth"); err == nil {
			parts := strings.SplitN(cookie.Value, "|", 2)
			if len(parts) == 2 {
				username, hash := parts[0], parts[1]
				if user, err := h.reg.GetByUsername(r.Context(), username); err == nil {
					if subtle.ConstantTimeCompare([]byte(hash), []byte(user.PasswordHash)) == 1 {
						if db, schema, ok := h.mgr.DBForUsername(r.Context(), username); ok {
							next(w, r.WithContext(ctxdb.WithDB(r.Context(), db, schema)))
							return
						}
					}
				}
			}
		}

		http.Redirect(w, r, "/login?next="+r.URL.RequestURI(), http.StatusFound)
	}
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	// If no users exist yet, redirect to setup wizard.
	if !h.mgr.LegacyMode() && h.reg != nil && h.reg.IsEmpty(r.Context()) {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}

	next := r.URL.Query().Get("next")
	if next == "" {
		next = "/"
	}

	if r.Method == http.MethodPost {
		password := r.FormValue("password")
		sum := sha256.Sum256([]byte(password))
		hash := hex.EncodeToString(sum[:])

		// Legacy mode: compare against env password hash directly.
		if h.mgr.LegacyMode() {
			if subtle.ConstantTimeCompare([]byte(hash), []byte(h.mgr.LegacyPasswordHash())) == 1 {
				http.SetCookie(w, &http.Cookie{
					Name: "auth", Value: hash, Path: "/",
					HttpOnly: true, SameSite: http.SameSiteLaxMode,
					MaxAge: 60 * 60 * 24 * 30,
				})
				http.Redirect(w, r, next, http.StatusFound)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			renderPage(w, "login", struct{ Error string }{"Invalid password."})
			return
		}

		// Multi-user mode: require username.
		username := r.FormValue("username")
		user, err := h.reg.GetByUsername(r.Context(), username)
		if err != nil || subtle.ConstantTimeCompare([]byte(hash), []byte(user.PasswordHash)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			renderPage(w, "login", struct{ Error string }{"Invalid username or password."})
			return
		}

		cookieVal := username + "|" + hash
		http.SetCookie(w, &http.Cookie{
			Name: "auth", Value: cookieVal, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteLaxMode,
			MaxAge: 60 * 60 * 24 * 30,
		})
		http.Redirect(w, r, next, http.StatusFound)
		return
	}

	renderPage(w, "login", struct {
		Error      string
		MultiUser  bool
	}{"", !h.mgr.LegacyMode()})
}

func (h *Handler) setup(w http.ResponseWriter, r *http.Request) {
	// Setup is only available before any users exist and not in legacy mode.
	if h.mgr.LegacyMode() || (h.reg != nil && !h.reg.IsEmpty(r.Context())) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	if r.Method == http.MethodPost {
		username := strings.TrimSpace(r.FormValue("username"))
		password := r.FormValue("password")
		confirm := r.FormValue("confirm")

		if username == "" || password == "" {
			renderPage(w, "setup", struct{ Error string }{"Username and password are required."})
			return
		}
		if password != confirm {
			renderPage(w, "setup", struct{ Error string }{"Passwords do not match."})
			return
		}

		user, err := h.reg.CreateUser(r.Context(), registry.CreateUserReq{
			Username: username,
			Password: password,
			Email:    strings.TrimSpace(r.FormValue("email")),
			IsAdmin:  true,
		})
		if err != nil {
			renderPage(w, "setup", struct{ Error string }{"Failed to create user: " + err.Error()})
			return
		}

		if err := h.mgr.CreateUserSchema(r.Context(), user.SchemaName); err != nil {
			renderPage(w, "setup", struct{ Error string }{"Failed to create schema: " + err.Error()})
			return
		}
		if h.onTenantCreated != nil {
			h.onTenantCreated(user.SchemaName)
		}

		// Auto-login the new user.
		cookieVal := username + "|" + registry.HashPassword(password)
		http.SetCookie(w, &http.Cookie{
			Name: "auth", Value: cookieVal, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteLaxMode,
			MaxAge: 60 * 60 * 24 * 30,
		})
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	renderPage(w, "setup", struct{ Error string }{""})
}

// ---- Page handlers ----

func (h *Handler) pageDashboard(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	lang := langFromRequest(r)
	setLangCookie(w, r)

	type sleepData struct {
		Nights   int
		AvgTotal string
		AvgDeep  string
		AvgREM   string
	}

	data := struct {
		BasePage
		ReadinessScore  int
		ReadinessLabel  string
		ReadinessTip    string
		RecoveryPct     int
		Cards           []health.MetricCard
		Alerts          []health.Alert
		Sections        []health.BriefingSection
		Sleep           *sleepData
		Insights        []health.Insight
		Correlation     []health.CorrelationPoint
		CorrelationJSON template.JS
		AIInsight       string
	}{
		BasePage:        BasePage{Lang: lang, Title: T(lang, "app_title"), ActiveNav: "dashboard"},
		CorrelationJSON: "null",
	}

	today := time.Now().Format("2006-01-02")
	data.AIInsight = db.GetAIBriefing(today, lang)

	if br, err := db.GetHealthBriefing(lang); err == nil && br != nil {
		data.ReadinessScore = br.ReadinessToday
		data.ReadinessLabel = br.ReadinessTodayLabel
		data.ReadinessTip = br.ReadinessTip
		data.RecoveryPct = br.RecoveryPct
		data.Cards = br.MetricCards
		data.Alerts = br.Alerts
		data.Sections = br.Sections
		data.Insights = br.Insights
		data.Correlation = br.Correlation

		if br.Sleep != nil {
			s := br.Sleep
			data.Sleep = &sleepData{
				Nights:   s.Nights,
				AvgTotal: fmtMinutes(s.TotalAvg * 60),
				AvgDeep:  fmtMinutes(s.DeepAvg * 60),
				AvgREM:   fmtMinutes(s.REMAvg * 60),
			}
		}

		if len(br.Correlation) > 0 {
			if b, err := json.Marshal(br.Correlation); err == nil {
				data.CorrelationJSON = template.JS(b)
			}
		}
	}

	renderPage(w, "dashboard", data)
}

func fmtMinutes(m float64) string {
	h := int(math.Floor(m / 60))
	min := int(math.Round(m)) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, min)
	}
	return fmt.Sprintf("%dm", min)
}

func (h *Handler) pageSleep(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	lang := langFromRequest(r)
	setLangCookie(w, r)
	data := h.buildSectionPage("sleep", lang, db)
	renderPage(w, "section", data)
}

func (h *Handler) pageCardio(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	lang := langFromRequest(r)
	setLangCookie(w, r)
	data := h.buildSectionPage("cardio", lang, db)
	renderPage(w, "section", data)
}

func (h *Handler) pageActivity(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	lang := langFromRequest(r)
	setLangCookie(w, r)
	data := h.buildSectionPage("activity", lang, db)
	renderPage(w, "section", data)
}

func (h *Handler) pageRecovery(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	lang := langFromRequest(r)
	setLangCookie(w, r)
	data := h.buildSectionPage("recovery", lang, db)
	renderPage(w, "section", data)
}

func (h *Handler) pageMetrics(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	lang := langFromRequest(r)
	setLangCookie(w, r)
	query := r.URL.Query().Get("q")
	data := h.buildMetricsPageData(lang, query, db)
	renderPage(w, "metrics", data)
}

func (h *Handler) fragmentMetricsList(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	lang := langFromRequest(r)
	query := r.URL.Query().Get("q")
	data := h.buildMetricsPageData(lang, query, db)
	renderFragment(w, "metrics-list", data)
}

func (h *Handler) pageMetricDetail(w http.ResponseWriter, r *http.Request) {
	lang := langFromRequest(r)
	setLangCookie(w, r)
	metricName := r.PathValue("name")
	data := struct {
		BasePage
		MetricName  string
		MetricLabel string
	}{
		BasePage:    BasePage{Lang: lang, Title: MetricName(lang, metricName), ActiveNav: "metrics"},
		MetricName:  metricName,
		MetricLabel: MetricName(lang, metricName),
	}
	renderPage(w, "metric_detail", data)
}

func (h *Handler) pageAdmin(w http.ResponseWriter, r *http.Request) {
	lang := langFromRequest(r)
	setLangCookie(w, r)
	renderPage(w, "admin", BasePage{Lang: lang, Title: T(lang, "admin_title"), ActiveNav: "admin"})
}

func (h *Handler) fragmentAdminStatus(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	lang := langFromRequest(r)
	status, err := db.GetCacheStatus()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := struct {
		Lang        string
		RawCount    int
		MinuteCount int
		HourlyCount int
		DailyCount  int
		LastSync    string
	}{
		Lang:        lang,
		RawCount:    status.RawPoints.Rows,
		MinuteCount: status.MinuteCache.Rows,
		HourlyCount: status.HourlyCache.Rows,
		DailyCount:  status.DailyScores.Rows,
		LastSync:    status.LastSync,
	}
	renderFragment(w, "admin-status", data)
}

func setLangCookie(w http.ResponseWriter, r *http.Request) {
	if q := r.URL.Query().Get("lang"); q == "en" || q == "ru" || q == "sr" {
		http.SetCookie(w, &http.Cookie{
			Name:     "lang",
			Value:    q,
			Path:     "/",
			MaxAge:   60 * 60 * 24 * 365,
			SameSite: http.SameSiteLaxMode,
		})
	}
}

func (h *Handler) listMetrics(w http.ResponseWriter, r *http.Request) {
	metrics, err := h.tenantDB(r).ListMetrics()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, metrics)
}

func (h *Handler) metricRange(w http.ResponseWriter, r *http.Request) {
	metric := r.URL.Query().Get("metric")
	if metric == "" {
		http.Error(w, "metric required", http.StatusBadRequest)
		return
	}
	min, max, err := h.tenantDB(r).GetMetricDateRange(metric)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"min": min, "max": max})
}

func (h *Handler) syncCheckpoint(w http.ResponseWriter, r *http.Request) {
	ts, err := h.tenantDB(r).GetLatestMetricDate()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]int64{"latest_unix": ts})
}

func (h *Handler) latestMetricValues(w http.ResponseWriter, r *http.Request) {
	vals, err := h.tenantDB(r).GetLatestMetricValues()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, vals)
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	resp, err := h.tenantDB(r).GetDashboard()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, resp)
}

func (h *Handler) metricData(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	metric := q.Get("metric")
	if metric == "" {
		http.Error(w, "metric required", http.StatusBadRequest)
		return
	}

	from := q.Get("from")
	to := q.Get("to")
	if from == "" {
		from = time.Now().AddDate(0, 0, -7).Format("2006-01-02")
	}
	if to == "" {
		to = time.Now().Format("2006-01-02")
	}

	bucket := q.Get("bucket")
	if bucket == "" {
		fromT, _ := time.Parse("2006-01-02", from)
		toT, _ := time.Parse("2006-01-02", to[:10])
		days := int(toT.Sub(fromT).Hours()/24) + 1
		switch {
		case days <= 1:
			bucket = "minute"
		case days <= 14:
			bucket = "hour"
		default:
			bucket = "day"
		}
	}

	aggFunc := q.Get("agg")
	if aggFunc == "" {
		switch metric {
		case "step_count", "active_energy", "basal_energy_burned",
			"apple_exercise_time", "apple_stand_time", "flights_climbed",
			"walking_running_distance", "time_in_daylight", "apple_stand_hour":
			aggFunc = "SUM"
		default:
			aggFunc = "AVG"
		}
	}

	db := h.tenantDB(r)
	if q.Get("by_source") == "1" {
		sourcePoints, serr := db.GetMetricDataBySource(metric, from, to+" 23:59:59", bucket, aggFunc)
		if serr == nil {
			jsonResponse(w, map[string]any{
				"metric":           metric,
				"bucket":           bucket,
				"agg":              aggFunc,
				"by_source":        true,
				"points_by_source": sourcePoints,
			})
			return
		}
	}

	points, err := db.GetMetricData(metric, from, to+" 23:59:59", bucket, aggFunc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResponse(w, map[string]any{
		"metric": metric,
		"bucket": bucket,
		"agg":    aggFunc,
		"points": points,
	})
}

func (h *Handler) readinessHistory(w http.ResponseWriter, r *http.Request) {
	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		if n, err := strconv.Atoi(d); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}
	pts, err := h.tenantDB(r).GetReadinessHistory(days)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]any{"points": pts})
}

func (h *Handler) healthBriefing(w http.ResponseWriter, r *http.Request) {
	lang := r.URL.Query().Get("lang")
	if lang == "" {
		lang = "en"
	}
	resp, err := h.tenantDB(r).GetHealthBriefing(lang)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, resp)
}

func (h *Handler) adminStatus(w http.ResponseWriter, r *http.Request) {
	status, err := h.tenantDB(r).GetCacheStatus()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status.TelegramEnabled = h.mgr.TestNotifyFor(h.tenantSchema(r)) != nil
	jsonResponse(w, status)
}

func (h *Handler) adminBackfill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	backfill := h.mgr.BackfillFor(h.tenantSchema(r))
	if backfill == nil {
		http.Error(w, "backfill not configured", http.StatusServiceUnavailable)
		return
	}
	force := r.URL.Query().Get("force") == "1"
	backfill(force)
	msg := "incremental backfill scheduled"
	if force {
		msg = "full rebuild started"
	}
	jsonResponse(w, map[string]string{"status": "ok", "message": msg})
}

func (h *Handler) adminSettings(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	schema := h.tenantSchema(r)

	if r.Method == http.MethodPost {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		allowed := map[string]bool{
			"telegram_token": true, "telegram_chat_id": true, "report_lang": true,
			"timezone":               true,
			"report_morning_weekday": true, "report_morning_weekend": true,
			"report_evening_weekday": true, "report_evening_weekend": true,
			"gemini_api_key": true, "gemini_model": true, "gemini_max_tokens": true,
		}
		clean := make(map[string]string)
		for k, v := range body {
			if allowed[k] {
				clean[k] = v
			}
		}
		if err := db.SaveSettings(clean); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]string{"status": "ok"})
		return
	}

	notifyDefaults := h.mgr.NotifyDefaultsFor(schema)
	aiDefaults := h.mgr.AIDefaultsFor(schema)
	cfg := db.GetNotifyConfig(notifyDefaults)
	aiCfg := db.GetAIConfig(aiDefaults)
	jsonResponse(w, map[string]any{
		"telegram_token":          cfg.Token,
		"telegram_chat_id":        cfg.ChatID,
		"report_lang":             cfg.Lang,
		"timezone":                cfg.Timezone,
		"report_morning_weekday":  cfg.MorningWeekdayHour,
		"report_morning_weekend":  cfg.MorningWeekendHour,
		"report_evening_weekday":  cfg.EveningWeekdayHour,
		"report_evening_weekend":  cfg.EveningWeekendHour,
		"enabled":                 cfg.Enabled(),
		"gemini_api_key":          aiCfg.APIKey,
		"gemini_model":            aiCfg.Model,
		"gemini_max_tokens":       aiCfg.MaxOutputTokens,
		"gemini_enabled":          aiCfg.Enabled(),
	})
}

func (h *Handler) adminAIModels(w http.ResponseWriter, r *http.Request) {
	schema := h.tenantSchema(r)
	aiDefaults := h.mgr.AIDefaultsFor(schema)
	aiCfg := h.tenantDB(r).GetAIConfig(aiDefaults)
	if !aiCfg.Enabled() {
		http.Error(w, "Gemini API key not configured", http.StatusBadRequest)
		return
	}
	models, err := ai.ListModels(aiCfg.APIKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	jsonResponse(w, map[string]any{"models": models})
}

func (h *Handler) adminTestNotify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	testNotify := h.mgr.TestNotifyFor(h.tenantSchema(r))
	if testNotify == nil {
		jsonResponse(w, map[string]string{"status": "error", "message": "Telegram not configured"})
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind != "morning" && kind != "evening" {
		kind = "morning"
	}
	if err := testNotify(kind); err != nil {
		jsonResponse(w, map[string]string{"status": "error", "message": err.Error()})
		return
	}
	jsonResponse(w, map[string]string{"status": "ok", "message": "message sent"})
}

func (h *Handler) adminGaps(w http.ResponseWriter, r *http.Request) {
	gaps, err := h.tenantDB(r).GetDataGaps(2, 6)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if gaps == nil {
		gaps = []storage.DataGap{}
	}
	jsonResponse(w, map[string]any{"gaps": gaps})
}

func (h *Handler) adminUsers(w http.ResponseWriter, r *http.Request) {
	if h.reg == nil {
		http.Error(w, "registry not available", http.StatusServiceUnavailable)
		return
	}

	if r.Method == http.MethodPost {
		var req registry.CreateUserReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		user, err := h.reg.CreateUser(r.Context(), req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := h.mgr.CreateUserSchema(r.Context(), user.SchemaName); err != nil {
			jsonResponse(w, map[string]any{
				"status":  "partial",
				"user":    user,
				"warning": err.Error(),
			})
			return
		}
		if h.onTenantCreated != nil {
			h.onTenantCreated(user.SchemaName)
		}
		jsonResponse(w, map[string]any{"status": "ok", "user": user})
		return
	}

	users, err := h.reg.ListUsers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Mask password hashes.
	type safeUser struct {
		Username   string `json:"username"`
		SchemaName string `json:"schema_name"`
		APIKey     string `json:"api_key"`
		IsAdmin    bool   `json:"is_admin"`
	}
	out := make([]safeUser, len(users))
	for i, u := range users {
		out[i] = safeUser{u.Username, u.SchemaName, u.APIKey, u.IsAdmin}
	}
	jsonResponse(w, map[string]any{"users": out})
}

func jsonResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
