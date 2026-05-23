package ui

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"health-receiver/internal/storage"
)

// backfillTimeout returns the per-job context cap. Configurable via
// HEALTH_BACKFILL_TIMEOUT_MINUTES env var (default 60, clamp [1, 720]).
// Separate function so the value is fresh on each job — operators can
// raise it without restarting after tuning, the next kicked job sees
// the new env. (Setting env vars in a running container is unusual but
// the indirection costs nothing.)
func backfillTimeout() time.Duration {
	const def = 60
	const maxMin = 720 // 12h hard ceiling — past that, run cmd/energy_backfill
	v := os.Getenv("HEALTH_BACKFILL_TIMEOUT_MINUTES")
	if v == "" {
		return time.Duration(def) * time.Minute
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > maxMin {
		log.Printf("HEALTH_BACKFILL_TIMEOUT_MINUTES=%q ignored (must be int in [1, %d]); using %dm", v, maxMin, def)
		return time.Duration(def) * time.Minute
	}
	return time.Duration(n) * time.Minute
}

// energyBackfillJob is the per-tenant running state of a backfill
// triggered from the settings UI. Mirrors importJob's pattern in
// import_handler.go: mutex-protected state, one active job per
// schema, plus the final EnergyBackfillProgress from the storage
// layer for the UI to render.
//
// We could have re-used importJob's shape, but the import fields
// (parsed/inserted/skipped/bytesRead) don't map cleanly to the
// backfill's (ok/skipped/errors), and forcing one type would either
// half-populate fields or pollute the JSON contract. Two small
// parallel types beats one polymorphic one.
type energyBackfillJob struct {
	mu         sync.Mutex
	running    bool
	done       bool
	progress   storage.EnergyBackfillProgress
	startedAt  time.Time
	finishedAt time.Time
	err        string
}

func (j *energyBackfillJob) status() energyBackfillStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := energyBackfillStatus{
		Running:   j.running,
		Done:      j.done,
		Progress:  j.progress,
		StartedAt: j.startedAt.Format(time.RFC3339),
		Err:       j.err,
	}
	if j.done {
		s.ElapsedSec = int(j.finishedAt.Sub(j.startedAt).Seconds())
	} else if j.running {
		s.ElapsedSec = int(time.Since(j.startedAt).Seconds())
	}
	return s
}

type energyBackfillStatus struct {
	Running    bool                           `json:"running"`
	Done       bool                           `json:"done"`
	Progress   storage.EnergyBackfillProgress `json:"progress"`
	ElapsedSec int                            `json:"elapsed_sec"`
	StartedAt  string                         `json:"started_at,omitempty"`
	Err        string                         `json:"error,omitempty"`
}

// energyBackfillSummary is the GET response shape — counts the UI
// needs to render the "you have N days of history, M backfilled,
// K computable" status line before the user clicks the button.
type energyBackfillSummary struct {
	CompleteDailyScores int    `json:"complete_daily_scores"`
	BackfilledSnapshots int    `json:"backfilled_snapshots"`
	EarliestComplete    string `json:"earliest_complete,omitempty"`
	YesterdayInTZ       string `json:"yesterday_in_tz,omitempty"`
	TZ                  string `json:"tz,omitempty"`
}

var (
	currentBackfillJobs   = map[string]*energyBackfillJob{}
	currentBackfillJobsMu sync.Mutex
)

func (h *Handler) registerEnergyBackfillRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/settings/energy-backfill", h.guard(h.energyBackfillRun))
	mux.HandleFunc("/api/settings/energy-backfill/status", h.guard(h.energyBackfillStatus))
	mux.HandleFunc("/api/settings/energy-backfill/summary", h.guard(h.energyBackfillSummary))
}

// energyBackfillSummary reports the state of the world before the
// user clicks the button: how many days could be backfilled, how
// many already are, and what the resolved [from, to] range would be
// against the current tenant TZ. Used by the settings page to render
// the status line and gray out the button when there's nothing to do.
func (h *Handler) energyBackfillSummary(w http.ResponseWriter, r *http.Request) {
	scope, scopeErr := h.resolveAdminTenantScope(r)
	if scopeErr != nil {
		writeStatusError(w, scopeErr)
		return
	}
	db := scope.DB
	schema := scope.Schema
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Tenant TZ resolution mirrors the live orchestrator's logic —
	// settings.timezone if set, env REPORT_TZ default, "" otherwise.
	// We don't substitute UTC here on the read path: the UI needs to
	// know whether the user has explicitly set a TZ so it can show
	// the "set timezone first" hint and disable the button.
	tz := db.GetNotifyConfig(h.mgr.NotifyDefaultsFor(schema)).Timezone

	earliest, err := db.EarliestCompleteDailyScore(ctx)
	if err != nil {
		http.Error(w, "earliest: "+err.Error(), http.StatusInternalServerError)
		return
	}

	completeCount, backfilledCount, err := db.EnergyBackfillCoverage(ctx)
	if err != nil {
		http.Error(w, "coverage: "+err.Error(), http.StatusInternalServerError)
		return
	}

	yesterday := ""
	if tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			yesterday = time.Now().In(loc).AddDate(0, 0, -1).Format("2006-01-02")
		}
	}

	jsonResponse(w, energyBackfillSummary{
		CompleteDailyScores: completeCount,
		BackfilledSnapshots: backfilledCount,
		EarliestComplete:    earliest,
		YesterdayInTZ:       yesterday,
		TZ:                  tz,
	})
}

func (h *Handler) energyBackfillStatus(w http.ResponseWriter, r *http.Request) {
	scope, scopeErr := h.resolveAdminTenantSchemaScope(r)
	if scopeErr != nil {
		writeStatusError(w, scopeErr)
		return
	}
	schema := scope.Schema
	currentBackfillJobsMu.Lock()
	job := currentBackfillJobs[schema]
	// Opportunistic GC: drop job records that finished more than a
	// day ago. The map is bounded by tenant count so the leak is
	// small, but the UI hits this endpoint on every page load and a
	// long-running server accumulates stale snapshots — the status
	// payload then surfaces a "finished 30 days ago" panel that's
	// indistinguishable from a fresh run from the front end's
	// point of view. Deleting under the lock is safe because the
	// running goroutine has long returned by the time `done=true`
	// is set.
	if job != nil && job.done && time.Since(job.finishedAt) > 24*time.Hour {
		delete(currentBackfillJobs, schema)
		job = nil
	}
	currentBackfillJobsMu.Unlock()
	if job == nil {
		jsonResponse(w, energyBackfillStatus{})
		return
	}
	jsonResponse(w, job.status())
}

// energyBackfillRun kicks off a backfill in a goroutine and returns
// immediately with the job's initial status. The client polls
// /status until Done=true. One job per tenant: a second POST while
// the first is running returns 409 Conflict with the current status
// embedded (so the UI can hydrate progress without an extra round
// trip).
func (h *Handler) energyBackfillRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	scope, scopeErr := h.resolveAdminTenantScope(r)
	if scopeErr != nil {
		writeStatusError(w, scopeErr)
		return
	}
	db := scope.DB
	schema := scope.Schema
	tz := db.GetNotifyConfig(h.mgr.NotifyDefaultsFor(schema)).Timezone
	if tz == "" {
		// The UI gates the button on tz being set, so this is a
		// defence-in-depth check. Without a TZ the EOD bucket
		// timestamp is meaningless and rows would mis-bucket on
		// midnight boundaries.
		http.Error(w, "timezone not set — open Settings → Notifications and set Timezone first", http.StatusPreconditionFailed)
		return
	}

	// Decode optional body — front-end may pass explicit from/to
	// to backfill a specific window; empty body means "auto-resolve
	// from earliest complete to yesterday".
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	from, to, err := db.ResolveBackfillDateRange(r.Context(), tz, body.From, body.To)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if from == "" {
		http.Error(w, "no daily_scores rows with complete inputs yet — import Apple Health data or wait for live ingest to accumulate", http.StatusPreconditionFailed)
		return
	}

	currentBackfillJobsMu.Lock()
	if existing := currentBackfillJobs[schema]; existing != nil && existing.running {
		st := existing.status()
		currentBackfillJobsMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(st)
		return
	}
	job := &energyBackfillJob{
		running:   true,
		startedAt: time.Now(),
		progress: storage.EnergyBackfillProgress{
			From: from, To: to, TZ: tz,
		},
	}
	currentBackfillJobs[schema] = job
	currentBackfillJobsMu.Unlock()

	go runBackfillJob(db, schema, tz, from, to, job)

	jsonResponse(w, job.status())
}

func runBackfillJob(db *storage.DB, schema, tz, from, to string, job *energyBackfillJob) {
	// Use a context detached from the HTTP request so the goroutine
	// outlives the connection that started it.
	//
	// Default 1h cap is plenty for typical backfills (~10 years × 1
	// query/day ≈ 3600 queries, ~5ms each ≈ 18s wall). Operators with
	// multi-decade histories or congested DB pools can raise it via
	// HEALTH_BACKFILL_TIMEOUT_MINUTES (parsed as integer minutes,
	// clamped to [1, 720]). Out-of-range / unparseable values fall
	// back to the default — better to start the job on a sane
	// timeout than to fail loud over an env typo.
	timeout := backfillTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	progress, err := db.BackfillEnergyRange(ctx, tz, from, to, false, func(p storage.EnergyBackfillProgress) {
		job.mu.Lock()
		job.progress = p
		job.mu.Unlock()
	})

	job.mu.Lock()
	job.running = false
	job.done = true
	job.progress = progress
	job.finishedAt = time.Now()
	if err != nil {
		job.err = err.Error()
		log.Printf("[%s] energy-backfill error: %v", schema, err)
	}
	job.mu.Unlock()
	log.Printf("[%s] energy-backfill done: ok=%d skipped=%d errs=%d from=%s to=%s",
		schema, progress.OK, progress.Skipped, progress.Errors, from, to)
}
