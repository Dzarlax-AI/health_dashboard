package ui

import (
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"health-receiver/internal/applehealth"
	"health-receiver/internal/storage"
)

const maxImportBytes int64 = 2 * 1024 * 1024 * 1024

// importJob tracks the state of a running or completed import.
type importJob struct {
	mu               sync.Mutex
	running          bool
	done             bool
	parsed           int64
	inserted         int64
	skipped          int64
	workoutsParsed   int64
	workoutsUpserted int64
	workoutsFailed   int64
	bytesRead        int64
	totalBytes       int64
	startedAt        time.Time
	finishedAt       time.Time
	err              string
}

func (j *importJob) status() importStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := importStatus{
		Running:          j.running,
		Done:             j.done,
		Parsed:           atomic.LoadInt64(&j.parsed),
		Inserted:         atomic.LoadInt64(&j.inserted),
		Skipped:          atomic.LoadInt64(&j.skipped),
		WorkoutsParsed:   atomic.LoadInt64(&j.workoutsParsed),
		WorkoutsUpserted: atomic.LoadInt64(&j.workoutsUpserted),
		WorkoutsFailed:   atomic.LoadInt64(&j.workoutsFailed),
		BytesRead:        atomic.LoadInt64(&j.bytesRead),
		TotalBytes:       atomic.LoadInt64(&j.totalBytes),
		StartedAt:        j.startedAt.Format(time.RFC3339),
		Err:              j.err,
	}
	if j.done {
		s.ElapsedSec = int(j.finishedAt.Sub(j.startedAt).Seconds())
	} else if j.running {
		s.ElapsedSec = int(time.Since(j.startedAt).Seconds())
	}
	return s
}

type importStatus struct {
	Running          bool   `json:"running"`
	Done             bool   `json:"done"`
	Parsed           int64  `json:"parsed"`
	Inserted         int64  `json:"inserted"`
	Skipped          int64  `json:"skipped"`
	WorkoutsParsed   int64  `json:"workouts_parsed"`
	WorkoutsUpserted int64  `json:"workouts_upserted"`
	WorkoutsFailed   int64  `json:"workouts_failed"`
	BytesRead        int64  `json:"bytes_read"`
	TotalBytes       int64  `json:"total_bytes"`
	ElapsedSec       int    `json:"elapsed_sec"`
	StartedAt        string `json:"started_at,omitempty"`
	Err              string `json:"error,omitempty"`
}

// per-schema import jobs (one at a time per tenant)
var (
	currentJobs   = map[string]*importJob{}
	currentJobsMu sync.Mutex
)

func (h *Handler) adminImportStatus(w http.ResponseWriter, r *http.Request) {
	scope, scopeErr := h.resolveAdminTenantSchemaScope(r)
	if scopeErr != nil {
		writeStatusError(w, scopeErr)
		return
	}
	schema := scope.Schema
	currentJobsMu.Lock()
	job := currentJobs[schema]
	currentJobsMu.Unlock()

	if job == nil {
		jsonResponse(w, importStatus{})
		return
	}
	jsonResponse(w, job.status())
}

func (h *Handler) adminImportUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.ContentLength > maxImportBytes {
		http.Error(w, "import upload too large", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes)

	scope, scopeErr := h.resolveAdminTenantScope(r)
	if scopeErr != nil {
		writeStatusError(w, scopeErr)
		return
	}
	schema := scope.Schema
	db := scope.DB

	batchSize := 500
	if v := r.URL.Query().Get("batch"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			batchSize = n
		}
	}
	pauseMs := 150
	if v := r.URL.Query().Get("pause"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			pauseMs = n
		}
	}
	var fileSize int64
	if v := r.URL.Query().Get("size"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			fileSize = n
		}
	}

	// Only one import at a time. Reserve the slot before streaming so a
	// second request cannot start another large upload for the same tenant.
	job := &importJob{running: true, startedAt: time.Now(), totalBytes: fileSize}
	currentJobsMu.Lock()
	if currentJobs[schema] != nil && currentJobs[schema].running {
		currentJobsMu.Unlock()
		jsonResponse(w, map[string]string{"status": "error", "message": "import already running"})
		return
	}
	currentJobs[schema] = job
	currentJobsMu.Unlock()

	// Stream upload to a temp file so we can close the HTTP request quickly.
	tmp, err := os.CreateTemp("", "health-import-*.zip")
	if err != nil {
		currentJobsMu.Lock()
		delete(currentJobs, schema)
		currentJobsMu.Unlock()
		http.Error(w, "failed to create temp file", http.StatusInternalServerError)
		return
	}

	// Parse filename hint from query for format detection.
	filename := r.URL.Query().Get("filename")

	if _, err := io.Copy(tmp, r.Body); err != nil {
		currentJobsMu.Lock()
		delete(currentJobs, schema)
		currentJobsMu.Unlock()
		tmp.Close()
		os.Remove(tmp.Name())
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "import upload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "failed to receive file", http.StatusInternalServerError)
		return
	}
	tmp.Close()

	backfill := h.mgr.BackfillFor(schema)
	go runImport(job, db, tmp.Name(), filename, batchSize, time.Duration(pauseMs)*time.Millisecond, backfill)

	jsonResponse(w, map[string]string{"status": "ok", "message": "import started"})
}

func runImport(job *importJob, db *storage.DB, tmpPath, filename string, batchSize int, pause time.Duration, backfillFn func(bool)) {
	defer os.Remove(tmpPath)

	finish := func(errMsg string) {
		job.mu.Lock()
		job.running = false
		job.done = true
		job.finishedAt = time.Now()
		job.err = errMsg
		job.mu.Unlock()
	}

	// Track min/max dates of imported points so we can invalidate that range.
	var (
		dateMu         sync.Mutex
		metricMinDate  string
		metricMaxDate  string
		workoutMinDate string
		workoutMaxDate string
	)
	updateDates := func(pts []storage.MetricPoint) {
		dateMu.Lock()
		defer dateMu.Unlock()
		for _, p := range pts {
			d := p.Date
			if len(d) > 10 {
				d = d[:10]
			}
			if d == "" {
				continue
			}
			if metricMinDate == "" || d < metricMinDate {
				metricMinDate = d
			}
			if d > metricMaxDate {
				metricMaxDate = d
			}
		}
	}
	updateWorkoutDates := func(workouts []storage.Workout) {
		dateMu.Lock()
		defer dateMu.Unlock()
		for _, w := range workouts {
			d := w.StartTime.Format("2006-01-02")
			if d == "" {
				continue
			}
			if workoutMinDate == "" || d < workoutMinDate {
				workoutMinDate = d
			}
			if d > workoutMaxDate {
				workoutMaxDate = d
			}
		}
	}

	var batchCount int64
	emit := func(pts []storage.MetricPoint) {
		batchCount++
		updateDates(pts)
		n, err := db.BulkInsertPoints("apple-health-web-import", pts)
		if err != nil {
			log.Printf("import batch %d error: %v", batchCount, err)
		}
		atomic.AddInt64(&job.parsed, int64(len(pts)))
		atomic.AddInt64(&job.inserted, int64(n))
		atomic.AddInt64(&job.skipped, int64(len(pts)-n))
		if pause > 0 {
			time.Sleep(pause)
		}
	}
	var workoutBatchCount int64
	emitWorkouts := func(workouts []storage.Workout) {
		workoutBatchCount++
		updateWorkoutDates(workouts)
		var failed int64
		for _, w := range workouts {
			if err := db.UpsertWorkout(0, w); err != nil {
				failed++
				log.Printf("import workout batch %d upsert %s error: %v", workoutBatchCount, w.ExternalID, err)
			}
		}
		atomic.AddInt64(&job.workoutsParsed, int64(len(workouts)))
		atomic.AddInt64(&job.workoutsUpserted, int64(len(workouts))-failed)
		atomic.AddInt64(&job.workoutsFailed, failed)
		if pause > 0 {
			time.Sleep(pause)
		}
	}

	onProgress := func(read, total int64) {
		atomic.StoreInt64(&job.bytesRead, read)
		if total > 0 && job.totalBytes == 0 {
			atomic.StoreInt64(&job.totalBytes, total)
		}
	}

	var parseErr error
	isZip := true
	if len(filename) > 4 {
		ext := filename[len(filename)-4:]
		if ext == ".xml" {
			isZip = false
		}
	}

	opts := applehealth.EmitOptions{Points: emit, Workouts: emitWorkouts}
	if isZip {
		parseErr = applehealth.ParseZipWithOptions(tmpPath, opts, onProgress)
	} else {
		parseErr = applehealth.ParseXMLFileWithOptions(tmpPath, opts, onProgress)
	}

	errMsg := ""
	if parseErr != nil {
		errMsg = parseErr.Error()
		log.Printf("import parse error: %v", parseErr)
	}
	finish(errMsg)

	if metricMinDate != "" {
		// Remove Auto Export data for imported date range — Apple Health export
		// is the ground truth and should replace potentially inaccurate Auto Export data.
		log.Printf("import: removing Auto Export data for %s … %s", metricMinDate, metricMaxDate)
		db.RemoveAutoExportForRange(metricMinDate, metricMaxDate)

		// Invalidate aggregates and force full rebuild to ensure correctness.
		log.Printf("import: invalidating aggregates for %s … %s", metricMinDate, metricMaxDate)
		db.InvalidateDateRangeAggregates(metricMinDate, metricMaxDate)
		if backfillFn != nil {
			log.Println("import: triggering force backfill…")
			backfillFn(true)
		}
	}
	if workoutMinDate != "" {
		log.Printf("import: workouts upserted for %s … %s", workoutMinDate, workoutMaxDate)
	}
}
