package health

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HRZones holds the four BPM borders defining five training zones:
//
//	Z1 (recovery):    bpm <  Borders[0]
//	Z2 (aerobic):     Borders[0] ≤ bpm < Borders[1]
//	Z3 (tempo):       Borders[1] ≤ bpm < Borders[2]
//	Z4 (threshold):   Borders[2] ≤ bpm < Borders[3]
//	Z5 (VO2max):      bpm ≥ Borders[3]
//
// Borders are configured by the user via HEALTH_HR_ZONES_BPM. The most
// physiologically accurate way to derive them is the Karvonen (HR Reserve)
// method using observed MaxHR and resting HR; %MaxHR is a simpler fallback
// that aligns with what Apple Watch / Strava show by default.
type HRZones struct {
	Borders    [4]float64
	configured bool
}

// ParseHRZones parses HEALTH_HR_ZONES_BPM = "B1,B2,B3,B4". Empty string
// returns an unconfigured zero value (IsConfigured() == false), which means
// the ingest path will leave hr_z*_sec columns as NULL. Invalid input returns
// an error and the caller should log it and continue with no zones.
func ParseHRZones(s string) (HRZones, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return HRZones{}, nil
	}
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return HRZones{}, fmt.Errorf("HEALTH_HR_ZONES_BPM must be 4 comma-separated numbers, got %d", len(parts))
	}
	var z HRZones
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return HRZones{}, fmt.Errorf("HEALTH_HR_ZONES_BPM[%d]: %w", i, err)
		}
		if v <= 0 || v > 240 {
			return HRZones{}, fmt.Errorf("HEALTH_HR_ZONES_BPM[%d]: %.0f bpm out of plausible range (1..240)", i, v)
		}
		if i > 0 && v <= z.Borders[i-1] {
			return HRZones{}, fmt.Errorf("HEALTH_HR_ZONES_BPM must be strictly ascending")
		}
		z.Borders[i] = v
	}
	z.configured = true
	return z, nil
}

// IsConfigured reports whether the env var was supplied and parsed cleanly.
func (z HRZones) IsConfigured() bool { return z.configured }

// HRSample is one bucketed heart-rate sample as emitted by Health Auto
// Export (typically a one-minute average).
type HRSample struct {
	Time time.Time
	Avg  float64
}

// WorkoutZoneInput is the normalized capture for workout_hr_zones_v1. Preserve
// the ingest sort's ordering of tied instants and the precision of Go timestamps.
type WorkoutZoneInput struct {
	Samples   []workoutZoneSample `json:"samples"`
	EndSecond int64               `json:"end_second"`
	EndNano   int                 `json:"end_nano"`
	Borders   []string            `json:"borders"`
}

type workoutZoneSample struct {
	Second int64   `json:"second"`
	Nano   int     `json:"nano"`
	Avg    float64 `json:"avg"`
}

func NewWorkoutZoneInput(samples []HRSample, end time.Time, zones HRZones) WorkoutZoneInput {
	input := WorkoutZoneInput{Samples: []workoutZoneSample{}, EndSecond: end.Unix(), EndNano: end.Nanosecond()}
	if !zones.IsConfigured() {
		return input
	}
	for _, border := range zones.Borders {
		input.Borders = append(input.Borders, strconv.FormatFloat(border, 'g', -1, 64))
	}
	input.Samples = make([]workoutZoneSample, 0, len(samples))
	sorted := append([]HRSample(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Time.Before(sorted[j].Time) })
	for _, s := range sorted {
		input.Samples = append(input.Samples, workoutZoneSample{s.Time.Unix(), s.Time.Nanosecond(), s.Avg})
	}
	return input
}
