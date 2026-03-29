package ui

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"health-receiver/internal/storage"
)

type Handler struct {
	db             *storage.DB
	password       string // empty = no auth
	token          string // sha256(password), used as cookie value
	apiKey         string // also allows access via X-API-Key header
	backfill       func(force bool)
	testNotify     func(kind string) error
	notifyDefaults storage.NotifyConfig
}

func New(db *storage.DB, password, apiKey string, backfill func(force bool), testNotify func(kind string) error, notifyDefaults storage.NotifyConfig) *Handler {
	var token string
	if password != "" {
		h := sha256.Sum256([]byte(password))
		token = hex.EncodeToString(h[:])
	}
	return &Handler{db: db, password: password, token: token, apiKey: apiKey, backfill: backfill, testNotify: testNotify, notifyDefaults: notifyDefaults}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/login", h.login)
	mux.HandleFunc("/", h.guard(h.page))
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
	mux.HandleFunc("/api/admin/gaps", h.guard(h.adminGaps))
	h.registerImportRoutes(mux)
}

func (h *Handler) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.password == "" {
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
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(loginPage("Invalid password.")))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(loginPage("")))
}

func (h *Handler) page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(indexHTML))
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
	})
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

func loginPage(errMsg string) string {
	errHTML := ""
	if errMsg != "" {
		errHTML = `<div style="color:#f87171;margin-bottom:12px;font-size:13px">` + errMsg + `</div>`
	}
	return `<!DOCTYPE html><html lang="en"><head>
<meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Health — Login</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#0d0f18;color:#e2e8f0;display:flex;align-items:center;justify-content:center;height:100vh}
.box{background:#13151f;border:1px solid #252836;border-radius:14px;padding:36px 32px;width:100%;max-width:340px}
h1{font-size:18px;font-weight:700;margin-bottom:24px;text-align:center}
label{font-size:12px;color:#64748b;display:block;margin-bottom:6px}
input{width:100%;background:#1a1d2e;border:1px solid #252836;color:#e2e8f0;padding:9px 12px;border-radius:8px;font-size:14px;outline:none}
input:focus{border-color:#4f8ef7}
button{margin-top:16px;width:100%;background:#4f8ef7;border:none;color:#fff;padding:10px;border-radius:8px;font-size:14px;font-weight:600;cursor:pointer}
button:hover{background:#3b7de0}
</style></head><body>
<div class="box">
  <h1>❤ Health</h1>` + errHTML + `
  <form method="POST">
    <label>Password</label>
    <input type="password" name="password" autofocus>
    <button type="submit">Sign in</button>
  </form>
</div>
</body></html>`
}
