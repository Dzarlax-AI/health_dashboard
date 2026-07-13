package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"health-receiver/internal/ctxdb"
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

// Sync session protocol (iOS chunked re-sync):
//
//	X-Sync-Session:        <uuid>   — same on every chunk in the batch
//	X-Sync-Session-Total:  <N>      — total chunks the client will send
//
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
const maxIngestBodyBytes = 16 << 20

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
	"sleep_unspecified":      true,
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

	hrZones health.HRZones // optional; zero value disables HR-zone computation on /health/workouts

	jobs     chan func()
	jobsWG   sync.WaitGroup
	timersWG sync.WaitGroup
	closing  atomic.Bool

	sessMu   sync.Mutex
	sessions map[string]*syncSession
}

// New constructs a handler. zones may be the zero value (HRZones{}) to leave
// HR-zone columns NULL on workout ingest; configure via HEALTH_HR_ZONES_BPM.
func New(mgr *tenants.Manager, onNewData func(db *storage.DB, dates []string), zones health.HRZones) *Handler {
	h := &Handler{
		mgr:       mgr,
		onNewData: onNewData,
		hrZones:   zones,
		jobs:      make(chan func(), jobQueueSize),
		sessions:  make(map[string]*syncSession),
	}
	go h.runWorker()
	h.recoverPendingRecords()
	return h
}

func (h *Handler) recoverPendingRecords() {
	for _, db := range h.mgr.ActiveDBs(context.Background()) {
		records, err := db.PendingHealthRecords(1000)
		if err != nil {
			log.Printf("handler: list pending accepted records: %v", err)
			continue
		}
		for _, record := range records {
			record := record
			h.enqueue(func() {
				points, err := h.processAcceptedRecord(db, record.ID, []byte(record.Payload), record.ProcessingKind)
				if err != nil {
					log.Printf("record %d: recover accepted payload: %v", record.ID, err)
					return
				}
				h.finalizeChunk("", 0, db, datesOf(points), affectsReadiness(points))
			})
		}
	}
}

func (h *Handler) processAcceptedRecord(db *storage.DB, id int64, body []byte, kind string) ([]storage.MetricPoint, error) {
	allPoints, err := parseMetricPoints(body)
	if err != nil {
		_ = db.SetHealthRecordProcessing(id, "failed", err)
		return nil, err
	}
	points := filterPointsByKind(allPoints, kind)
	if err = db.InsertPoints(id, points); err != nil {
		_ = db.SetHealthRecordProcessing(id, "pending", err)
		return nil, err
	}
	err = db.SetHealthRecordProcessing(id, "complete", nil)
	return acceptedPointsAfterStatusUpdate(id, points, err)
}

// acceptedPointsAfterStatusUpdate preserves the successful ingest result when
// only the replay bookkeeping write fails. InsertPoints is idempotent, so the
// pending record can repair its status on restart; callers must still refresh
// caches for the points that are already durable.
func acceptedPointsAfterStatusUpdate(id int64, points []storage.MetricPoint, statusErr error) ([]storage.MetricPoint, error) {
	if statusErr != nil {
		log.Printf("record %d: mark accepted payload complete: %v", id, statusErr)
	}
	return points, nil
}

func filterPointsByKind(all []storage.MetricPoint, kind string) []storage.MetricPoint {
	if kind != "sum" && kind != "avg" {
		return all
	}
	points := make([]storage.MetricPoint, 0, len(all))
	for _, point := range all {
		isSUM := storage.SumMetrics[point.MetricName]
		if (kind == "sum" && isSUM) || (kind == "avg" && !isSUM) {
			points = append(points, point)
		}
	}
	return points
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
	h.jobsWG.Add(1)
	wrapped := func() {
		defer h.jobsWG.Done()
		job()
	}
	if h.closing.Load() {
		wrapped()
		return
	}
	select {
	case h.jobs <- wrapped:
	default:
		log.Printf("handler: job queue full (%d), running inline", jobQueueSize)
		wrapped()
	}
}

// Shutdown stops session timers, flushes their accepted work, and waits for
// the bounded processing queue. Raw payloads are already durable before a
// request receives 200, so a deadline only postpones derived-cache work.
func (h *Handler) Shutdown(ctx context.Context) error {
	h.closing.Store(true)
	h.sessMu.Lock()
	for id, session := range h.sessions {
		if session.timer.Stop() {
			h.timersWG.Done()
		}
		delete(h.sessions, id)
		dates := make([]string, 0, len(session.dates))
		for date := range session.dates {
			dates = append(dates, date)
		}
		h.enqueue(func() { h.flushDates(session.db, dates, session.scoreMetricChanged) })
	}
	h.sessMu.Unlock()
	h.timersWG.Wait()
	done := make(chan struct{})
	go func() { h.jobsWG.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
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
		h.timersWG.Add(1)
		s.timer = time.AfterFunc(sessionTimeout, func() {
			defer h.timersWG.Done()
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
		if s.timer.Stop() {
			h.timersWG.Done()
		}
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
	mux.HandleFunc("/health/workouts", h.auth(h.workouts))
}

func readIngestBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxIngestBodyBytes)
	return io.ReadAll(r.Body)
}

func writeIngestReadError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "failed to read body", http.StatusBadRequest)
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

	body, err := readIngestBody(w, r)
	if err != nil {
		log.Printf("read body: %v", err)
		writeIngestReadError(w, err)
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
		PendingProcessing:     true,
		ProcessingKind:        "all",
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
		points, err := h.processAcceptedRecord(db, id, body, "all")
		if err != nil {
			log.Printf("record %d: process accepted payload: %v", id, err)
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

		body, err := readIngestBody(w, r)
		if err != nil {
			log.Printf("read body: %v", err)
			writeIngestReadError(w, err)
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
			PendingProcessing:     true,
			ProcessingKind:        kind,
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
			points, err := h.processAcceptedRecord(db, id, body, kind)
			if err != nil {
				log.Printf("record %d: process accepted payload: %v", id, err)
				return
			}
			log.Printf("record %d: saved %d points (filtered %s)", id, len(points), kind)
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
			points = append(points, filterImpossible(extractPoints(m.Name, m.Units, raw))...)
		}
	}
	return points, nil
}

// filterImpossible drops points whose values fall outside the configured
// physiological range for the metric (see internal/health/quality.go). Logged
// at WARN so we can spot misbehaving sources, but rate-limited to one line per
// (metric,source) combination per call to avoid log floods on a stuck device.
func filterImpossible(in []storage.MetricPoint) []storage.MetricPoint {
	if len(in) == 0 {
		return in
	}
	out := in[:0]
	logged := map[string]bool{}
	for _, pt := range in {
		if !health.IsImpossible(pt.MetricName, float64(pt.Qty)) {
			out = append(out, pt)
			continue
		}
		key := pt.MetricName + "|" + pt.Source
		if !logged[key] {
			log.Printf("[QUALITY] drop %s=%v (source=%q date=%q): outside physiological range",
				pt.MetricName, pt.Qty, pt.Source, pt.Date)
			logged[key] = true
		}
	}
	return out
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
	var p struct {
		Qty float64 `json:"qty"`
	}
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
