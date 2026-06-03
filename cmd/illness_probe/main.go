package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

func main() {
	schema := flag.String("schema", "health", "tenant schema")
	lang := flag.String("lang", "en", "briefing language")
	date := flag.String("date", "", "specific local date YYYY-MM-DD; defaults to latest briefing")
	days := flag.Int("days", 0, "retrospective sweep over recent daily_scores dates")
	noCheckin := flag.Bool("no-checkin", false, "ignore subjective check-in evidence")
	flag.Parse()

	db, err := storage.NewWithSchema(context.Background(), os.Getenv("DATABASE_URL"), *schema)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if *days > 0 {
		runSweep(db, *schema, *days, !*noCheckin)
		return
	}

	if *date != "" {
		printDate(db, *schema, *date, !*noCheckin)
		return
	}

	briefing, err := db.GetHealthBriefing(*lang)
	if err != nil {
		log.Fatal(err)
	}
	out := map[string]any{
		"schema":             *schema,
		"date":               briefing.Date,
		"readiness_band":     briefing.ReadinessBand,
		"energy_verdict":     nil,
		"illness_suspicion":  briefing.IllnessSuspicion,
		"subjective_checkin": briefing.SubjectiveCheckin,
	}
	if briefing.EnergyBank != nil {
		out["energy_verdict"] = briefing.EnergyBank.ActionVerdict
	}
	printJSON(out)
}

func printDate(db *storage.DB, schema, date string, includeCheckin bool) {
	var checkin *health.SubjectiveCheckinSummary
	if includeCheckin {
		checkin = checkinForDate(db, date)
	}
	ill := db.BuildIllnessEvidenceInput(date, checkin)
	out := map[string]any{
		"schema":             schema,
		"date":               date,
		"illness_suspicion":  health.ComputeIllnessSuspicion(ill),
		"subjective_checkin": checkin,
	}
	printJSON(out)
}

func runSweep(db *storage.DB, schema string, days int, includeCheckin bool) {
	if days < 1 {
		days = 1
	}
	if days > 3650 {
		days = 3650
	}
	rows, err := db.QueryReadOnly(fmt.Sprintf("SELECT date FROM daily_scores ORDER BY date DESC LIMIT %d", days))
	if err != nil {
		log.Fatal(err)
	}
	counts := map[string]int{"none": 0, "low": 0, "moderate": 0, "high": 0}
	type hit struct {
		Date       string   `json:"date"`
		Confidence string   `json:"confidence"`
		Signals    []string `json:"signals"`
	}
	var hits []hit
	for _, row := range rows {
		date, _ := row["date"].(string)
		if date == "" {
			continue
		}
		var checkin *health.SubjectiveCheckinSummary
		if includeCheckin {
			checkin = checkinForDate(db, date)
		}
		ill := health.ComputeIllnessSuspicion(db.BuildIllnessEvidenceInput(date, checkin))
		counts[ill.Confidence]++
		if ill.Confidence == "moderate" || ill.Confidence == "high" {
			h := hit{Date: date, Confidence: ill.Confidence}
			for _, sig := range ill.Signals {
				if sig.Status == "ok" && sig.Strength != "weak" {
					h.Signals = append(h.Signals, sig.Metric+":"+sig.Strength)
				}
			}
			hits = append(hits, h)
		}
	}
	printJSON(map[string]any{"schema": schema, "days": days, "counts": counts, "top_hits": hits})
}

func checkinForDate(db *storage.DB, date string) *health.SubjectiveCheckinSummary {
	row, err := db.GetTodayCheckin(date, storage.CheckinSourceTelegram)
	if err != nil || row == nil {
		return nil
	}
	return &health.SubjectiveCheckinSummary{Status: row.Status, Answer: row.Answer}
}

func printJSON(v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(data))
}
