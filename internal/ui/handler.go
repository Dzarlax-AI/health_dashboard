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
	"health-receiver/internal/notify"
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

// adminGuard wraps guard() and additionally requires is_admin.
func (h *Handler) adminGuard(next http.HandlerFunc) http.HandlerFunc {
	return h.guard(func(w http.ResponseWriter, r *http.Request) {
		if !ctxdb.IsAdminFromContext(r.Context()) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

// isAdmin reports whether the current request user is an admin.
func (h *Handler) isAdmin(r *http.Request) bool {
	if h.mgr.LegacyMode() {
		return true
	}
	return ctxdb.IsAdminFromContext(r.Context())
}

// basePage builds a BasePage populated with lang, title, nav and isAdmin.
func (h *Handler) basePage(r *http.Request, title, activeNav string) BasePage {
	return BasePage{
		Lang:      langFromRequest(r),
		Title:     title,
		ActiveNav: activeNav,
		IsAdmin:   h.isAdmin(r),
		StaticVer: StaticVer(),
	}
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
	mux.HandleFunc("GET /settings", h.guard(h.pageSettings))
	mux.HandleFunc("GET /admin", h.adminGuard(h.pageAdmin))

	// htmx fragments
	mux.HandleFunc("GET /fragments/metrics-list", h.guard(h.fragmentMetricsList))
	mux.HandleFunc("GET /fragments/admin-status", h.adminGuard(h.fragmentAdminStatus))
	mux.HandleFunc("GET /fragments/admin-readiness-contract", h.adminGuard(h.fragmentAdminReadinessContract))

	// Static assets
	mux.HandleFunc("GET /static/", serveStatic)

	// JSON API — available to all authenticated users
	mux.HandleFunc("/health/checkpoint", h.guard(h.syncCheckpoint))
	mux.HandleFunc("/api/metrics", h.guard(h.listMetrics))
	mux.HandleFunc("/api/metrics/latest", h.guard(h.latestMetricValues))
	mux.HandleFunc("/api/metrics/range", h.guard(h.metricRange))
	mux.HandleFunc("/api/metrics/data", h.guard(h.metricData))
	mux.HandleFunc("/api/dashboard", h.guard(h.dashboard))
	mux.HandleFunc("/api/health-briefing", h.guard(h.healthBriefing))
	mux.HandleFunc("/api/ai-briefing", h.guard(h.aiBriefing))
	mux.HandleFunc("GET /api/section/{key}", h.guard(h.sectionAPI))
	mux.HandleFunc("GET /api/sections", h.guard(h.sectionsCatalogue))
	mux.HandleFunc("/api/readiness-history", h.guard(h.readinessHistory))
	mux.HandleFunc("/api/energy-history", h.guard(h.energyHistory))
	mux.HandleFunc("/api/settings", h.guard(h.userSettings))
	mux.HandleFunc("/api/settings/test-notify", h.guard(h.adminTestNotify))
	mux.HandleFunc("/api/import/upload", h.guard(h.adminImportUpload))
	mux.HandleFunc("/api/import/status", h.guard(h.adminImportStatus))

	// JSON API — admin only
	mux.HandleFunc("/api/admin/status", h.adminGuard(h.adminStatus))
	mux.HandleFunc("/api/admin/backfill", h.adminGuard(h.adminBackfill))
	mux.HandleFunc("/api/admin/readiness-redesign/backfill", h.adminGuard(h.adminReadinessRedesignBackfill))
	mux.HandleFunc("/api/admin/readiness-redesign/config", h.adminGuard(h.adminReadinessRedesignConfig))
	mux.HandleFunc("/api/admin/readiness-redesign/operational-contract", h.adminGuard(h.adminReadinessRedesignOperationalContract))
	mux.HandleFunc("/api/admin/gaps", h.adminGuard(h.adminGaps))
	mux.HandleFunc("/api/admin/quality-audit", h.adminGuard(h.adminQualityAudit))
	mux.HandleFunc("/api/admin/quality-fix", h.adminGuard(h.adminQualityFix))
	mux.HandleFunc("/api/admin/quality-digest", h.adminGuard(h.adminQualityDigest))
	mux.HandleFunc("/api/admin/settings", h.adminGuard(h.adminAISettings))
	mux.HandleFunc("/api/admin/ai-models", h.adminGuard(h.adminAIModels))
	mux.HandleFunc("/api/admin/energy-settings", h.adminGuard(h.adminEnergySettings))
	mux.HandleFunc("/api/admin/stress-validation", h.adminGuard(h.adminStressValidation))
	mux.HandleFunc("/api/admin/users", h.adminGuard(h.adminUsers))
	h.registerImportRoutes(mux)
	h.registerEnergyBackfillRoutes(mux)
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
				// Issue a local cookie so requests survive Authentik session expiry.
				if _, err := r.Cookie("auth"); err != nil {
					http.SetCookie(w, &http.Cookie{
						Name: "auth", Value: h.mgr.LegacyPasswordHash(), Path: "/",
						HttpOnly: true, SameSite: http.SameSiteLaxMode,
						MaxAge: 60 * 60 * 24 * 30,
					})
				}
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

		inject := func(db *storage.DB, schema string, isAdmin bool) {
			ctx := ctxdb.WithDB(r.Context(), db, schema)
			ctx = ctxdb.WithIsAdmin(ctx, isAdmin)
			next(w, r.WithContext(ctx))
		}

		// setAuthCookie issues a local session cookie so the user stays logged in
		// even if the Authentik session expires between requests.
		setAuthCookie := func(username string) {
			if _, err := r.Cookie("auth"); err == nil {
				return // cookie already present
			}
			if u, err := h.reg.GetByUsername(r.Context(), username); err == nil {
				http.SetCookie(w, &http.Cookie{
					Name:     "auth",
					Value:    u.Username + "|" + u.PasswordHash,
					Path:     "/",
					HttpOnly: true,
					SameSite: http.SameSiteLaxMode,
					MaxAge:   60 * 60 * 24 * 30,
				})
			}
		}

		// Authentik forward auth: trust X-authentik-username / X-authentik-email headers.
		if h.trustFwdAuth {
			authentikUser := r.Header.Get("X-authentik-username")
			authentikEmail := r.Header.Get("X-authentik-email")
			if authentikUser != "" || authentikEmail != "" {
				if authentikUser != "" {
					if db, schema, isAdmin, ok := h.mgr.DBForUsername(r.Context(), authentikUser); ok {
						setAuthCookie(authentikUser)
						inject(db, schema, isAdmin)
						return
					}
				}
				if authentikEmail != "" {
					if db, schema, isAdmin, ok := h.mgr.DBForEmail(r.Context(), authentikEmail); ok {
						if u, err := h.reg.GetByEmail(r.Context(), authentikEmail); err == nil {
							setAuthCookie(u.Username)
						}
						inject(db, schema, isAdmin)
						return
					}
				}
				// Fallback: sole registered user (migration case).
				if db, schema, isAdmin, ok := h.mgr.DBForSoleUser(r.Context()); ok {
					inject(db, schema, isAdmin)
					return
				}
			}
		}

		// API key (for /health/checkpoint called from iOS app).
		if key := r.Header.Get("X-API-Key"); key != "" {
			if db, schema, isAdmin, ok := h.mgr.DBForAPIKey(r.Context(), key); ok {
				inject(db, schema, isAdmin)
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
						if db, schema, isAdmin, ok := h.mgr.DBForUsername(r.Context(), username); ok {
							inject(db, schema, isAdmin)
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
		Headline        *health.HeadlineSignal
		EnergyBank      *health.EnergyBank
		Cards           []health.MetricCard
		Alerts          []health.Alert
		Sections        []health.BriefingSection
		Sleep           *sleepData
		Insights        []health.Insight
		Correlation     []health.CorrelationPoint
		CorrelationJSON template.JS
		AIInsight       string
	}{
		BasePage:        h.basePage(r, T(lang, "app_title"), "dashboard"),
		CorrelationJSON: "null",
	}

	today := time.Now().Format("2006-01-02")
	data.AIInsight = db.GetAIInsightCombined(today, lang)

	if br, err := db.GetHealthBriefing(lang); err == nil && br != nil {
		data.ReadinessScore = br.ReadinessToday
		data.ReadinessLabel = br.ReadinessTodayLabel
		data.ReadinessTip = br.ReadinessTip
		data.RecoveryPct = br.RecoveryPct
		data.Headline = br.Headline
		data.EnergyBank = br.EnergyBank
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
	data.IsAdmin = h.isAdmin(r)
	renderPage(w, "section", data)
}

func (h *Handler) pageCardio(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	lang := langFromRequest(r)
	setLangCookie(w, r)
	data := h.buildSectionPage("cardio", lang, db)
	data.IsAdmin = h.isAdmin(r)
	renderPage(w, "section", data)
}

func (h *Handler) pageActivity(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	lang := langFromRequest(r)
	setLangCookie(w, r)
	data := h.buildSectionPage("activity", lang, db)
	data.IsAdmin = h.isAdmin(r)
	renderPage(w, "section", data)
}

func (h *Handler) pageRecovery(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	lang := langFromRequest(r)
	setLangCookie(w, r)
	data := h.buildSectionPage("recovery", lang, db)
	data.IsAdmin = h.isAdmin(r)
	renderPage(w, "section", data)
}

func (h *Handler) pageMetrics(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	lang := langFromRequest(r)
	setLangCookie(w, r)
	query := r.URL.Query().Get("q")
	data := h.buildMetricsPageData(lang, query, db)
	data.IsAdmin = h.isAdmin(r)
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
		BasePage:    h.basePage(r, MetricName(lang, metricName), "metrics"),
		MetricName:  metricName,
		MetricLabel: MetricName(lang, metricName),
	}
	renderPage(w, "metric_detail", data)
}

func (h *Handler) pageSettings(w http.ResponseWriter, r *http.Request) {
	setLangCookie(w, r)
	renderPage(w, "settings", h.basePage(r, "Settings", "settings"))
}

func (h *Handler) pageAdmin(w http.ResponseWriter, r *http.Request) {
	setLangCookie(w, r)
	renderPage(w, "admin", struct {
		BasePage
		MultiUser bool
	}{
		BasePage:  h.basePage(r, T(langFromRequest(r), "admin_title"), "admin"),
		MultiUser: !h.mgr.LegacyMode(),
	})
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

// fragmentAdminReadinessContract renders the operational-contract
// preview table (§6.1) as an HTML fragment. Reads from the same
// `LoadOperationalContractRows` storage method as the JSON API
// counterpart so the two views can never drift.
//
// Each row is mapped to display strings here rather than in the
// template so the conditional rendering (pending / unknown / value /
// reason fallbacks) lives in one Go-testable place, with the
// template purely structural.
func (h *Handler) fragmentAdminReadinessContract(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	if db == nil {
		http.Error(w, "no tenant DB available", http.StatusServiceUnavailable)
		return
	}
	schema := h.tenantSchema(r)
	days, err := parseOperationalContractDays(r.URL.Query().Get("days"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	from, to := operationalContractWindow(h, db, schema, days)
	rows, err := db.LoadOperationalContractRows(from, to)
	if err != nil {
		http.Error(w, "load rows: "+err.Error(), http.StatusInternalServerError)
		return
	}

	type viewRow struct {
		Date               string
		SubScoreLabel      string
		ValueCell          string
		BaselineReasonCell string
		TargetEligCell     string
		SourceEpochCell    string
	}
	subScoreLabels := map[string]string{
		storage.SubScoreRecoveryStability: "recovery",
		storage.SubScorePassiveEfficiency: "passive",
		storage.SubScoreChronicLoad:       "chronic",
		storage.SubScoreAcuteRisk:         "acute",
	}
	view := make([]viewRow, 0, len(rows))
	for _, r := range rows {
		vr := viewRow{Date: r.Date, SubScoreLabel: subScoreLabels[r.SubScore]}
		// Value cell — chip's primary output. § 6.1 mapping:
		//   predicted_value != nil → render the number;
		//   no naive_baselines row at all (predicted_value nil AND
		//     reason nil) → `pending` (writer hasn't reached date yet);
		//   predicted_value nil, reason set → `unknown`.
		if r.PredictedValue != nil {
			vr.ValueCell = fmt.Sprintf("%.3f", *r.PredictedValue)
		} else if r.BaselineReason == nil {
			vr.ValueCell = "pending"
		} else {
			vr.ValueCell = "unknown"
		}
		if r.BaselineReason != nil {
			vr.BaselineReasonCell = *r.BaselineReason
		} else {
			vr.BaselineReasonCell = "—"
		}
		if r.TargetEligibilityReason != nil {
			vr.TargetEligCell = *r.TargetEligibilityReason
		} else {
			vr.TargetEligCell = "—"
		}
		if r.SourceEpoch != nil {
			vr.SourceEpochCell = *r.SourceEpoch
		} else {
			vr.SourceEpochCell = "—"
		}
		view = append(view, vr)
	}

	data := struct {
		Lang     string
		Rows     []viewRow
		EmptyMsg string
	}{
		Lang:     langFromRequest(r),
		Rows:     view,
		EmptyMsg: "no rows yet — run the readiness-redesign backfill first",
	}
	renderFragment(w, "admin-readiness-contract", data)
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
	lang := r.URL.Query().Get("lang")
	if lang != "ru" && lang != "sr" {
		lang = "en"
	}
	type metricItem struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Units       string `json:"units"`
		Count       int    `json:"count"`
		Min         string `json:"min"`
		Max         string `json:"max"`
	}
	out := make([]metricItem, 0, len(metrics))
	for _, m := range metrics {
		out = append(out, metricItem{
			Name:        m.Name,
			DisplayName: MetricName(lang, m.Name),
			Units:       m.Units,
			Count:       m.Count,
			Min:         m.Min,
			Max:         m.Max,
		})
	}
	jsonResponse(w, out)
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

// energyHistory serves the EnergyBank trend chart in two modes:
//
//	?granularity=day  (default) — legacy v1 behaviour, reads EOD
//	                              snapshots from daily_scores. Kept for
//	                              backward compatibility with existing
//	                              dashboard sparkline callers; will be
//	                              retired in PR8 once the UI flips to
//	                              v2.
//	?granularity=hour            — v2 behaviour, reads 5-min buckets
//	                              from energy_snapshots over the last
//	                              ?hours= hours.
//
// Response shapes differ between modes (different data sources, no
// way to lossless-merge pre-v2 days into v2 buckets), so the
// `granularity` field in the response identifies which schema the
// `points` array uses. Empty `points` is the correct response for a
// fresh tenant where v2 hasn't accumulated yet — the UI hides the
// sparkline instead of rendering a flat line over no data.
func (h *Handler) energyHistory(w http.ResponseWriter, r *http.Request) {
	gran := r.URL.Query().Get("granularity")
	if gran == "" {
		gran = "day"
	}

	switch gran {
	case "day":
		days := 14
		if d := r.URL.Query().Get("days"); d != "" {
			if n, err := strconv.Atoi(d); err == nil && n > 0 && n <= 365 {
				days = n
			}
		}
		pts, err := h.tenantDB(r).GetEnergyHistory(days)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if pts == nil {
			pts = []storage.EnergyHistoryPoint{}
		}
		jsonResponse(w, map[string]any{
			"granularity": "day",
			"points":      pts,
		})
	case "hour":
		hours := 72
		if hq := r.URL.Query().Get("hours"); hq != "" {
			if n, err := strconv.Atoi(hq); err == nil && n > 0 && n <= 720 {
				hours = n
			}
		}
		// Resolve TZ at request time so a tenant who edits
		// /settings.timezone sees the new offset immediately, without
		// a server restart. Empty TZ falls back to UTC silently — the
		// orchestrator's tenantTZOrUTC helper (cmd/server/main.go)
		// already log-warns about the misconfiguration on every
		// ingest for this same tenant, so doubling that warning here
		// would only spam the logs without telling an operator
		// anything new. Intentionally simpler than the writer path,
		// not an oversight.
		tz := h.tenantDB(r).GetNotifyConfig(storage.NotifyConfig{}).Timezone
		if tz == "" {
			tz = "UTC"
		}
		pts, err := h.tenantDB(r).GetEnergyHistoryV2(r.Context(), tz, hours)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		formulaVersion := 0
		if len(pts) > 0 {
			formulaVersion = pts[len(pts)-1].FormulaVersion
		}
		jsonResponse(w, map[string]any{
			"granularity":     "hour",
			"formula_version": formulaVersion,
			"points":          pts,
		})
	default:
		http.Error(w, "granularity must be 'day' or 'hour'", http.StatusBadRequest)
	}
}

// sectionAPIResponse is the JSON-friendly subset of SectionPageData. It
// drops template-only fields (HTML icons, BasePage chrome) so native
// clients consume only what they render.
type sectionAPIResponse struct {
	Key      string                 `json:"key"`
	Title    string                 `json:"title"`
	Summary  string                 `json:"summary"`
	Details  []sectionAPIDetail     `json:"details"`
	Charts   []sectionAPIChart      `json:"charts"`
	Explains []sectionAPIExplain    `json:"explains"`
}

type sectionAPIDetail struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Trend string `json:"trend"`
	Note  string `json:"note,omitempty"`
}

type sectionAPIChart struct {
	Metric    string `json:"metric,omitempty"`
	Agg       string `json:"agg,omitempty"`
	Label     string `json:"label"`
	Unit      string `json:"unit,omitempty"`
	Color     string `json:"color,omitempty"`
	ColorDark string `json:"color_dark,omitempty"`
	Type      string `json:"type,omitempty"`
	Stacked   bool   `json:"stacked,omitempty"`
	Virtual   bool   `json:"virtual,omitempty"`
}

type sectionAPIExplain struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// sectionCatalogueEntry is one row in the stable section catalogue
// returned by GET /api/sections. iOS / other native clients render
// it as a navigation row; the abstract `Icon` token (e.g. "heart")
// is mapped to a platform-specific symbol by the client.
type sectionCatalogueEntry struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Icon     string `json:"icon"`
}

// sectionCatalogue is the source of truth for which detail pages
// exist on the server. Adding a new section means appending an
// entry here AND providing the matching i18n keys
// (section_<key>_title / section_<key>_subtitle) plus the
// sectionMeta entry handled by sectionAPI. New entries appear on
// iOS automatically without an App Store release (issue #89).
//
// Sleep intentionally omitted: it has its own dedicated card on
// the Today screen rather than a Trends navigation row. If a
// future iOS surface needs the sleep entry too, add it here.
var sectionCatalogue = []struct {
	Key  string
	Icon string
}{
	{"cardio", "heart"},
	{"activity", "activity"},
	{"recovery", "leaf"},
}

func (h *Handler) sectionsCatalogue(w http.ResponseWriter, r *http.Request) {
	lang := supportedLang(r.URL.Query().Get("lang"))
	out := make([]sectionCatalogueEntry, 0, len(sectionCatalogue))
	for _, s := range sectionCatalogue {
		out = append(out, sectionCatalogueEntry{
			Key:      s.Key,
			Title:    T(lang, "section_"+s.Key+"_title"),
			Subtitle: T(lang, "section_"+s.Key+"_subtitle"),
			Icon:     s.Icon,
		})
	}
	jsonResponse(w, map[string]any{"sections": out})
}

func (h *Handler) sectionAPI(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if _, ok := sectionMeta[key]; !ok {
		http.Error(w, "unknown section", http.StatusNotFound)
		return
	}
	// Use the same resolver the HTML section pages use so /cardio and
	// /api/section/cardio agree on locale even when the lang comes from
	// the cookie rather than the query (CodeRabbit, PR #15).
	lang := langFromRequest(r)
	data := h.buildSectionPage(key, lang, h.tenantDB(r))

	out := sectionAPIResponse{
		Key:      data.SectionKey,
		Title:    data.SectionTitle,
		Summary:  data.Summary,
		Details:  []sectionAPIDetail{},  // never nil — clients prefer [] over null
		Charts:   []sectionAPIChart{},
		Explains: []sectionAPIExplain{},
	}
	for _, d := range data.Details {
		out.Details = append(out.Details, sectionAPIDetail{
			Label: d.Label, Value: d.Value, Trend: d.Trend, Note: d.Note,
		})
	}
	for _, c := range data.Charts {
		out.Charts = append(out.Charts, sectionAPIChart{
			Metric: c.Metric, Agg: c.Agg, Label: c.Label, Unit: c.Unit,
			Color: c.Color, ColorDark: c.ColorDark,
			Type: c.Type, Stacked: c.Stacked, Virtual: c.Virtual,
		})
	}
	for _, e := range data.Explains {
		out.Explains = append(out.Explains, sectionAPIExplain{
			Title: e.Title, Body: e.Body,
		})
	}
	jsonResponse(w, out)
}

// supportedLang clamps untrusted query input to the en/ru/sr whitelist.
// Any other value (including unknown locales like "fr") falls back to "en"
// so junk values can't pollute the AI cache or trigger Gemini regen on
// dead-data languages.
func supportedLang(q string) string {
	if q == "en" || q == "ru" || q == "sr" {
		return q
	}
	return "en"
}

func (h *Handler) healthBriefing(w http.ResponseWriter, r *http.Request) {
	lang := supportedLang(r.URL.Query().Get("lang"))
	db := h.tenantDB(r)
	schema := h.tenantSchema(r)
	resp, err := db.GetHealthBriefing(lang)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Briefing returns immediately. AIInsight is read from cache only — never
	// blocks on Gemini. If empty, kick off async regen so the next poll on
	// /api/ai-briefing returns content. Clients should fetch the AI narrative
	// from /api/ai-briefing separately and update their UI when it arrives,
	// instead of waiting on this endpoint.
	if resp != nil {
		today := time.Now().Format("2006-01-02")
		resp.AIInsight = db.GetAIInsightCombined(today, lang)
		if resp.AIInsight == "" {
			aiDefaults := h.mgr.AIDefaultsFor(r.Context(), schema)
			db.EnsureTodayAIInsightAsync(db.GetAIConfig(aiDefaults), lang)
		}
	}
	jsonResponse(w, resp)
}

// aiBriefing serves the per-block AI narrative for today. Polled by the web
// dashboard and the iOS client so a cold cache doesn't block the rest of the
// UI. Returns blocks + a generating flag so clients can distinguish "cache
// empty, regen running" from "cache empty, AI disabled".
func (h *Handler) aiBriefing(w http.ResponseWriter, r *http.Request) {
	lang := supportedLang(r.URL.Query().Get("lang"))
	db := h.tenantDB(r)
	schema := h.tenantSchema(r)
	today := time.Now().Format("2006-01-02")

	aiDefaults := h.mgr.AIDefaultsFor(r.Context(), schema)
	aiCfg := db.GetAIConfig(aiDefaults)

	blocks := db.GetAIBlocks(today, lang)
	combined := db.GetAIInsightCombined(today, lang)

	if combined == "" && aiCfg.Enabled() {
		db.EnsureTodayAIInsightAsync(aiCfg, lang)
	}

	// `sections[]` is the canonical shape going forward: ordered array
	// of `{key, header, body}` entries with the localized header
	// inline. iOS decodes the array directly and renders each entry
	// without per-block lookup. Crucially, a new AI block added
	// server-side (e.g. a `nutrition` chunk) appears in the array
	// automatically — iOS picks it up with zero code change because
	// the header ships in the response. Closed extensibility (issue
	// #83 item #5 clarification).
	//
	// The legacy `blocks` map (uppercase keys) and `insight` (combined
	// text) stay for backward compat: older iOS builds depend on them
	// and the web dashboard pre-renders `insight` template-side.
	ls := health.GetStrings(lang)
	type aiSection struct {
		Key    string `json:"key"`
		Header string `json:"header"`
		Body   string `json:"body"`
	}
	// Canonical block order matches the morning report (notify/report.go)
	// so the dashboard, Telegram, and iOS all render the same sequence.
	type blockSpec struct{ wireKey, dbKey, headerKey string }
	blockOrder := []blockSpec{
		{"sleep", "SLEEP", "ai_block_sleep_header"},
		{"yesterday", "YESTERDAY", "ai_block_yesterday_header"},
		{"recovery", "RECOVERY", "ai_block_recovery_header"},
		{"recommendation", "RECOMMENDATION", "ai_block_recommendation_header"},
	}
	sections := make([]aiSection, 0, len(blockOrder))
	for _, b := range blockOrder {
		body := blocks[b.dbKey]
		if body == "" {
			continue
		}
		sections = append(sections, aiSection{
			Key:    b.wireKey,
			Header: ls[b.headerKey],
			Body:   body,
		})
	}

	jsonResponse(w, map[string]any{
		"date":       today,
		"lang":       lang,
		"insight":    combined,
		"sections":   sections,
		"blocks":     blocks,
		"generating": db.AIRegenInFlight(lang),
		"disabled":   !aiCfg.Enabled(),
	})
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
	schema := h.tenantSchema(r)
	if target := strings.TrimSpace(r.URL.Query().Get("schema")); target != "" && target != schema {
		// Admin-only override (route already gated by adminGuard). Validate the
		// target belongs to a registered user before triggering backfill.
		if h.reg == nil {
			http.Error(w, "registry not available", http.StatusServiceUnavailable)
			return
		}
		users, err := h.reg.ListUsers(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		known := false
		for _, u := range users {
			if u.SchemaName == target {
				known = true
				break
			}
		}
		if !known {
			http.Error(w, "unknown schema", http.StatusBadRequest)
			return
		}
		schema = target
	}
	backfill := h.mgr.BackfillFor(schema)
	if backfill == nil {
		http.Error(w, "backfill not configured", http.StatusServiceUnavailable)
		return
	}
	force := r.URL.Query().Get("force") == "1"
	backfill(force)
	msg := "incremental backfill scheduled for " + schema
	if force {
		msg = "full rebuild started for " + schema
	}
	jsonResponse(w, map[string]string{"status": "ok", "message": msg, "schema": schema})
}

// adminReadinessRedesignBackfill runs the Phase 0 sub-score writers
// (Recovery Stability, Passive Efficiency) against [from, to].
//
// Query params:
//   from        YYYY-MM-DD (required)
//   to          YYYY-MM-DD (required, ≥ from)
//   sub_score   recovery_stability | passive_efficiency | all   (default: all)
//   force       "1" to lift the 90-day soft cap up to ~5 years
//   schema      tenant schema override (admin cross-tenant only)
//
// Execution is synchronous: returns when both writers finish. With a
// large range this can take tens of seconds; idempotent on every PK
// so retries are safe. Schema health is reported in the response so
// the operator can confirm Phase 0 storage is intact after the run.
func (h *Handler) adminReadinessRedesignBackfill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()
	from := strings.TrimSpace(q.Get("from"))
	to := strings.TrimSpace(q.Get("to"))
	if from == "" || to == "" {
		http.Error(w, "from and to are required (YYYY-MM-DD)", http.StatusBadRequest)
		return
	}
	fromT, err := time.Parse("2006-01-02", from)
	if err != nil {
		http.Error(w, "from must be YYYY-MM-DD", http.StatusBadRequest)
		return
	}
	toT, err := time.Parse("2006-01-02", to)
	if err != nil {
		http.Error(w, "to must be YYYY-MM-DD", http.StatusBadRequest)
		return
	}
	if toT.Before(fromT) {
		http.Error(w, "to must be on or after from", http.StatusBadRequest)
		return
	}

	// Range guard. Soft cap 90 days protects against accidental
	// full-history runs. force=1 lifts it to 5 years (1825 days) —
	// beyond that, the operator should chunk the run.
	const softCapDays = 90
	const hardCapDays = 1825
	force := q.Get("force") == "1"
	days := int(toT.Sub(fromT).Hours()/24) + 1
	cap := softCapDays
	if force {
		cap = hardCapDays
	}
	if days > cap {
		http.Error(w, fmt.Sprintf("range of %d days exceeds cap of %d days (pass force=1 for up to %d)",
			days, cap, hardCapDays), http.StatusBadRequest)
		return
	}

	requested := strings.TrimSpace(q.Get("sub_score"))
	if requested == "" {
		requested = "all"
	}
	wantRecovery := requested == "all" || requested == storage.SubScoreRecoveryStability
	wantPassive := requested == "all" || requested == storage.SubScorePassiveEfficiency
	wantAcute := requested == "all" || requested == storage.SubScoreAcuteRisk
	wantChronic := requested == "all" || requested == storage.SubScoreChronicLoad
	if !wantRecovery && !wantPassive && !wantAcute && !wantChronic {
		http.Error(w, "sub_score must be one of: recovery_stability, passive_efficiency, acute_risk, chronic_load, all",
			http.StatusBadRequest)
		return
	}

	// Tenant resolution — mirror adminBackfill's schema= override path.
	schema := h.tenantSchema(r)
	db := h.tenantDB(r)
	if target := strings.TrimSpace(q.Get("schema")); target != "" && target != schema {
		if h.reg == nil {
			http.Error(w, "registry not available", http.StatusServiceUnavailable)
			return
		}
		users, err := h.reg.ListUsers(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		known := false
		for _, u := range users {
			if u.SchemaName == target {
				known = true
				break
			}
		}
		if !known {
			http.Error(w, "unknown schema", http.StatusBadRequest)
			return
		}
		all := h.mgr.AllDBs()
		got, ok := all[target]
		if !ok || got == nil {
			http.Error(w, "tenant DB pool not initialised", http.StatusServiceUnavailable)
			return
		}
		db = got
		schema = target
	}
	if db == nil {
		http.Error(w, "no tenant DB available", http.StatusServiceUnavailable)
		return
	}

	type runResult struct {
		Written int    `json:"written"`
		Error   string `json:"error,omitempty"`
	}
	results := map[string]runResult{}

	if wantRecovery {
		n, err := db.BackfillRecoveryStabilitySnapshots(from, to)
		res := runResult{Written: n}
		if err != nil {
			res.Error = err.Error()
		}
		results[storage.SubScoreRecoveryStability] = res
	}
	if wantPassive {
		n, err := db.BackfillPassiveEfficiencySnapshots(from, to)
		res := runResult{Written: n}
		if err != nil {
			res.Error = err.Error()
		}
		results[storage.SubScorePassiveEfficiency] = res
	}
	if wantAcute {
		n, err := db.BackfillAcuteRiskSnapshots(from, to)
		res := runResult{Written: n}
		if err != nil {
			res.Error = err.Error()
		}
		results[storage.SubScoreAcuteRisk] = res
	}
	if wantChronic {
		n, err := db.BackfillChronicLoadSnapshots(from, to)
		res := runResult{Written: n}
		if err != nil {
			res.Error = err.Error()
		}
		results[storage.SubScoreChronicLoad] = res
	}

	schemaHealth := storage.RedesignStorageStatus{Healthy: true}
	if err := db.VerifyReadinessRedesignSchema(); err != nil {
		schemaHealth = storage.RedesignStorageStatus{Healthy: false, Error: err.Error()}
	}

	// Echo the effective chronic_load config the writer actually used,
	// so an operator backfilling a non-`health` tenant can confirm at a
	// glance whether the run used per-tenant overrides or fell back to
	// the calibrated defaults. Always populated, even when wantChronic
	// is false — useful when chaining backfills.
	_, chronicCfg := db.LoadChronicLoadConfig()

	jsonResponse(w, map[string]any{
		"schema":              schema,
		"from":                from,
		"to":                  to,
		"days":                days,
		"force":               force,
		"chronic_load_config": chronicCfg,
		"sub_scores":    results,
		"schema_health": schemaHealth,
	})
}

// adminReadinessRedesignConfig handles GET/POST
// /api/admin/readiness-redesign/config — inspect (GET) or override
// (POST) the Chronic Load calibration thresholds on a per-tenant
// basis. Admin only.
//
// GET — returns the effective config without running a backfill.
// Schema selectable via `?schema=<tenant_schema>`. Useful as a runbook
// step before backfilling a non-`health` tenant.
//
// POST — body is `{"chronic_load.min_acute_density": <int>,
// "chronic_load.min_breach_days": <int>}` (either or both keys).
// Writes to the tenant's `<schema>.settings` table directly, not
// the global registry — the two are separate stores and the chronic
// thresholds are intentionally per-tenant.
//
// The general /api/admin/settings endpoint deliberately does NOT
// accept these keys: it routes to the global registry and silently
// drops anything outside the gemini_* allow-list, which would look
// like success from the operator's side. This endpoint is the
// supported way to apply the override.
func (h *Handler) adminReadinessRedesignConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	schema := h.tenantSchema(r)
	db := h.tenantDB(r)
	if target := strings.TrimSpace(r.URL.Query().Get("schema")); target != "" && target != schema {
		if h.reg == nil {
			http.Error(w, "registry not available", http.StatusServiceUnavailable)
			return
		}
		users, err := h.reg.ListUsers(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		known := false
		for _, u := range users {
			if u.SchemaName == target {
				known = true
				break
			}
		}
		if !known {
			http.Error(w, "unknown schema", http.StatusBadRequest)
			return
		}
		all := h.mgr.AllDBs()
		got, ok := all[target]
		if !ok || got == nil {
			http.Error(w, "tenant DB pool not initialised", http.StatusServiceUnavailable)
			return
		}
		db = got
		schema = target
	}
	if db == nil {
		http.Error(w, "no tenant DB available", http.StatusServiceUnavailable)
		return
	}

	if r.Method == http.MethodPost {
		// Strongly-typed body: only positive ints, only the two keys.
		// Reject everything else explicitly so a typo in a key name
		// produces a 400 instead of a silent no-op.
		var body map[string]int
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: expected object of {key: int}", http.StatusBadRequest)
			return
		}
		if len(body) == 0 {
			http.Error(w, "body is empty; expected at least one of "+
				storage.SettingChronicLoadMinAcuteDensity+", "+
				storage.SettingChronicLoadMinBreachDays, http.StatusBadRequest)
			return
		}
		allowed := map[string]bool{
			storage.SettingChronicLoadMinAcuteDensity: true,
			storage.SettingChronicLoadMinBreachDays:   true,
		}
		toSave := make(map[string]string, len(body))
		for k, v := range body {
			if !allowed[k] {
				http.Error(w, "unknown key "+k+"; allowed: "+
					storage.SettingChronicLoadMinAcuteDensity+", "+
					storage.SettingChronicLoadMinBreachDays, http.StatusBadRequest)
				return
			}
			if v <= 0 {
				http.Error(w, k+" must be a positive integer; got "+strconv.Itoa(v),
					http.StatusBadRequest)
				return
			}
			toSave[k] = strconv.Itoa(v)
		}
		if err := db.SaveSettings(toSave); err != nil {
			http.Error(w, "save settings: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Echo the post-write effective config so the operator confirms
		// in one round-trip that the override took.
	}

	_, status := db.LoadChronicLoadConfig()
	jsonResponse(w, map[string]any{
		"schema":              schema,
		"chronic_load_config": status,
	})
}

// adminReadinessRedesignOperationalContract returns one row per
// (date, chip_config) for the last N days, joining
// `naive_baselines.predicted_value` / `reason` with
// `target_snapshots.eligibility_reason`. Implements the §6.1
// "Deliverable" — operator preview of what each chip would render,
// validated against the contract, before any UI ships.
//
// GET /api/admin/readiness-redesign/operational-contract?days=14&schema=<tenant>
//
// `days` defaults to 14, capped at 90. `schema` selects a cross-tenant
// override consistent with the other admin endpoints in this file.
func (h *Handler) adminReadinessRedesignOperationalContract(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	schema := h.tenantSchema(r)
	db := h.tenantDB(r)
	if target := strings.TrimSpace(r.URL.Query().Get("schema")); target != "" && target != schema {
		if h.reg == nil {
			http.Error(w, "registry not available", http.StatusServiceUnavailable)
			return
		}
		users, err := h.reg.ListUsers(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		known := false
		for _, u := range users {
			if u.SchemaName == target {
				known = true
				break
			}
		}
		if !known {
			http.Error(w, "unknown schema", http.StatusBadRequest)
			return
		}
		all := h.mgr.AllDBs()
		got, ok := all[target]
		if !ok || got == nil {
			http.Error(w, "tenant DB pool not initialised", http.StatusServiceUnavailable)
			return
		}
		db = got
		schema = target
	}
	if db == nil {
		http.Error(w, "no tenant DB available", http.StatusServiceUnavailable)
		return
	}

	days, err := parseOperationalContractDays(r.URL.Query().Get("days"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	from, to := operationalContractWindow(h, db, schema, days)

	rows, loadErr := db.LoadOperationalContractRows(from, to)
	if loadErr != nil {
		http.Error(w, "load rows: "+loadErr.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]any{
		"schema": schema,
		"from":   from,
		"to":     to,
		"days":   days,
		"rows":   rows,
	})
}

// parseOperationalContractDays validates the `days` query parameter
// for both the JSON and fragment surfaces. Empty → 14 (default).
// Returns (clampedDays, nil) on success, (0, err) on invalid input.
// Clamps at 90 — anything larger should go through the backfill
// endpoint or direct SQL, this surface is for at-a-glance validation.
func parseOperationalContractDays(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 14, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("days must be a positive integer")
	}
	if n > 90 {
		n = 90
	}
	return n, nil
}

// operationalContractWindow computes (from, to) date strings for the
// preview, anchored in the tenant's REPORT_TZ rather than UTC. Near
// midnight in non-UTC tenants this is the difference between showing
// the correct local day vs a stale UTC day.
//
// Resolution order: settings.timezone → env REPORT_TZ default → UTC.
// Same as the stress-validation and energy handlers.
func operationalContractWindow(h *Handler, db *storage.DB, schema string, days int) (from, to string) {
	var tz string
	// h.mgr is non-nil in production but may be nil in some tests
	// that build a bare Handler{}. Skip the per-tenant lookup in
	// that case rather than panicking.
	if h.mgr != nil {
		tz = db.GetNotifyConfig(h.mgr.NotifyDefaultsFor(schema)).Timezone
	}
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		// Unknown TZ name falls back to UTC rather than failing the
		// request — operator can see the data, the date-window edge
		// case is the only loss.
		loc = time.UTC
	}
	toT := time.Now().In(loc)
	fromT := toT.AddDate(0, 0, -(days - 1))
	return fromT.Format("2006-01-02"), toT.Format("2006-01-02")
}

// userSettings handles GET/POST /api/settings — Telegram config, available to all users.
func (h *Handler) userSettings(w http.ResponseWriter, r *http.Request) {
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
	cfg := db.GetNotifyConfig(notifyDefaults)
	out := map[string]any{
		"telegram_token":         cfg.Token,
		"telegram_chat_id":       cfg.ChatID,
		"report_lang":            cfg.Lang,
		"timezone":               cfg.Timezone,
		"report_morning_weekday": cfg.MorningWeekdayHour,
		"report_morning_weekend": cfg.MorningWeekendHour,
		"report_evening_weekday": cfg.EveningWeekdayHour,
		"report_evening_weekend": cfg.EveningWeekendHour,
		"enabled":                cfg.Enabled(),
	}
	// Identity: who the caller is logged in as. Lets native clients show
	// "Logged in as X · Tenant Y" without a separate /api/me round-trip,
	// and helps catch mis-configured API keys early. Skipped in legacy
	// single-user mode where there's no registry concept of users; that
	// keeps the documented "legacy omits identity fields" contract from
	// drifting if registry behaviour ever changes.
	if h.reg != nil && !h.mgr.LegacyMode() {
		if u, err := h.reg.GetBySchema(r.Context(), schema); err == nil && u != nil {
			out["username"] = u.Username
			out["tenant"] = u.SchemaName
			out["is_admin"] = u.IsAdmin
		}
	}
	jsonResponse(w, out)
}

// adminAISettings handles GET/POST /api/admin/settings — Gemini config, admin only.
//
// Gemini config is now installation-wide: writes go to
// `health_registry.global_settings`, reads layer that on top of env
// defaults (see Manager.AIDefaultsFor). Per-tenant overrides in
// `<schema>.settings` still win, but the admin UI no longer creates new
// per-tenant entries — one save reaches every tenant whose own config is
// blank, including non-admin users like Maria.
func (h *Handler) adminAISettings(w http.ResponseWriter, r *http.Request) {
	schema := h.tenantSchema(r)

	if r.Method == http.MethodPost {
		if h.reg == nil {
			http.Error(w, "registry unavailable", http.StatusInternalServerError)
			return
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		allowed := map[string]bool{
			"gemini_api_key": true, "gemini_model": true, "gemini_max_tokens": true,
		}
		clean := make(map[string]string)
		for k, v := range body {
			if allowed[k] {
				clean[k] = v
			}
		}
		if err := h.reg.SaveGlobalSettings(r.Context(), clean); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]string{"status": "ok"})
		return
	}

	// Show the installation-wide value (global + env), NOT the admin's own
	// tenant override. Otherwise saving a new global key and refreshing
	// would re-display whatever legacy `<schema>.settings.gemini_*` row the
	// admin has — making the form look like the save didn't take. The
	// settings page exists to manage the global default; tenant overrides
	// are deliberately invisible here.
	aiCfg := h.mgr.AIDefaultsFor(r.Context(), schema)
	jsonResponse(w, map[string]any{
		"gemini_api_key":    aiCfg.APIKey,
		"gemini_model":      aiCfg.Model,
		"gemini_max_tokens": aiCfg.MaxOutputTokens,
		"gemini_enabled":    aiCfg.Enabled(),
	})
}

// adminEnergySettings handles GET/POST /api/admin/energy-settings —
// v2.2 stress-drain coefficients (Beta, ZThreshold, StressDrainEnabled).
// Per-tenant (writes to <schema>.settings, not the global registry)
// because the §4.5 validation rubric clears these independently per
// user; tying them to a global setting would either over-restrict
// (one user's failing rubric blocks all) or leak (one user's tuned
// β applies to everyone). Admin-only to keep the non-production
// placeholder default (β=0.8) from being flipped on without an
// operator who understands the validation gate.
func (h *Handler) adminEnergySettings(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	if db == nil {
		http.Error(w, "tenant DB unavailable", http.StatusInternalServerError)
		return
	}

	if r.Method == http.MethodPost {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		// Validate each v2.2 stress-drain knob before persisting.
		// Whitelist alone wasn't enough — someone POSTing
		// `energy.beta=-1` or `=100` would silently break the bank
		// once StressDrainEnabled is flipped on. Bounds match the
		// `min`/`max` attributes on the admin.html inputs so the
		// UI and server agree on what's reasonable; tightening
		// upper bounds later (e.g. when cohort study clamps β to
		// a narrow range) only requires updating these two checks.
		// Flagged by CodeRabbit on PR #60.
		if v, ok := body["energy.beta"]; ok {
			beta, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil || beta < 0 || beta > 5 {
				http.Error(w, "energy.beta must be a number in [0, 5]", http.StatusBadRequest)
				return
			}
		}
		if v, ok := body["energy.z_threshold"]; ok {
			z, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil || z < 0 || z > 3 {
				http.Error(w, "energy.z_threshold must be a number in [0, 3]", http.StatusBadRequest)
				return
			}
		}
		if v, ok := body["energy.stress_drain_enabled"]; ok {
			if _, err := strconv.ParseBool(strings.TrimSpace(v)); err != nil {
				http.Error(w, "energy.stress_drain_enabled must be true/false", http.StatusBadRequest)
				return
			}
		}
		// Whitelist the three v2.2 keys — Alpha / AlphaFactor /
		// FormulaVersion stay out of the admin UI for now (they're
		// either tuned by the v2.5 calibrator or set deliberately by
		// `make energy-backfill`). A wider settings page can wire
		// them later without changing this whitelist's intent.
		allowed := map[string]bool{
			"energy.beta":                 true,
			"energy.z_threshold":          true,
			"energy.stress_drain_enabled": true,
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

	cfg := db.GetEnergyConfig()
	jsonResponse(w, map[string]any{
		"energy.beta":                 cfg.Beta,
		"energy.z_threshold":          cfg.ZThreshold,
		"energy.stress_drain_enabled": cfg.StressDrainEnabled,
		"effective_beta":              cfg.EffectiveBeta(),
	})
}

// adminStressValidation handles GET /api/admin/stress-validation —
// runs the STRESS_MEASUREMENT.md §4.5 four-channel rubric against
// the tenant's own history and returns the verdict + per-channel
// coefficients.
//
// Query params:
//   - window: rolling window in days (default 30, min 7, max 90)
//   - as_of:  end date YYYY-MM-DD (default today in REPORT_TZ)
//
// Admin-only because the response surface includes raw per-channel
// Pearson coefficients that we don't want to expose to regular
// users (a low r doesn't mean "you're not stressed", it means "the
// formula isn't capturing your physiology" — that's an operator
// concept, not a user-facing one).
//
// Read-only — does NOT flip settings.energy.stress_drain_enabled
// based on the verdict. Per §6 Q3, that flip is a manual operator
// decision after reviewing the rubric output. The endpoint exists
// to surface evidence, not to automate the gate.
func (h *Handler) adminStressValidation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	db := h.tenantDB(r)
	if db == nil {
		http.Error(w, "tenant DB unavailable", http.StatusInternalServerError)
		return
	}

	window := 30
	if v := r.URL.Query().Get("window"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 7 || n > 90 {
			http.Error(w, "window must be an integer in [7, 90]", http.StatusBadRequest)
			return
		}
		window = n
	}
	asOf := r.URL.Query().Get("as_of")
	if asOf != "" {
		if _, err := time.Parse("2006-01-02", asOf); err != nil {
			http.Error(w, "as_of must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}
	}

	// Tenant TZ resolution mirrors the energy backfill handler —
	// settings.timezone if set, env REPORT_TZ default, "UTC"
	// otherwise. Empty string into time.LoadLocation = UTC; safe
	// fallback that produces sane day boundaries even on a fresh
	// install without a configured tenant TZ.
	schema := h.tenantSchema(r)
	tz := db.GetNotifyConfig(h.mgr.NotifyDefaultsFor(schema)).Timezone
	if tz == "" {
		tz = "UTC"
	}
	report, err := db.ComputeStressValidationReport(r.Context(), tz, asOf, window)
	if err != nil {
		http.Error(w, "stress-validation: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, report)
}

func (h *Handler) adminAIModels(w http.ResponseWriter, r *http.Request) {
	schema := h.tenantSchema(r)
	// Use the installation-wide config (global + env), same source as
	// /api/admin/settings GET. Layering the admin's tenant override here
	// would make model discovery use a different API key than what the
	// admin just saved on the settings page.
	aiCfg := h.mgr.AIDefaultsFor(r.Context(), schema)
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

// adminQualityAudit returns a per-metric count of values currently in the
// metric_points table that fall outside the physiological ranges defined in
// internal/health/quality.go, plus this week's quality stats. Diagnostic only
// — does not modify any data.
func (h *Handler) adminQualityAudit(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	entries, err := db.AuditImpossibleValues()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []storage.QualityAuditEntry{}
	}
	weekly, _ := db.WeeklyQualityReport(7)
	jsonResponse(w, map[string]any{
		"entries": entries,
		"weekly":  weekly,
	})
}

// adminQualityFix runs MarkExistingImpossible + MarkSuspectPoints. POST only.
// Idempotent: re-running over already-flagged data is a no-op.
func (h *Handler) adminQualityFix(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	db := h.tenantDB(r)
	impossible, err := db.MarkExistingImpossible()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	suspectPerMetric, err := db.MarkSuspectPoints(7, 3)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	suspectTotal := 0
	for _, n := range suspectPerMetric {
		suspectTotal += n
	}
	jsonResponse(w, map[string]any{
		"impossible_flagged": impossible,
		"suspect_flagged":    suspectTotal,
		"per_metric":         suspectPerMetric,
	})
}

// adminQualityDigest sends the weekly digest immediately as a test. POST only.
// Useful to verify Telegram formatting without waiting for Monday.
func (h *Handler) adminQualityDigest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	db := h.tenantDB(r)
	cfg := db.GetNotifyConfig(storage.NotifyConfig{})
	if !cfg.Enabled() {
		http.Error(w, "Telegram not configured", http.StatusBadRequest)
		return
	}
	bot := notify.NewBot(cfg.Token, cfg.ChatID)
	ncfg := notify.Config{
		Token:    cfg.Token,
		ChatID:   cfg.ChatID,
		Lang:     cfg.Lang,
		Timezone: cfg.Timezone,
	}
	if err := notify.SendWeeklyDigestForce(bot, db, ncfg, 7); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]any{"ok": true})
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
		Email      string `json:"email,omitempty"`
		IsAdmin    bool   `json:"is_admin"`
	}
	out := make([]safeUser, len(users))
	for i, u := range users {
		out[i] = safeUser{u.Username, u.SchemaName, u.APIKey, u.Email, u.IsAdmin}
	}
	jsonResponse(w, map[string]any{"users": out})
}

func jsonResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
