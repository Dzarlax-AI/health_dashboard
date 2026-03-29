package ui

import (
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

// importJob tracks the state of a running or completed import.
type importJob struct {
	mu         sync.Mutex
	running    bool
	done       bool
	parsed     int64
	inserted   int64
	skipped    int64
	bytesRead  int64
	totalBytes int64
	startedAt  time.Time
	finishedAt time.Time
	err        string
}

func (j *importJob) status() importStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := importStatus{
		Running:    j.running,
		Done:       j.done,
		Parsed:     j.parsed,
		Inserted:   j.inserted,
		Skipped:    j.skipped,
		BytesRead:  j.bytesRead,
		TotalBytes: j.totalBytes,
		StartedAt:  j.startedAt.Format(time.RFC3339),
		Err:        j.err,
	}
	if j.done {
		s.ElapsedSec = int(j.finishedAt.Sub(j.startedAt).Seconds())
	} else if j.running {
		s.ElapsedSec = int(time.Since(j.startedAt).Seconds())
	}
	return s
}

type importStatus struct {
	Running    bool   `json:"running"`
	Done       bool   `json:"done"`
	Parsed     int64  `json:"parsed"`
	Inserted   int64  `json:"inserted"`
	Skipped    int64  `json:"skipped"`
	BytesRead  int64  `json:"bytes_read"`
	TotalBytes int64  `json:"total_bytes"`
	ElapsedSec int    `json:"elapsed_sec"`
	StartedAt  string `json:"started_at,omitempty"`
	Err        string `json:"error,omitempty"`
}

// global singleton job (only one import at a time)
var (
	currentJob   *importJob
	currentJobMu sync.Mutex
)

func (h *Handler) registerImportRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/admin/import/upload", h.guard(h.adminImportUpload))
	mux.HandleFunc("/api/admin/import/status", h.guard(h.adminImportStatus))
}

func (h *Handler) adminImportStatus(w http.ResponseWriter, r *http.Request) {
	currentJobMu.Lock()
	job := currentJob
	currentJobMu.Unlock()

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

	// Only one import at a time.
	currentJobMu.Lock()
	if currentJob != nil && currentJob.running {
		currentJobMu.Unlock()
		jsonResponse(w, map[string]string{"status": "error", "message": "import already running"})
		return
	}

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

	// Stream upload to a temp file so we can close the HTTP request quickly.
	tmp, err := os.CreateTemp("", "health-import-*.zip")
	if err != nil {
		currentJobMu.Unlock()
		http.Error(w, "failed to create temp file", http.StatusInternalServerError)
		return
	}

	// Parse filename hint from query for format detection.
	filename := r.URL.Query().Get("filename")

	if _, err := io.Copy(tmp, r.Body); err != nil {
		currentJobMu.Unlock()
		tmp.Close()
		os.Remove(tmp.Name())
		http.Error(w, "failed to receive file", http.StatusInternalServerError)
		return
	}
	tmp.Close()

	job := &importJob{running: true, startedAt: time.Now(), totalBytes: fileSize}
	currentJob = job
	currentJobMu.Unlock()

	go runImport(job, h.db, tmp.Name(), filename, batchSize, time.Duration(pauseMs)*time.Millisecond, h.backfill)

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
		dateMu  sync.Mutex
		minDate string
		maxDate string
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
			if minDate == "" || d < minDate {
				minDate = d
			}
			if d > maxDate {
				maxDate = d
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

	if isZip {
		parseErr = applehealth.ParseZip(tmpPath, emit, onProgress)
	} else {
		parseErr = applehealth.ParseXMLFile(tmpPath, emit, onProgress)
	}

	errMsg := ""
	if parseErr != nil {
		errMsg = parseErr.Error()
		log.Printf("import parse error: %v", parseErr)
	}
	finish(errMsg)

	if minDate != "" {
		// Remove Auto Export data for imported date range — Apple Health export
		// is the ground truth and should replace potentially inaccurate Auto Export data.
		log.Printf("import: removing Auto Export data for %s … %s", minDate, maxDate)
		db.RemoveAutoExportForRange(minDate, maxDate)

		// Invalidate aggregates and force full rebuild to ensure correctness.
		log.Printf("import: invalidating aggregates for %s … %s", minDate, maxDate)
		db.InvalidateDateRangeAggregates(minDate, maxDate)
		if backfillFn != nil {
			log.Println("import: triggering force backfill…")
			backfillFn(true)
		}
	}
}
