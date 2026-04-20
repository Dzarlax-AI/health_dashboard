package handler

import (
	"encoding/json"
	"io"
	"log"
	"net/http"

	"health-receiver/internal/storage"
)

type Handler struct {
	db        *storage.DB
	apiKey    string
	onNewData func() // called in a goroutine after a successful insert; may be nil
}

func New(db *storage.DB, apiKey string, onNewData func()) *Handler {
	return &Handler{db: db, apiKey: apiKey, onNewData: onNewData}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/health", h.auth(h.health))
	// Filtered endpoints: accept all metrics but keep only SUM or AVG types.
	// This allows two Health Auto Export automations with identical "All Metrics"
	// but different Time Grouping (Hour vs Default) to hit different endpoints.
	mux.HandleFunc("/health/hourly", h.auth(h.healthFiltered("sum")))
	mux.HandleFunc("/health/vitals", h.auth(h.healthFiltered("avg")))
}

func (h *Handler) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.apiKey != "" && r.Header.Get("X-API-Key") != h.apiKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("read body: %v", err)
		http.Error(w, "failed to read body", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	points, parseErr := parseMetricPoints(body)
	if parseErr != nil {
		log.Printf("parse payload: %v (will still save raw)", parseErr)
	}

	rec := storage.Record{
		AutomationName:        r.Header.Get("automation-name"),
		AutomationID:          r.Header.Get("automation-id"),
		AutomationAggregation: r.Header.Get("automation-aggregation"),
		AutomationPeriod:      r.Header.Get("automation-period"),
		SessionID:             r.Header.Get("session-id"),
		ContentType:           r.Header.Get("Content-Type"),
		Payload:               string(body),
	}

	id, err := h.db.Insert(rec, points)
	if err != nil {
		log.Printf("insert: %v", err)
		http.Error(w, "failed to save record", http.StatusInternalServerError)
		return
	}

	log.Printf("saved record id=%d points=%d", id, len(points))

	// Inline cache upsert: rebuild hourly + daily for affected dates
	// directly from metric_points. No stale window.
	dateSet := make(map[string]bool)
	for _, p := range points {
		if len(p.Date) >= 10 {
			dateSet[p.Date[:10]] = true
		}
	}
	if len(dateSet) > 0 {
		dates := make([]string, 0, len(dateSet))
		for d := range dateSet {
			dates = append(dates, d)
		}
		go h.db.UpsertRecentCache(dates)
	}

	if h.onNewData != nil {
		go h.onNewData()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "ok", "id": id, "points": len(points)})
}

// healthFiltered returns a handler that accepts the same payload as /health
// but keeps only metrics of the given kind ("sum" or "avg").
// "sum" keeps SUM metrics (steps, calories, sleep, distance, etc.)
// "avg" keeps everything else (heart rate, HRV, SpO2, temperature, etc.)
// Unknown/new metrics default to "avg".
func (h *Handler) healthFiltered(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok", "filter": kind})
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("read body: %v", err)
			http.Error(w, "failed to read body", http.StatusInternalServerError)
			return
		}
		defer r.Body.Close()

		allPoints, parseErr := parseMetricPoints(body)
		if parseErr != nil {
			log.Printf("parse payload: %v (will still save raw)", parseErr)
		}

		// Filter points by metric type.
		var points []storage.MetricPoint
		for _, p := range allPoints {
			isSUM := storage.SumMetrics[p.MetricName]
			if (kind == "sum" && isSUM) || (kind == "avg" && !isSUM) {
				points = append(points, p)
			}
		}

		rec := storage.Record{
			AutomationName:        r.Header.Get("automation-name"),
			AutomationID:          r.Header.Get("automation-id"),
			AutomationAggregation: r.Header.Get("automation-aggregation"),
			AutomationPeriod:      r.Header.Get("automation-period"),
			SessionID:             r.Header.Get("session-id"),
			ContentType:           r.Header.Get("Content-Type"),
			Payload:               string(body),
		}

		id, err := h.db.Insert(rec, points)
		if err != nil {
			log.Printf("insert: %v", err)
			http.Error(w, "failed to save record", http.StatusInternalServerError)
			return
		}

		log.Printf("saved record id=%d points=%d (filtered %s, dropped %d)", id, len(points), kind, len(allPoints)-len(points))

		dateSet := make(map[string]bool)
		for _, p := range points {
			if len(p.Date) >= 10 {
				dateSet[p.Date[:10]] = true
			}
		}
		if len(dateSet) > 0 {
			dates := make([]string, 0, len(dateSet))
			for d := range dateSet {
				dates = append(dates, d)
			}
			go h.db.UpsertRecentCache(dates)
		}

		if h.onNewData != nil {
			go h.onNewData()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "id": id, "points": len(points), "filter": kind, "dropped": len(allPoints) - len(points)})
	}
}

type payload struct {
	Data struct {
		Metrics []struct {
			Name  string            `json:"name"`
			Units string            `json:"units"`
			Data  []json.RawMessage `json:"data"`
		} `json:"metrics"`
	} `json:"data"`
}

type basePoint struct {
	Date   string `json:"date"`
	Source string `json:"source"`
}

func parseMetricPoints(body []byte) ([]storage.MetricPoint, error) {
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	var points []storage.MetricPoint
	for _, m := range p.Data.Metrics {
		for _, raw := range m.Data {
			points = append(points, extractPoints(m.Name, m.Units, raw)...)
		}
	}
	return points, nil
}

// metricAliases maps Health Auto Export metric names to canonical names
// used by the dashboard. Add entries here when upstream changes naming.
var metricAliases = map[string]string{
	"weight_body_mass": "body_mass",
}

func extractPoints(metricName, units string, raw json.RawMessage) []storage.MetricPoint {
	if canonical, ok := metricAliases[metricName]; ok {
		metricName = canonical
	}

	var base basePoint
	json.Unmarshal(raw, &base)
	if base.Date == "" {
		return nil
	}

	pt := func(name string, qty float64) storage.MetricPoint {
		return storage.MetricPoint{MetricName: name, Units: units, Date: base.Date, Qty: qty, Source: base.Source}
	}

	switch metricName {
	case "heart_rate":
		var p struct{ Avg float64 }
		if json.Unmarshal(raw, &p) == nil {
			return []storage.MetricPoint{pt(metricName, p.Avg)}
		}
	case "sleep_analysis":
		var p struct {
			Deep       float64 `json:"deep"`
			REM        float64 `json:"rem"`
			Core       float64 `json:"core"`
			Awake      float64 `json:"awake"`
			TotalSleep float64 `json:"totalSleep"`
		}
		if json.Unmarshal(raw, &p) == nil {
			// Cap sleep values at physiological maximums to guard against
			// cumulative/inflated summaries from source apps (e.g. Health Auto Export + RingConn).
			const maxTotal = 12.0 // hours — extreme upper bound for a single night
			const maxPhase = 8.0  // hours — no single phase should exceed this
			p.Deep = capSleep(p.Deep, maxPhase)
			p.REM = capSleep(p.REM, maxPhase)
			p.Core = capSleep(p.Core, maxPhase)
			p.Awake = capSleep(p.Awake, maxPhase)
			p.TotalSleep = capSleep(p.TotalSleep, maxTotal)
			return []storage.MetricPoint{
				{MetricName: "sleep_deep",  Units: "hr", Date: base.Date, Qty: p.Deep,       Source: base.Source},
				{MetricName: "sleep_rem",   Units: "hr", Date: base.Date, Qty: p.REM,        Source: base.Source},
				{MetricName: "sleep_core",  Units: "hr", Date: base.Date, Qty: p.Core,       Source: base.Source},
				{MetricName: "sleep_awake", Units: "hr", Date: base.Date, Qty: p.Awake,      Source: base.Source},
				{MetricName: "sleep_total", Units: "hr", Date: base.Date, Qty: p.TotalSleep, Source: base.Source},
			}
		}
	}
	var p struct{ Qty float64 `json:"qty"` }
	json.Unmarshal(raw, &p)
	return []storage.MetricPoint{pt(metricName, p.Qty)}
}

// capSleep returns v capped at max; negative values become 0.
func capSleep(v, max float64) float64 {
	if v < 0 {
		return 0
	}
	if v > max {
		log.Printf("[WARN] sleep value %.2f exceeds cap %.0f h, capping", v, max)
		return max
	}
	return v
}
