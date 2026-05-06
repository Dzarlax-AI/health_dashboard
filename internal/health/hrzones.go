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

// maxBucketSec caps the duration we ascribe to a single sample. HAE buckets
// are normally ~60s; gaps wider than five minutes likely indicate the watch
// stopped recording — counting them in full would over-inflate one zone.
const maxBucketSec = 300.0

// ComputeTimeInZones distributes total seconds across five HR zones based on
// each sample's average BPM and the time delta to the next sample (or the
// workout end for the final sample). Returns five integers (Z1..Z5).
//
// Algorithm:
//  1. Sort samples by time.
//  2. For each sample i, take the average BPM and the duration to sample i+1
//     (or workoutEnd for the last). Cap at maxBucketSec.
//  3. Find the zone for that BPM and accumulate.
//
// If zones are not configured or there are no samples, all five values are 0.
func ComputeTimeInZones(samples []HRSample, workoutEnd time.Time, z HRZones) [5]int {
	var out [5]int
	if !z.configured || len(samples) == 0 {
		return out
	}
	sorted := make([]HRSample, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Time.Before(sorted[j].Time) })

	for i, s := range sorted {
		var until time.Time
		if i+1 < len(sorted) {
			until = sorted[i+1].Time
		} else {
			until = workoutEnd
		}
		dur := until.Sub(s.Time).Seconds()
		if dur <= 0 {
			continue
		}
		if dur > maxBucketSec {
			dur = maxBucketSec
		}
		zoneIdx := zoneFor(s.Avg, z)
		out[zoneIdx] += int(dur + 0.5)
	}
	return out
}

func zoneFor(bpm float64, z HRZones) int {
	switch {
	case bpm < z.Borders[0]:
		return 0
	case bpm < z.Borders[1]:
		return 1
	case bpm < z.Borders[2]:
		return 2
	case bpm < z.Borders[3]:
		return 3
	default:
		return 4
	}
}
