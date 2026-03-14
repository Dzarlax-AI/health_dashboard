package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"

	_ "github.com/mattn/go-sqlite3"
)

func main() {
	dbPath := getEnv("DB_PATH", "./data/health.db")

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := ensureTable(db); err != nil {
		log.Fatalf("ensure table: %v", err)
	}

	rows, err := db.Query(`SELECT id, payload FROM health_records WHERE content_type != 'apple-health-import' ORDER BY id`)
	if err != nil {
		log.Fatalf("query: %v", err)
	}
	defer rows.Close()

	var totalRecords, totalPoints, skipped int

	for rows.Next() {
		var id int64
		var payload string
		if err := rows.Scan(&id, &payload); err != nil {
			log.Printf("scan id=%d: %v", id, err)
			continue
		}

		points, err := parseMetricPoints([]byte(payload))
		if err != nil {
			log.Printf("parse id=%d: %v — skipping", id, err)
			skipped++
			continue
		}

		tx, err := db.Begin()
		if err != nil {
			log.Fatalf("begin tx: %v", err)
		}
		stmt, err := tx.Prepare(`INSERT OR IGNORE INTO metric_points
			(health_record_id, metric_name, units, date, qty, source)
			VALUES (?, ?, ?, ?, ?, ?)`)
		if err != nil {
			tx.Rollback()
			log.Fatalf("prepare: %v", err)
		}
		for _, p := range points {
			if _, err := stmt.Exec(id, p.metricName, p.units, p.date, p.qty, p.source); err != nil {
				log.Printf("insert point id=%d metric=%s date=%s: %v", id, p.metricName, p.date, err)
			}
		}
		stmt.Close()
		if err := tx.Commit(); err != nil {
			log.Printf("commit id=%d: %v", id, err)
		}

		totalRecords++
		totalPoints += len(points)
		fmt.Printf("record %d → %d points\n", id, len(points))
	}

	fmt.Printf("\ndone: %d records, %d points inserted, %d skipped\n", totalRecords, totalPoints, skipped)
}

func ensureTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS metric_points (
		id               INTEGER PRIMARY KEY AUTOINCREMENT,
		health_record_id INTEGER NOT NULL REFERENCES health_records(id),
		received_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
		metric_name      TEXT NOT NULL,
		units            TEXT,
		date             TEXT NOT NULL,
		qty              REAL,
		source           TEXT
	)`)
	if err != nil {
		return err
	}
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_metric_points_name_date ON metric_points(metric_name, date)`)
	return err
}

type metricPoint struct {
	metricName string
	units      string
	date       string
	qty        float64
	source     string
}

type payloadShape struct {
	Data struct {
		Metrics []struct {
			Name  string            `json:"name"`
			Units string            `json:"units"`
			Data  []json.RawMessage `json:"data"`
		} `json:"metrics"`
	} `json:"data"`
}

func parseMetricPoints(body []byte) ([]metricPoint, error) {
	var p payloadShape
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	var points []metricPoint
	for _, m := range p.Data.Metrics {
		for _, raw := range m.Data {
			points = append(points, extractPoints(m.Name, m.Units, raw)...)
		}
	}
	return points, nil
}

func extractPoints(name, units string, raw json.RawMessage) []metricPoint {
	var base struct {
		Date   string `json:"date"`
		Source string `json:"source"`
	}
	json.Unmarshal(raw, &base)
	if base.Date == "" {
		return nil
	}

	pt := func(n string, u string, qty float64) metricPoint {
		return metricPoint{metricName: n, units: u, date: base.Date, qty: qty, source: base.Source}
	}

	switch name {
	case "heart_rate":
		var p struct{ Avg float64 }
		if json.Unmarshal(raw, &p) == nil {
			return []metricPoint{pt(name, units, p.Avg)}
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
			return []metricPoint{
				pt("sleep_deep",  "hr", p.Deep),
				pt("sleep_rem",   "hr", p.REM),
				pt("sleep_core",  "hr", p.Core),
				pt("sleep_awake", "hr", p.Awake),
				pt("sleep_total", "hr", p.TotalSleep),
			}
		}
	}
	var p struct{ Qty float64 `json:"qty"` }
	json.Unmarshal(raw, &p)
	return []metricPoint{pt(name, units, p.Qty)}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
