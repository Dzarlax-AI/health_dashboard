// cmd/downsample: aggregates old metric_points to reduce storage and speed up queries.
//
//	Pass 1: data older than PASS1_DAYS (14) days → hourly granularity
//	Pass 2: data older than PASS2_DAYS (90) days → daily granularity
//
// Usage: DB_PATH=./data/health.db go run ./cmd/downsample [--dry-run]
package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"

	_ "github.com/mattn/go-sqlite3"
)

var sumMetrics = map[string]bool{
	"step_count": true, "active_energy": true, "basal_energy_burned": true,
	"apple_exercise_time": true, "apple_stand_time": true,
	"flights_climbed": true, "walking_running_distance": true,
	"time_in_daylight": true, "apple_stand_hour": true,
}

func main() {
	dbPath := getEnv("DB_PATH", "./data/health.db")
	dryRun := len(os.Args) > 1 && os.Args[1] == "--dry-run"

	pass1Days := getEnvInt("DOWNSAMPLE_PASS1_DAYS", 14)
	pass2Days := getEnvInt("DOWNSAMPLE_PASS2_DAYS", 90)

	if dryRun {
		fmt.Println("DRY RUN — no changes will be made")
	}
	fmt.Printf("Pass 1: minute → hour  (older than %d days)\n", pass1Days)
	fmt.Printf("Pass 2: hour   → day   (older than %d days)\n\n", pass2Days)

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer db.Close()

	metrics, err := listMetrics(db)
	if err != nil {
		log.Fatalf("list metrics: %v", err)
	}

	// Cleanup temp table from any previous failed run
	db.Exec("DROP TABLE IF EXISTS _ds_tmp")

	totalDel, totalIns := 0, 0

	fmt.Printf("── Pass 1: minute → hour (older than %d days) ──\n", pass1Days)
	bucketHour := "substr(date, 1, 13) || ':00:00'"
	cutoff1 := fmt.Sprintf("date('now', '-%d days')", pass1Days)
	for _, m := range metrics {
		d, i, err := downsampleMetric(db, m, aggFor(m), bucketHour, cutoff1, dryRun)
		if err != nil {
			log.Printf("  ERROR %s: %v", m, err)
			continue
		}
		if d > 0 {
			fmt.Printf("  %-42s %6d → %6d rows\n", m, d, i)
			totalDel += d
			totalIns += i
		}
	}

	fmt.Printf("\n── Pass 2: hour → day (older than %d days) ──\n", pass2Days)
	bucketDay := "substr(date, 1, 10) || ' 00:00:00'"
	cutoff2 := fmt.Sprintf("date('now', '-%d days')", pass2Days)
	for _, m := range metrics {
		d, i, err := downsampleMetric(db, m, aggFor(m), bucketDay, cutoff2, dryRun)
		if err != nil {
			log.Printf("  ERROR %s: %v", m, err)
			continue
		}
		if d > 0 {
			fmt.Printf("  %-42s %6d → %6d rows\n", m, d, i)
			totalDel += d
			totalIns += i
		}
	}

	fmt.Println()
	if dryRun {
		fmt.Printf("dry run: would remove %d rows, keep %d rows (save %d)\n", totalDel-totalIns, totalIns, totalDel-totalIns)
	} else {
		fmt.Printf("done: removed %d duplicate rows, kept %d aggregated rows\n", totalDel-totalIns, totalIns)
	}
}

func aggFor(metric string) string {
	if sumMetrics[metric] {
		return "SUM"
	}
	return "AVG"
}

func listMetrics(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT DISTINCT metric_name FROM metric_points ORDER BY metric_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		rows.Scan(&m)
		out = append(out, m)
	}
	return out, rows.Err()
}

func downsampleMetric(db *sql.DB, metric, agg, bucketExpr, cutoff string, dryRun bool) (deleted, inserted int, err error) {
	var count int
	db.QueryRow("SELECT COUNT(*) FROM metric_points WHERE metric_name = ? AND substr(date,1,10) < "+cutoff, metric).Scan(&count)
	if count == 0 {
		return 0, 0, nil
	}

	// Count how many rows will remain after aggregation
	var newCount int
	db.QueryRow(fmt.Sprintf(
		`SELECT COUNT(*) FROM (SELECT %s as b, source FROM metric_points WHERE metric_name = ? AND substr(date,1,10) < %s AND qty > 0 GROUP BY b, source)`,
		bucketExpr, cutoff,
	), metric).Scan(&newCount)

	if dryRun {
		return count, newCount, nil
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	// Staging table (temp database, outside transaction — safe as read-only staging)
	db.Exec("DROP TABLE IF EXISTS _ds_tmp")
	if _, err = db.Exec(`CREATE TEMP TABLE _ds_tmp (
		health_record_id INTEGER, metric_name TEXT, units TEXT,
		date TEXT, qty REAL, source TEXT
	)`); err != nil {
		return 0, 0, fmt.Errorf("create temp: %w", err)
	}

	if _, err = db.Exec(fmt.Sprintf(`
		INSERT INTO _ds_tmp (health_record_id, metric_name, units, date, qty, source)
		SELECT MIN(health_record_id), metric_name, MIN(units),
		       %s AS d, %s(qty), source
		FROM metric_points
		WHERE metric_name = ? AND substr(date,1,10) < %s AND qty > 0
		GROUP BY metric_name, d, source
	`, bucketExpr, agg, cutoff), metric); err != nil {
		db.Exec("DROP TABLE IF EXISTS _ds_tmp")
		return 0, 0, fmt.Errorf("aggregate: %w", err)
	}

	res, err := tx.Exec("DELETE FROM metric_points WHERE metric_name = ? AND substr(date,1,10) < "+cutoff, metric)
	if err != nil {
		db.Exec("DROP TABLE IF EXISTS _ds_tmp")
		return 0, 0, fmt.Errorf("delete: %w", err)
	}
	del, _ := res.RowsAffected()

	res2, err := tx.Exec(`INSERT INTO metric_points (health_record_id, metric_name, units, date, qty, source)
		SELECT health_record_id, metric_name, units, date, qty, source FROM _ds_tmp`)
	if err != nil {
		db.Exec("DROP TABLE IF EXISTS _ds_tmp")
		return 0, 0, fmt.Errorf("insert: %w", err)
	}
	ins, _ := res2.RowsAffected()

	db.Exec("DROP TABLE IF EXISTS _ds_tmp")

	return int(del), int(ins), tx.Commit()
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
