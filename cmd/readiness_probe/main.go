package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"

	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

var countsByDate = map[string]map[string]int{}

type dayRollup struct {
	Date    string
	HRV     *float64
	RHR     *float64
	Sleep   *float64
	Deep    *float64
	REM     *float64
	Core    *float64
	Awake   *float64
	Steps   *float64
	Cal     *float64
	Ex      *float64
	SpO2    *float64
	VO2     *float64
	Resp    *float64
	NightHR *float64
	Counts  map[string]int
}

type dayResult struct {
	Date                string   `json:"date"`
	Raw                 int      `json:"raw"`
	Display             int      `json:"display"`
	Band                string   `json:"band"`
	Confidence          string   `json:"confidence"`
	CapReason           string   `json:"cap_reason,omitempty"`
	Illness             string   `json:"illness,omitempty"`
	Checkin             string   `json:"checkin,omitempty"`
	Capped              bool     `json:"capped"`
	ComponentConditions []string `json:"component_conditions,omitempty"`
}

func main() {
	schema := flag.String("schema", "health", "tenant schema")
	from := flag.String("from", "", "inclusive start date")
	to := flag.String("to", "", "inclusive end date")
	days := flag.Int("days", 180, "recent daily_scores dates to evaluate when from/to are empty")
	lang := flag.String("lang", "en", "briefing language")
	noIllness := flag.Bool("no-illness", false, "skip illness evidence and check-in lookup")
	flag.Parse()

	db, err := storage.NewWithSchema(context.Background(), os.Getenv("DATABASE_URL"), *schema)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	dates, err := targetDates(db, *from, *to, *days)
	if err != nil {
		log.Fatal(err)
	}
	results := make([]dayResult, 0, len(dates))
	for _, date := range dates {
		res, err := computeDate(db, date, *lang, !*noIllness)
		if err != nil {
			log.Printf("date %s: %v", date, err)
			continue
		}
		results = append(results, res)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Date < results[j].Date })

	out := map[string]any{
		"schema":      *schema,
		"days_tested": len(results),
		"summary":     summarize(results),
		"hits":        interesting(results),
	}
	printJSON(out)
}

func targetDates(db *storage.DB, from, to string, days int) ([]string, error) {
	var rows []map[string]any
	var err error
	if from != "" || to != "" {
		if from == "" || to == "" {
			return nil, fmt.Errorf("--from and --to must be provided together")
		}
		rows, err = db.QueryReadOnly("SELECT date FROM daily_scores WHERE date >= $1 AND date <= $2 ORDER BY date", from, to)
	} else {
		if days < 1 {
			days = 1
		}
		rows, err = db.QueryReadOnly(fmt.Sprintf("SELECT date FROM daily_scores ORDER BY date DESC LIMIT %d", days))
	}
	if err != nil {
		return nil, err
	}
	dates := make([]string, 0, len(rows))
	for _, row := range rows {
		if date, ok := row["date"].(string); ok && date != "" {
			dates = append(dates, date)
		}
	}
	return dates, nil
}

func computeDate(db *storage.DB, date, lang string, includeIllness bool) (dayResult, error) {
	window, err := loadWindow(db, date)
	if err != nil {
		return dayResult{}, err
	}
	if len(window) == 0 {
		return dayResult{}, fmt.Errorf("no daily_scores rows")
	}
	raw := buildRawMetrics(date, window)
	resp := health.ComputeBriefing(raw, lang)

	illnessConfidence := "skipped"
	var checkin *health.SubjectiveCheckinSummary
	if includeIllness {
		checkin = checkinForDate(db, date)
		ill := health.ComputeIllnessSuspicion(db.BuildIllnessEvidenceInput(date, checkin))
		resp.SubjectiveCheckin = checkin
		resp.IllnessSuspicion = ill
		health.ApplyIllnessSafetyCap(resp, health.GetStrings(lang))
		illnessConfidence = ill.Confidence
	}

	res := dayResult{
		Date:       date,
		Raw:        resp.ReadinessRawScore,
		Display:    resp.ReadinessDisplayScore,
		Band:       resp.ReadinessBand,
		Confidence: resp.ReadinessConfidence,
		CapReason:  resp.ReadinessCapReason,
		Illness:    illnessConfidence,
		Capped:     resp.ReadinessDisplayScore < resp.ReadinessRawScore,
	}
	if checkin != nil {
		res.Checkin = checkin.Answer
	}
	for _, c := range resp.ReadinessComponents {
		if c.MissingReason != "" {
			res.ComponentConditions = append(res.ComponentConditions, c.Metric+":"+c.MissingReason)
			continue
		}
		if c.Freshness != "" && c.Freshness != health.ReadinessFreshnessOK {
			res.ComponentConditions = append(res.ComponentConditions, c.Metric+":"+c.Freshness)
			continue
		}
		if c.Confidence != "" && c.Confidence != health.ReadinessConfidenceFinal {
			res.ComponentConditions = append(res.ComponentConditions, c.Metric+":"+c.Confidence)
		}
	}
	return res, nil
}

func loadWindow(db *storage.DB, date string) ([]dayRollup, error) {
	rows, err := db.QueryReadOnly(`
		SELECT date, hrv_avg, rhr_avg, sleep_total, sleep_deep, sleep_rem,
		       sleep_core, sleep_awake, steps, calories, exercise_min,
		       spo2_avg, vo2_avg, resp_avg, baseline_hr_overnight
		FROM daily_scores
		WHERE date >= ($1::date - interval '29 days')::text
		  AND date <= $1
		  AND (hrv_avg IS NOT NULL OR sleep_total IS NOT NULL OR steps IS NOT NULL)
		ORDER BY date DESC
		LIMIT 30`, date)
	if err != nil {
		return nil, err
	}
	out := make([]dayRollup, 0, len(rows))
	for _, row := range rows {
		d := dayRollup{
			Date:    stringVal(row["date"]),
			HRV:     floatPtr(row["hrv_avg"]),
			RHR:     floatPtr(row["rhr_avg"]),
			Sleep:   floatPtr(row["sleep_total"]),
			Deep:    floatPtr(row["sleep_deep"]),
			REM:     floatPtr(row["sleep_rem"]),
			Core:    floatPtr(row["sleep_core"]),
			Awake:   floatPtr(row["sleep_awake"]),
			Steps:   floatPtr(row["steps"]),
			Cal:     floatPtr(row["calories"]),
			Ex:      floatPtr(row["exercise_min"]),
			SpO2:    floatPtr(row["spo2_avg"]),
			VO2:     floatPtr(row["vo2_avg"]),
			Resp:    floatPtr(row["resp_avg"]),
			NightHR: floatPtr(row["baseline_hr_overnight"]),
			Counts:  map[string]int{},
		}
		if d.Date != "" {
			d.Counts = sampleCounts(db, d.Date)
			out = append(out, d)
		}
	}
	return out, nil
}

func buildRawMetrics(date string, rows []dayRollup) health.RawMetrics {
	d := health.RawMetrics{LastDate: date}
	appendPositive := func(dst *[]float64, p *float64) {
		if p != nil && *p > 0 {
			*dst = append(*dst, *p)
		}
	}
	for _, r := range rows {
		appendPositive(&d.HRV, r.HRV)
		appendPositive(&d.RHR, r.RHR)
		appendPositive(&d.Sleep, r.Sleep)
		appendPositive(&d.Deep, r.Deep)
		appendPositive(&d.REM, r.REM)
		appendPositive(&d.Awake, r.Awake)
		appendPositive(&d.Steps, r.Steps)
		appendPositive(&d.Cal, r.Cal)
		appendPositive(&d.Exercise, r.Ex)
		appendPositive(&d.SpO2, r.SpO2)
		appendPositive(&d.VO2, r.VO2)
		appendPositive(&d.Resp, r.Resp)
		if len(d.StepsWithDates) < 7 && r.Steps != nil && *r.Steps > 0 {
			d.StepsWithDates = append(d.StepsWithDates, health.DatedValue{Date: r.Date, Val: *r.Steps})
		}
		if len(d.HRVWithDates) < 7 && r.HRV != nil && *r.HRV > 0 {
			d.HRVWithDates = append(d.HRVWithDates, health.DatedValue{Date: r.Date, Val: *r.HRV})
		}
	}
	if len(rows) > 0 {
		d.ReadinessEvidence = buildEvidence(date, rows[0])
	}
	return d
}

func buildEvidence(date string, latest dayRollup) *health.ReadinessEvidenceInput {
	pick := func(metric string, value *float64, samples int) health.ReadinessComponentEvidence {
		c := health.ReadinessComponentEvidence{
			Metric:        metric,
			EvaluatedDate: date,
			SampleCount:   samples,
			Confidence:    health.ReadinessConfidenceFinal,
		}
		if value == nil || latest.Date != date {
			c.Freshness = health.ReadinessFreshnessMissing
			c.MissingReason = "missing_same_day_value"
			return c
		}
		c.Present = true
		c.SourceDate = latest.Date
		c.Value = value
		c.Freshness = health.ReadinessFreshnessOK
		return c
	}
	e := &health.ReadinessEvidenceInput{Date: date}
	e.HRV = pick("heart_rate_variability", latest.HRV, latest.Counts["heart_rate_variability"])
	if e.HRV.Present && e.HRV.SampleCount < health.MinSleepWindowHRVSamplesForFullConfidence {
		e.HRV.Confidence = health.ReadinessConfidenceProvisional
	}
	e.RHR = pick("resting_heart_rate", latest.RHR, latest.Counts["resting_heart_rate"])
	e.OvernightHR = pick("baseline_hr_overnight", latest.NightHR, 0)
	e.SleepDuration = pick("sleep_total", latest.Sleep, 0)
	e.Respiratory = pick("respiratory_rate", latest.Resp, latest.Counts["respiratory_rate"])
	e.SleepQuality = sleepQuality(date, latest)
	return e
}

func sleepQuality(date string, latest dayRollup) health.ReadinessComponentEvidence {
	c := health.ReadinessComponentEvidence{
		Metric:        "sleep_quality",
		EvaluatedDate: date,
		Confidence:    health.ReadinessConfidenceFinal,
	}
	if latest.Date != date || latest.Sleep == nil || *latest.Sleep <= 0 {
		c.Freshness = health.ReadinessFreshnessMissing
		c.MissingReason = "missing_sleep_quality"
		return c
	}
	if latest.Deep == nil && latest.Awake == nil {
		c.Freshness = health.ReadinessFreshnessMissing
		c.MissingReason = "missing_sleep_stage_details"
		return c
	}
	deepPct := 0.0
	if latest.Deep != nil {
		deepPct = *latest.Deep / *latest.Sleep * 100
	}
	awakePct := 0.0
	if latest.Awake != nil {
		awakePct = *latest.Awake / *latest.Sleep * 100
	}
	v := deepPct
	c.Present = true
	c.SourceDate = latest.Date
	c.Value = &v
	c.Freshness = health.ReadinessFreshnessOK
	if deepPct < 8 || awakePct > 10 {
		c.Confidence = health.ReadinessConfidenceLow
	}
	return c
}

func sampleCounts(db *storage.DB, date string) map[string]int {
	if counts, ok := countsByDate[date]; ok {
		return counts
	}
	rows, err := db.QueryReadOnly(`
		SELECT metric_name, COUNT(*)
		FROM metric_points
		WHERE quality = 'ok'
		  AND qty > 0
		  AND SUBSTRING(date,1,10) = $1
		  AND metric_name IN ('heart_rate_variability','resting_heart_rate','respiratory_rate')
		GROUP BY metric_name`, date)
	if err != nil {
		return map[string]int{}
	}
	out := map[string]int{}
	for _, row := range rows {
		out[stringVal(row["metric_name"])] = intVal(row["count"])
	}
	countsByDate[date] = out
	return out
}

func checkinForDate(db *storage.DB, date string) *health.SubjectiveCheckinSummary {
	row, err := db.GetTodayCheckin(date, storage.CheckinSourceTelegram)
	if err != nil || row == nil {
		return nil
	}
	return &health.SubjectiveCheckinSummary{Status: row.Status, Answer: row.Answer}
}

func summarize(results []dayResult) map[string]any {
	caps := map[string]int{}
	conf := map[string]int{}
	bands := map[string]int{}
	ill := map[string]int{}
	capped := 0
	for _, r := range results {
		conf[r.Confidence]++
		bands[r.Band]++
		ill[r.Illness]++
		if r.Capped {
			capped++
			caps[r.CapReason]++
		}
	}
	return map[string]any{
		"confidence":  conf,
		"bands":       bands,
		"illness":     ill,
		"capped_days": capped,
		"cap_reasons": caps,
	}
}

func interesting(results []dayResult) []dayResult {
	var out []dayResult
	for _, r := range results {
		if r.Capped || r.Illness == "moderate" || r.Illness == "high" || r.Confidence != health.ReadinessConfidenceFinal {
			out = append(out, r)
		}
	}
	return out
}

func stringVal(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func floatPtr(v any) *float64 {
	if v == nil {
		return nil
	}
	switch n := v.(type) {
	case float64:
		return &n
	case float32:
		f := float64(n)
		return &f
	case int64:
		f := float64(n)
		return &f
	case int:
		f := float64(n)
		return &f
	case []byte:
		return parseFloat(string(n))
	case string:
		return parseFloat(n)
	default:
		return parseFloat(fmt.Sprint(v))
	}
}

func parseFloat(s string) *float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &f
}

func intVal(v any) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case int32:
		return int(n)
	case int:
		return n
	case []byte:
		i, _ := strconv.Atoi(string(n))
		return i
	case string:
		i, _ := strconv.Atoi(n)
		return i
	default:
		i, _ := strconv.Atoi(fmt.Sprint(v))
		return i
	}
}

func printJSON(v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(data))
}
