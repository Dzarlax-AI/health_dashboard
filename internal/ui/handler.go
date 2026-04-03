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
	"time"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

type Handler struct {
	db             *storage.DB
	password       string // empty = no auth
	token          string // sha256(password), used as cookie value
	apiKey         string // also allows access via X-API-Key header
	trustFwdAuth   bool   // trust X-authentik-username header (set via TRUST_FORWARD_AUTH=true)
	backfill       func(force bool)
	testNotify     func(kind string) error
	notifyDefaults storage.NotifyConfig
	aiDefaults     storage.AIConfig
}

func New(db *storage.DB, password, apiKey string, trustFwdAuth bool, backfill func(force bool), testNotify func(kind string) error, notifyDefaults storage.NotifyConfig, aiDefaults storage.AIConfig) *Handler {
	var token string
	if password != "" {
		h := sha256.Sum256([]byte(password))
		token = hex.EncodeToString(h[:])
	}
	return &Handler{db: db, password: password, token: token, apiKey: apiKey, trustFwdAuth: trustFwdAuth, backfill: backfill, testNotify: testNotify, notifyDefaults: notifyDefaults, aiDefaults: aiDefaults}
}

func (h *Handler) Register(mux *http.ServeMux) {
	// Login
	mux.HandleFunc("/login", h.login)

	// Page routes (server-rendered)
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

	// JSON API (unchanged)
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
	h.registerImportRoutes(mux)
}

func (h *Handler) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.password == "" {
			next(w, r)
			return
		}
		// Authentik ForwardAuth: trust X-authentik-username when reverse proxy handles auth
		if h.trustFwdAuth && r.Header.Get("X-authentik-username") != "" {
			next(w, r)
			return
		}
		// Allow access via API key (X-API-Key header or Authorization: Bearer)
		if h.apiKey != "" {
			if key := r.Header.Get("X-API-Key"); key != "" {
				if subtle.ConstantTimeCompare([]byte(key), []byte(h.apiKey)) == 1 {
					next(w, r)
					return
				}
			}
		}
		cookie, err := r.Cookie("auth")
		if err != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(h.token)) != 1 {
			http.Redirect(w, r, "/login?next="+r.URL.RequestURI(), http.StatusFound)
			return
		}
		next(w, r)
	}
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	next := r.URL.Query().Get("next")
	if next == "" {
		next = "/"
	}

	if r.Method == http.MethodPost {
		pwd := r.FormValue("password")
		sum := sha256.Sum256([]byte(pwd))
		tok := hex.EncodeToString(sum[:])
		if subtle.ConstantTimeCompare([]byte(tok), []byte(h.token)) == 1 {
			http.SetCookie(w, &http.Cookie{
				Name:     "auth",
				Value:    h.token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   60 * 60 * 24 * 30, // 30 days
			})
			http.Redirect(w, r, next, http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		renderPage(w, "login", struct{ Error string }{"Invalid password."})
		return
	}

	renderPage(w, "login", struct{ Error string }{""})
}

// ---- Page handlers ----

func (h *Handler) pageDashboard(w http.ResponseWriter, r *http.Request) {
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

	// Fetch today's AI insight if available.
	today := time.Now().Format("2006-01-02")
	data.AIInsight = h.db.GetAIBriefing(today)

	// Fetch briefing (contains readiness, cards, insights, sleep, correlation)
	if br, err := h.db.GetHealthBriefing(lang); err == nil && br != nil {
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

// fmtMinutes formats a duration in minutes as "Xh Ym".
func fmtMinutes(m float64) string {
	h := int(math.Floor(m / 60))
	min := int(math.Round(m)) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, min)
	}
	return fmt.Sprintf("%dm", min)
}

func (h *Handler) pageSleep(w http.ResponseWriter, r *http.Request) {
	lang := langFromRequest(r)
	setLangCookie(w, r)
	data := h.buildSectionPage("sleep", lang)
	renderPage(w, "section", data)
}

func (h *Handler) pageCardio(w http.ResponseWriter, r *http.Request) {
	lang := langFromRequest(r)
	setLangCookie(w, r)
	data := h.buildSectionPage("cardio", lang)
	renderPage(w, "section", data)
}

func (h *Handler) pageActivity(w http.ResponseWriter, r *http.Request) {
	lang := langFromRequest(r)
	setLangCookie(w, r)
	data := h.buildSectionPage("activity", lang)
	renderPage(w, "section", data)
}

func (h *Handler) pageRecovery(w http.ResponseWriter, r *http.Request) {
	lang := langFromRequest(r)
	setLangCookie(w, r)
	data := h.buildSectionPage("recovery", lang)
	renderPage(w, "section", data)
}

func (h *Handler) pageMetrics(w http.ResponseWriter, r *http.Request) {
	lang := langFromRequest(r)
	setLangCookie(w, r)
	query := r.URL.Query().Get("q")
	data := h.buildMetricsPageData(lang, query)
	renderPage(w, "metrics", data)
}

func (h *Handler) fragmentMetricsList(w http.ResponseWriter, r *http.Request) {
	lang := langFromRequest(r)
	query := r.URL.Query().Get("q")
	data := h.buildMetricsPageData(lang, query)
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
	lang := langFromRequest(r)
	status, err := h.db.GetCacheStatus()
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

// setLangCookie persists the language preference when ?lang= is used.
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
	metrics, err := h.db.ListMetrics()
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
	min, max, err := h.db.GetMetricDateRange(metric)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"min": min, "max": max})
}

func (h *Handler) latestMetricValues(w http.ResponseWriter, r *http.Request) {
	vals, err := h.db.GetLatestMetricValues()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, vals)
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	resp, err := h.db.GetDashboard()
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

	if q.Get("by_source") == "1" {
		sourcePoints, serr := h.db.GetMetricDataBySource(metric, from, to+" 23:59:59", bucket, aggFunc)
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

	points, err := h.db.GetMetricData(metric, from, to+" 23:59:59", bucket, aggFunc)
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
	pts, err := h.db.GetReadinessHistory(days)
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
	resp, err := h.db.GetHealthBriefing(lang)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, resp)
}

func (h *Handler) adminStatus(w http.ResponseWriter, r *http.Request) {
	status, err := h.db.GetCacheStatus()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status.TelegramEnabled = h.testNotify != nil
	jsonResponse(w, status)
}

func (h *Handler) adminBackfill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.backfill == nil {
		http.Error(w, "backfill not configured", http.StatusServiceUnavailable)
		return
	}
	force := r.URL.Query().Get("force") == "1"
	h.backfill(force)
	msg := "incremental backfill scheduled"
	if force {
		msg = "full rebuild started"
	}
	jsonResponse(w, map[string]string{"status": "ok", "message": msg})
}

func (h *Handler) adminSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		allowed := map[string]bool{
			"telegram_token": true, "telegram_chat_id": true, "report_lang": true,
			"timezone": true,
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
		if err := h.db.SaveSettings(clean); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]string{"status": "ok"})
		return
	}

	// GET — return current effective config (DB values, falling back to env defaults).
	cfg := h.db.GetNotifyConfig(h.notifyDefaults)
	aiCfg := h.db.GetAIConfig(h.aiDefaults)
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
	aiCfg := h.db.GetAIConfig(h.aiDefaults)
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
	if h.testNotify == nil {
		jsonResponse(w, map[string]string{"status": "error", "message": "Telegram not configured (TELEGRAM_TOKEN / TELEGRAM_CHAT_ID missing)"})
		return
	}
	kind := r.URL.Query().Get("kind") // "morning" or "evening"
	if kind != "morning" && kind != "evening" {
		kind = "morning"
	}
	if err := h.testNotify(kind); err != nil {
		jsonResponse(w, map[string]string{"status": "error", "message": err.Error()})
		return
	}
	jsonResponse(w, map[string]string{"status": "ok", "message": "message sent"})
}

func (h *Handler) adminGaps(w http.ResponseWriter, r *http.Request) {
	gaps, err := h.db.GetDataGaps(2, 6)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if gaps == nil {
		gaps = []storage.DataGap{}
	}
	jsonResponse(w, map[string]any{"gaps": gaps})
}

func jsonResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

