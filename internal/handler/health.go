package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
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
	parsed, err := parseMetricPayload(body)
	if err != nil {
		_ = db.SetHealthRecordProcessing(id, "failed", err)
		return nil, err
	}
	points := filterPointsByKind(parsed.Points, kind)
	if err = db.InsertPoints(id, points); err != nil {
		_ = db.SetHealthRecordProcessing(id, "pending", err)
		return nil, err
	}
	for _, coverageErr := range parsed.NightSleepCoverageErr {
		log.Printf("record %d: ignore invalid night sleep coverage: %v", id, coverageErr)
	}
	for _, coverageErr := range parsed.SleepPeriodCoverageErr {
		log.Printf("record %d: ignore invalid sleep period coverage: %v", id, coverageErr)
	}
	// Canonical night sleep is a best-effort derived state. It is never
	// allowed to delay or fail an accepted raw upload, and it is only fed by
	// an explicit controlled-adapter coverage commitment.
	for _, commitment := range parsed.NightSleepCoverage {
		if !containsCommittedNightPoint(points, commitment) {
			continue
		}
		if err := db.SaveNightSleepCoverageCommitment(context.Background(), commitment); err != nil {
			log.Printf("record %d: save night sleep coverage: %v", id, err)
			continue
		}
		if err := db.ReconcileCompletedNightSleep(context.Background(), commitment.WakeDate, time.Now()); err != nil {
			log.Printf("record %d: reconcile completed night sleep: %v", id, err)
		}
	}
	// Balance periods are independent from the legacy nightly aggregate. The
	// storage operation replaces a period atomically, so a late correction
	// cannot leave an old nap mixed with a new night. Failures remain best-effort
	// derived-state failures and never turn an accepted upload into a rejection.
	for _, coverage := range parsed.SleepPeriodCoverage {
		changed, err := db.ApplySleepPeriodSnapshot(context.Background(), coverage, parsed.CompletedSleepEpisodes[coverage.WakeDate])
		if err != nil {
			log.Printf("record %d: apply sleep period snapshot: %v", id, err)
			continue
		}
		if !changed {
			continue
		}
		through, err := db.LatestSleepPeriodCoverageDate(context.Background())
		if err != nil || through == "" {
			if err != nil {
				log.Printf("record %d: find latest sleep period coverage: %v", id, err)
			}
			continue
		}
		if err := db.ReconcileSleepDurationBalancesAfter(context.Background(), coverage.WakeDate, through, time.Now()); err != nil {
			log.Printf("record %d: reconcile sleep duration balances: %v", id, err)
		}
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
		NightSleepCoverage     []nightSleepCoveragePayload    `json:"night_sleep_coverage"`
		SleepPeriodCoverage    []sleepPeriodCoveragePayload   `json:"sleep_period_coverage"`
		CompletedSleepEpisodes []completedSleepEpisodePayload `json:"completed_sleep_episodes"`
	} `json:"data"`
}

// nightSleepCoveragePayload is a controlled-adapter contract. `metric_date`
// must name the exact night_sleep_total point in the same payload; the server
// derives the canonical input hash and never trusts a vendor "closed night"
// flag that is not tied to a covered interval and sync generation.
type nightSleepCoveragePayload struct {
	WakeDate             string `json:"wake_date"`
	MetricDate           string `json:"metric_date"`
	Source               string `json:"source"`
	SourceEpoch          string `json:"source_epoch"`
	CaptureCompleteness  string `json:"capture_completeness"`
	CoverageGeneration   string `json:"sync_generation"`
	CoveredIntervalStart string `json:"covered_interval_start"`
	CoveredIntervalEnd   string `json:"covered_interval_end"`
}

// sleepPeriodCoveragePayload closes one complete tenant-local noon-to-noon
// accounting window. It is intentionally not tied to a particular wearable
// source or metric point: an empty complete period means no observed sleep,
// while an absent period remains unknown.
type sleepPeriodCoveragePayload struct {
	WakeDate             string `json:"wake_date"`
	SourceEpoch          string `json:"source_epoch"`
	CaptureCompleteness  string `json:"capture_completeness"`
	CoverageGeneration   string `json:"sync_generation"`
	CoveredIntervalStart string `json:"covered_interval_start"`
	CoveredIntervalEnd   string `json:"covered_interval_end"`
}

// completedSleepEpisodePayload carries an exact, source-selected asleep
// interval. The server derives its identity/hash and accepts it only beside
// the same payload's complete period coverage, never as a free-standing
// client assertion.
type completedSleepEpisodePayload struct {
	WakeDate           string `json:"wake_date"`
	Start              string `json:"start"`
	End                string `json:"end"`
	Source             string `json:"source"`
	SourceEpoch        string `json:"source_epoch"`
	CoverageGeneration string `json:"sync_generation"`
}

type parsedMetricPayload struct {
	Points                 []storage.MetricPoint
	NightSleepCoverage     []storage.NightSleepCoverageCommitment
	NightSleepCoverageErr  []error
	SleepPeriodCoverage    []storage.SleepPeriodCoverageCommitment
	SleepPeriodCoverageErr []error
	CompletedSleepEpisodes map[string][]storage.CompletedSleepEpisodeCommitment
}

type basePoint struct {
	Date   string `json:"date"`
	Source string `json:"source"`
}

func parseMetricPoints(body []byte) ([]storage.MetricPoint, error) {
	parsed, err := parseMetricPayload(body)
	if err != nil {
		return nil, err
	}
	return parsed.Points, nil
}

func parseMetricPayload(body []byte) (parsedMetricPayload, error) {
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return parsedMetricPayload{}, err
	}
	var points []storage.MetricPoint
	for _, m := range p.Data.Metrics {
		for _, raw := range m.Data {
			points = append(points, filterImpossible(extractPoints(m.Name, m.Units, raw))...)
		}
	}
	coverage, coverageErrs := parseNightSleepCoverage(p.Data.NightSleepCoverage)
	validCoverage := make([]storage.NightSleepCoverageCommitment, 0, len(coverage))
	for _, commitment := range coverage {
		if !containsCommittedNightPoint(points, commitment) {
			coverageErrs = append(coverageErrs, fmt.Errorf("night sleep coverage does not bind to an exact night_sleep_total point"))
			continue
		}
		// Keep only independently valid metadata. Raw metrics remain the
		// accepted record's durable payload even if this optional derived
		// attestation is malformed or does not bind exactly.
		validCoverage = append(validCoverage, commitment)
	}
	periodCoverage, periodErr := parseSleepPeriodCoverage(p.Data.SleepPeriodCoverage)
	periodCoverageErrs := make([]error, 0, 1)
	episodes := make(map[string][]storage.CompletedSleepEpisodeCommitment)
	if periodErr != nil {
		periodCoverageErrs = append(periodCoverageErrs, periodErr)
	} else {
		var episodesErr error
		episodes, episodesErr = parseCompletedSleepEpisodes(p.Data.CompletedSleepEpisodes, periodCoverage)
		if episodesErr != nil {
			// A rejected episode must not be turned into an empty complete
			// period: that could erase a prior valid snapshot on reconciliation.
			periodCoverage = nil
			episodes = make(map[string][]storage.CompletedSleepEpisodeCommitment)
			periodCoverageErrs = append(periodCoverageErrs, episodesErr)
		}
	}
	return parsedMetricPayload{
		Points: points, NightSleepCoverage: validCoverage, NightSleepCoverageErr: coverageErrs,
		SleepPeriodCoverage: periodCoverage, SleepPeriodCoverageErr: periodCoverageErrs,
		CompletedSleepEpisodes: episodes,
	}, nil
}

func parseNightSleepCoverage(raw []nightSleepCoveragePayload) ([]storage.NightSleepCoverageCommitment, []error) {
	commitments := make([]storage.NightSleepCoverageCommitment, 0, len(raw))
	warnings := make([]error, 0)
	for _, item := range raw {
		if _, err := time.Parse("2006-01-02", item.WakeDate); err != nil {
			warnings = append(warnings, fmt.Errorf("night sleep coverage wake date: %w", err))
			continue
		}
		if strings.TrimSpace(item.MetricDate) == "" || strings.TrimSpace(item.Source) == "" ||
			strings.TrimSpace(item.SourceEpoch) == "" || strings.TrimSpace(item.CoverageGeneration) == "" {
			warnings = append(warnings, fmt.Errorf("night sleep coverage requires metric date, source, source epoch, and sync generation"))
			continue
		}
		start, err := time.Parse(time.RFC3339, item.CoveredIntervalStart)
		if err != nil {
			warnings = append(warnings, fmt.Errorf("night sleep coverage start: %w", err))
			continue
		}
		end, err := time.Parse(time.RFC3339, item.CoveredIntervalEnd)
		if err != nil {
			warnings = append(warnings, fmt.Errorf("night sleep coverage end: %w", err))
			continue
		}
		if item.CaptureCompleteness != health.NightCaptureComplete && item.CaptureCompleteness != health.NightCapturePartial {
			warnings = append(warnings, fmt.Errorf("night sleep coverage has invalid completeness %q", item.CaptureCompleteness))
			continue
		}
		if !end.After(start) {
			warnings = append(warnings, fmt.Errorf("night sleep coverage interval must be positive"))
			continue
		}
		material := strings.Join([]string{item.WakeDate, item.MetricDate, item.Source, item.SourceEpoch, item.CaptureCompleteness, item.CoverageGeneration, start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano)}, "\x1f")
		commitments = append(commitments, storage.NightSleepCoverageCommitment{
			WakeDate: item.WakeDate, MetricDate: item.MetricDate, Source: item.Source, SourceEpoch: item.SourceEpoch,
			CaptureCompleteness: item.CaptureCompleteness, CoverageGeneration: item.CoverageGeneration,
			CoveredIntervalStart: start, CoveredIntervalEnd: end, ObservedAt: time.Now().UTC(),
			InputHash: fmt.Sprintf("%x", sha256.Sum256([]byte(material))),
		})
	}
	return commitments, warnings
}

func containsCommittedNightPoint(points []storage.MetricPoint, commitment storage.NightSleepCoverageCommitment) bool {
	for _, point := range points {
		if point.MetricName == "night_sleep_total" && point.Date == commitment.MetricDate && point.Source == commitment.Source {
			return true
		}
	}
	return false
}

func parseSleepPeriodCoverage(raw []sleepPeriodCoveragePayload) ([]storage.SleepPeriodCoverageCommitment, error) {
	commitments := make([]storage.SleepPeriodCoverageCommitment, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		if _, err := time.Parse("2006-01-02", item.WakeDate); err != nil {
			return nil, fmt.Errorf("sleep period coverage wake date: %w", err)
		}
		if _, duplicate := seen[item.WakeDate]; duplicate {
			return nil, fmt.Errorf("duplicate sleep period coverage for %s", item.WakeDate)
		}
		if strings.TrimSpace(item.SourceEpoch) == "" || strings.TrimSpace(item.CoverageGeneration) == "" {
			return nil, fmt.Errorf("sleep period coverage requires source epoch and sync generation")
		}
		if item.CaptureCompleteness != health.SleepBalanceCoverageComplete {
			return nil, fmt.Errorf("sleep period coverage must be complete")
		}
		start, err := time.Parse(time.RFC3339, item.CoveredIntervalStart)
		if err != nil {
			return nil, fmt.Errorf("sleep period coverage start: %w", err)
		}
		end, err := time.Parse(time.RFC3339, item.CoveredIntervalEnd)
		if err != nil {
			return nil, fmt.Errorf("sleep period coverage end: %w", err)
		}
		if !end.After(start) {
			return nil, fmt.Errorf("sleep period coverage interval must be positive")
		}
		material := strings.Join([]string{item.WakeDate, item.SourceEpoch, item.CaptureCompleteness, item.CoverageGeneration, start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano)}, "\x1f")
		commitments = append(commitments, storage.SleepPeriodCoverageCommitment{
			WakeDate: item.WakeDate, SourceEpoch: item.SourceEpoch, CaptureCompleteness: item.CaptureCompleteness,
			CoverageGeneration: item.CoverageGeneration, CoveredIntervalStart: start, CoveredIntervalEnd: end,
			ObservedAt: time.Now().UTC(), InputHash: fmt.Sprintf("%x", sha256.Sum256([]byte(material))),
		})
		seen[item.WakeDate] = struct{}{}
	}
	return commitments, nil
}

func parseCompletedSleepEpisodes(raw []completedSleepEpisodePayload, coverage []storage.SleepPeriodCoverageCommitment) (map[string][]storage.CompletedSleepEpisodeCommitment, error) {
	byWakeDate := make(map[string]storage.SleepPeriodCoverageCommitment, len(coverage))
	for _, item := range coverage {
		byWakeDate[item.WakeDate] = item
	}
	result := make(map[string][]storage.CompletedSleepEpisodeCommitment, len(coverage))
	for _, item := range raw {
		period, found := byWakeDate[item.WakeDate]
		if !found {
			return nil, fmt.Errorf("completed sleep episode has no matching complete period coverage")
		}
		if strings.TrimSpace(item.Source) == "" || strings.TrimSpace(item.SourceEpoch) == "" || strings.TrimSpace(item.CoverageGeneration) == "" {
			return nil, fmt.Errorf("completed sleep episode requires source, source epoch, and sync generation")
		}
		if item.SourceEpoch != period.SourceEpoch || item.CoverageGeneration != period.CoverageGeneration {
			return nil, fmt.Errorf("completed sleep episode does not match period coverage generation")
		}
		start, err := time.Parse(time.RFC3339, item.Start)
		if err != nil {
			return nil, fmt.Errorf("completed sleep episode start: %w", err)
		}
		end, err := time.Parse(time.RFC3339, item.End)
		if err != nil {
			return nil, fmt.Errorf("completed sleep episode end: %w", err)
		}
		material := strings.Join([]string{item.WakeDate, item.Source, item.SourceEpoch, item.CoverageGeneration, start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano)}, "\x1f")
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(material)))
		result[item.WakeDate] = append(result[item.WakeDate], storage.CompletedSleepEpisodeCommitment{
			EpisodeID: hash, WakeDate: item.WakeDate, Start: start, End: end, Source: item.Source,
			SourceEpoch: item.SourceEpoch, InputHash: hash, CoverageGeneration: item.CoverageGeneration,
			CaptureState: health.SleepBalanceCoverageComplete, DurationAssessment: health.NightDurationPlausible,
			ObservedAt: period.ObservedAt,
		})
	}
	return result, nil
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
