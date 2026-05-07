package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"health-receiver/internal/ctxdb"
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

// haeTimeLayout is the timestamp format Health Auto Export uses in workout
// fields (e.g. "2026-05-05 17:25:36 +0200"). It is the same on both manual
// JSON exports and Auto Export REST payloads.
const haeTimeLayout = "2006-01-02 15:04:05 -0700"

// maxWorkoutsBodyBytes caps the request body for /health/workouts. A typical
// outdoor run is ~500 KB JSON; 32 MB comfortably covers any realistic batch
// (a week of workouts including multiple runs with route polylines) while
// preventing pathological uploads from blowing memory.
const maxWorkoutsBodyBytes = 32 * 1024 * 1024

type qtyUnits struct {
	Qty   float64 `json:"qty"`
	Units string  `json:"units"`
}

type hrSample struct {
	Avg  float64 `json:"Avg"`
	Date string  `json:"date"`
}

type qtyDate struct {
	Qty  float64 `json:"qty"`
	Date string  `json:"date"`
}

// haeWorkout matches the manual-export shape produced by Health Auto Export.
// The Auto Export REST payload uses the same per-workout structure inside
// `data.workouts`. We deliberately do not deserialise route, walkingAndRunningDistance,
// activeEnergy, stepCount samples or speed timeseries — they are not used.
type haeWorkout struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Start    string  `json:"start"`
	End      string  `json:"end"`
	Duration float64 `json:"duration"`
	IsIndoor bool    `json:"isIndoor"`
	Location string  `json:"location"`

	AvgHeartRate       *qtyUnits `json:"avgHeartRate,omitempty"`
	MaxHeartRate       *qtyUnits `json:"maxHeartRate,omitempty"`
	ActiveEnergyBurned *qtyUnits `json:"activeEnergyBurned,omitempty"`
	Intensity          *qtyUnits `json:"intensity,omitempty"`
	Distance           *qtyUnits `json:"distance,omitempty"`
	AvgSpeed           *qtyUnits `json:"avgSpeed,omitempty"`
	MaxSpeed           *qtyUnits `json:"maxSpeed,omitempty"`
	ElevationUp        *qtyUnits `json:"elevationUp,omitempty"`
	StepCadence        *qtyUnits `json:"stepCadence,omitempty"`
	Temperature        *qtyUnits `json:"temperature,omitempty"`
	Humidity           *qtyUnits `json:"humidity,omitempty"`

	// Read only to compute per-zone seconds. Discarded after that.
	HeartRateData []hrSample `json:"heartRateData,omitempty"`
	// Read only to compute total step count. Discarded after that.
	StepCount []qtyDate `json:"stepCount,omitempty"`
}

type workoutsPayload struct {
	Data struct {
		Workouts []haeWorkout `json:"workouts"`
	} `json:"data"`
}

// parseWorkoutsBody accepts three top-level shapes:
//  1. {"data":{"workouts":[…]}}   — manual export and Auto Export REST default
//  2. {"workouts":[…]}            — bare object (defensive)
//  3. […]                          — top-level array (defensive)
func parseWorkoutsBody(body []byte) ([]haeWorkout, error) {
	var p workoutsPayload
	if err := json.Unmarshal(body, &p); err == nil && len(p.Data.Workouts) > 0 {
		return p.Data.Workouts, nil
	}
	var bare struct {
		Workouts []haeWorkout `json:"workouts"`
	}
	if err := json.Unmarshal(body, &bare); err == nil && len(bare.Workouts) > 0 {
		return bare.Workouts, nil
	}
	var arr []haeWorkout
	if err := json.Unmarshal(body, &arr); err == nil && len(arr) > 0 {
		return arr, nil
	}
	return nil, fmt.Errorf("payload contains no workouts")
}

func (h *Handler) workouts(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxWorkoutsBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("workouts: read body: %v", err)
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	db := ctxdb.FromContext(r.Context())
	rec := storage.Record{
		AutomationName: r.Header.Get("automation-name"),
		AutomationID:   r.Header.Get("automation-id"),
		SessionID:      r.Header.Get("session-id"),
		ContentType:    r.Header.Get("Content-Type"),
		Payload:        string(body),
	}
	recordID, err := db.InsertRaw(rec)
	if err != nil {
		log.Printf("workouts: insert raw: %v", err)
		http.Error(w, "failed to save record", http.StatusInternalServerError)
		return
	}

	workouts, err := parseWorkoutsBody(body)
	if err != nil {
		log.Printf("record %d: parse workouts: %v", recordID, err)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "id": recordID, "ingested": 0, "error": err.Error(),
		})
		return
	}

	ingested, failed := 0, 0
	for _, hw := range workouts {
		summary, convErr := convertHAEWorkout(hw, h.hrZones)
		if convErr != nil {
			log.Printf("record %d: convert workout %q: %v", recordID, hw.ID, convErr)
			failed++
			continue
		}
		if err := db.UpsertWorkout(recordID, summary); err != nil {
			log.Printf("record %d: upsert workout %s: %v", recordID, hw.ID, err)
			failed++
			continue
		}
		ingested++
	}
	log.Printf("record %d: workouts ingested=%d failed=%d total=%d", recordID, ingested, failed, len(workouts))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status": "ok", "id": recordID, "ingested": ingested, "failed": failed,
	})
}

func convertHAEWorkout(hw haeWorkout, zones health.HRZones) (storage.Workout, error) {
	if hw.ID == "" {
		return storage.Workout{}, fmt.Errorf("missing id")
	}
	start, err := time.Parse(haeTimeLayout, hw.Start)
	if err != nil {
		return storage.Workout{}, fmt.Errorf("parse start %q: %w", hw.Start, err)
	}
	end, err := time.Parse(haeTimeLayout, hw.End)
	if err != nil {
		return storage.Workout{}, fmt.Errorf("parse end %q: %w", hw.End, err)
	}
	out := storage.Workout{
		ExternalID:  hw.ID,
		Name:        hw.Name,
		StartTime:   start,
		EndTime:     end,
		DurationSec: hw.Duration,
		IsIndoor:    hw.IsIndoor,
		Location:    hw.Location,
	}
	if hw.AvgHeartRate != nil {
		v := hw.AvgHeartRate.Qty
		out.AvgHRBPM = &v
	}
	if hw.MaxHeartRate != nil {
		v := hw.MaxHeartRate.Qty
		out.MaxHRBPM = &v
	}
	if hw.ActiveEnergyBurned != nil {
		v := health.NormalizeEnergyKcal(hw.ActiveEnergyBurned.Qty, hw.ActiveEnergyBurned.Units)
		out.EnergyKcal = &v
	}
	if hw.Intensity != nil {
		v := hw.Intensity.Qty
		out.Intensity = &v
	}
	if hw.Distance != nil {
		v := health.NormalizeDistanceKm(hw.Distance.Qty, hw.Distance.Units)
		out.DistanceKm = &v
	}
	if hw.AvgSpeed != nil {
		v := health.NormalizeSpeedKmh(hw.AvgSpeed.Qty, hw.AvgSpeed.Units)
		out.AvgSpeedKmh = &v
	}
	if hw.MaxSpeed != nil {
		v := health.NormalizeSpeedKmh(hw.MaxSpeed.Qty, hw.MaxSpeed.Units)
		out.MaxSpeedKmh = &v
	}
	if hw.ElevationUp != nil {
		v := health.NormalizeMeters(hw.ElevationUp.Qty, hw.ElevationUp.Units)
		out.ElevationUpM = &v
	}
	if hw.StepCadence != nil {
		v := hw.StepCadence.Qty
		out.StepCadenceSpm = &v
	}
	if hw.Temperature != nil {
		v := health.NormalizeTempC(hw.Temperature.Qty, hw.Temperature.Units)
		out.TemperatureC = &v
	}
	if hw.Humidity != nil {
		v := hw.Humidity.Qty
		out.HumidityPct = &v
	}
	if len(hw.StepCount) > 0 {
		var n int
		for _, s := range hw.StepCount {
			n += int(s.Qty + 0.5)
		}
		out.StepCountTotal = &n
	}
	if zones.IsConfigured() && len(hw.HeartRateData) > 0 {
		samples := make([]health.HRSample, 0, len(hw.HeartRateData))
		for _, s := range hw.HeartRateData {
			t, err := time.Parse(haeTimeLayout, s.Date)
			if err != nil {
				continue
			}
			samples = append(samples, health.HRSample{Time: t, Avg: s.Avg})
		}
		secs := health.ComputeTimeInZones(samples, end, zones)
		z1, z2, z3, z4, z5 := secs[0], secs[1], secs[2], secs[3], secs[4]
		out.HRZ1Sec, out.HRZ2Sec, out.HRZ3Sec, out.HRZ4Sec, out.HRZ5Sec = &z1, &z2, &z3, &z4, &z5
	}
	return out, nil
}
