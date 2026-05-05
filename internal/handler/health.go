package handler

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"health-receiver/internal/ctxdb"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

// Sync session protocol (iOS chunked re-sync):
//   X-Sync-Session:        <uuid>   — same on every chunk in the batch
//   X-Sync-Session-Total:  <N>      — total chunks the client will send
// Server holds back UpsertRecentCache + onNewData until N chunks land (or the
// session ages out), then runs them ONCE for the union of affected dates.
// Posts WITHOUT these headers behave as before — per-POST cache rebuild —
// keeping the regular incremental syncNow() snappy.
const (
	hdrSyncSession      = "X-Sync-Session"
	hdrSyncSessionTotal = "X-Sync-Session-Total"
	sessionTimeout      = 90 * time.Second
)

// jobQueueSize bounds the in-flight backlog of InsertPoints work. A single
// worker drains the queue serially, keeping DB pool usage predictable
// (one connection in use from this path at a time, regardless of POST burst).
const jobQueueSize = 256

type syncSession struct {
	db                 *storage.DB
	total              int
	received           int
	dates              map[string]bool
	scoreMetricChanged bool
	timer              *time.Timer
}

// scoreRelevantMetrics lists metrics that feed into the readiness formula.
// Other metrics (step_count, audio exposure, …) don't change readiness — when
// only non-score metrics arrive we skip the recompute pass.
var scoreRelevantMetrics = map[string]bool{
	"heart_rate_variability": true,
	"resting_heart_rate":     true,
	"sleep_total":            true,
	"sleep_deep":             true,
	"sleep_rem":              true,
	"sleep_core":             true,
	"sleep_awake":            true,
}

func affectsReadiness(points []storage.MetricPoint) bool {
	for _, p := range points {
		if scoreRelevantMetrics[p.MetricName] {
			return true
		}
	}
	return false
}

type Handler struct {
	mgr       *tenants.Manager
	onNewData func(db *storage.DB, dates []string) // called after a successful insert; may be nil

	jobs chan func()

	sessMu   sync.Mutex
	sessions map[string]*syncSession
}

func New(mgr *tenants.Manager, onNewData func(db *storage.DB, dates []string)) *Handler {
	h := &Handler{
		mgr:       mgr,
		onNewData: onNewData,
		jobs:      make(chan func(), jobQueueSize),
		sessions:  make(map[string]*syncSession),
	}
	go h.runWorker()
	return h
}

// runWorker drains the job queue serially. One in-flight InsertPoints / cache
// flush at a time → predictable DB pool usage even under chunked re-sync bursts.
func (h *Handler) runWorker() {
	for job := range h.jobs {
		job()
	}
}

// enqueue tries to schedule the job. If the queue is full (very large backlog)
// we run the job inline as a degraded-but-safe fallback rather than dropping it.
func (h *Handler) enqueue(job func()) {
	select {
	case h.jobs <- job:
	default:
		log.Printf("handler: job queue full (%d), running inline", jobQueueSize)
		job()
	}
}

func (h *Handler) flushDates(db *storage.DB, dates []string, recomputeReadiness bool) {
	if len(dates) == 0 {
		return
	}
	db.UpsertRecentCache(dates, recomputeReadiness)
	if h.onNewData != nil {
		h.onNewData(db, dates)
	}
}

// finalizeChunk is called from the per-record goroutine after InsertPoints.
// Without session headers it flushes immediately (legacy per-POST behaviour).
// With session headers it merges the chunk's dates into a shared session set
// and flushes once when the last chunk arrives (or when the safety timer fires).
func (h *Handler) finalizeChunk(sessionID string, total int, db *storage.DB, dates []string, scoreAffected bool) {
	if sessionID == "" {
		h.flushDates(db, dates, scoreAffected)
		return
	}

	h.sessMu.Lock()
	s, ok := h.sessions[sessionID]
	if !ok {
		s = &syncSession{db: db, total: total, dates: make(map[string]bool)}
		s.timer = time.AfterFunc(sessionTimeout, func() {
			h.sessMu.Lock()
			ts, ok := h.sessions[sessionID]
			if !ok {
				h.sessMu.Unlock()
				return
			}
			delete(h.sessions, sessionID)
			h.sessMu.Unlock()
			ds := make([]string, 0, len(ts.dates))
			for d := range ts.dates {
				ds = append(ds, d)
			}
			affected := ts.scoreMetricChanged
			log.Printf("sync session %s: timed out at %d/%d, flushing %d dates (score=%v)",
				sessionID, ts.received, ts.total, len(ds), affected)
			h.enqueue(func() { h.flushDates(ts.db, ds, affected) })
		})
		h.sessions[sessionID] = s
	}
	for _, d := range dates {
		s.dates[d] = true
	}
	if scoreAffected {
		s.scoreMetricChanged = true
	}
	s.received++
	complete := s.total > 0 && s.received >= s.total
	var snapshot []string
	var scoreSnap bool
	if complete {
		s.timer.Stop()
		delete(h.sessions, sessionID)
		snapshot = make([]string, 0, len(s.dates))
		for d := range s.dates {
			snapshot = append(snapshot, d)
		}
		scoreSnap = s.scoreMetricChanged
	}
	h.sessMu.Unlock()

	if complete {
		log.Printf("sync session %s: complete (%d/%d), flushing %d dates (score=%v)",
			sessionID, total, total, len(snapshot), scoreSnap)
		h.flushDates(db, snapshot, scoreSnap)
	}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/health", h.auth(h.health))
	mux.HandleFunc("/health/hourly", h.auth(h.healthFiltered("sum")))
	mux.HandleFunc("/health/vitals", h.auth(h.healthFiltered("avg")))
}

// auth resolves the tenant DB from X-API-Key and injects it into the context.
func (h *Handler) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-API-Key")
		db, schema, _, ok := h.mgr.DBForAPIKey(r.Context(), key)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r.WithContext(ctxdb.WithDB(r.Context(), db, schema)))
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

	db := ctxdb.FromContext(r.Context())
	rec := storage.Record{
		AutomationName:        r.Header.Get("automation-name"),
		AutomationID:          r.Header.Get("automation-id"),
		AutomationAggregation: r.Header.Get("automation-aggregation"),
		AutomationPeriod:      r.Header.Get("automation-period"),
		SessionID:             r.Header.Get("session-id"),
		ContentType:           r.Header.Get("Content-Type"),
		Payload:               string(body),
	}

	id, err := db.InsertRaw(rec)
	if err != nil {
		log.Printf("insert raw: %v", err)
		http.Error(w, "failed to save record", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "ok", "id": id})

	sessionID := r.Header.Get(hdrSyncSession)
	sessionTotal, _ := strconv.Atoi(r.Header.Get(hdrSyncSessionTotal))

	h.enqueue(func() {
		points, parseErr := parseMetricPoints(body)
		if parseErr != nil {
			log.Printf("record %d: parse payload: %v", id, parseErr)
		}
		if err := db.InsertPoints(id, points); err != nil {
			log.Printf("record %d: insert points: %v", id, err)
			return
		}
		log.Printf("record %d: saved %d points", id, len(points))
		h.finalizeChunk(sessionID, sessionTotal, db, datesOf(points), affectsReadiness(points))
	})
}

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

		db := ctxdb.FromContext(r.Context())
		rec := storage.Record{
			AutomationName:        r.Header.Get("automation-name"),
			AutomationID:          r.Header.Get("automation-id"),
			AutomationAggregation: r.Header.Get("automation-aggregation"),
			AutomationPeriod:      r.Header.Get("automation-period"),
			SessionID:             r.Header.Get("session-id"),
			ContentType:           r.Header.Get("Content-Type"),
			Payload:               string(body),
		}

		id, err := db.InsertRaw(rec)
		if err != nil {
			log.Printf("insert raw: %v", err)
			http.Error(w, "failed to save record", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "id": id, "filter": kind})

		sessionID := r.Header.Get(hdrSyncSession)
		sessionTotal, _ := strconv.Atoi(r.Header.Get(hdrSyncSessionTotal))

		h.enqueue(func() {
			allPoints, parseErr := parseMetricPoints(body)
			if parseErr != nil {
				log.Printf("record %d: parse payload: %v", id, parseErr)
			}
			var points []storage.MetricPoint
			for _, p := range allPoints {
				isSUM := storage.SumMetrics[p.MetricName]
				if (kind == "sum" && isSUM) || (kind == "avg" && !isSUM) {
					points = append(points, p)
				}
			}
			if err := db.InsertPoints(id, points); err != nil {
				log.Printf("record %d: insert points: %v", id, err)
				return
			}
			log.Printf("record %d: saved %d points (filtered %s, dropped %d)", id, len(points), kind, len(allPoints)-len(points))
			h.finalizeChunk(sessionID, sessionTotal, db, datesOf(points), affectsReadiness(points))
		})
	}
}

func datesOf(points []storage.MetricPoint) []string {
	set := make(map[string]bool, len(points))
	for _, p := range points {
		if len(p.Date) >= 10 {
			set[p.Date[:10]] = true
		}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	return out
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
			const maxTotal = 12.0
			const maxPhase = 8.0
			p.Deep = capSleep(p.Deep, maxPhase)
			p.REM = capSleep(p.REM, maxPhase)
			p.Core = capSleep(p.Core, maxPhase)
			p.Awake = capSleep(p.Awake, maxPhase)
			p.TotalSleep = capSleep(p.TotalSleep, maxTotal)
			return []storage.MetricPoint{
				{MetricName: "sleep_deep", Units: "hr", Date: base.Date, Qty: p.Deep, Source: base.Source},
				{MetricName: "sleep_rem", Units: "hr", Date: base.Date, Qty: p.REM, Source: base.Source},
				{MetricName: "sleep_core", Units: "hr", Date: base.Date, Qty: p.Core, Source: base.Source},
				{MetricName: "sleep_awake", Units: "hr", Date: base.Date, Qty: p.Awake, Source: base.Source},
				{MetricName: "sleep_total", Units: "hr", Date: base.Date, Qty: p.TotalSleep, Source: base.Source},
			}
		}
	}
	var p struct{ Qty float64 `json:"qty"` }
	json.Unmarshal(raw, &p)
	return []storage.MetricPoint{pt(metricName, p.Qty)}
}

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
