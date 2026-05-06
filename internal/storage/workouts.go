package storage

import (
	"fmt"
	"strings"
	"time"
)

// Workout is the persisted summary of one Apple Health workout. Pointer
// fields are NULL-able in the DB — they are absent from the source payload
// either because the activity does not produce that signal (e.g. distance
// for indoor strength training) or because the user has not configured the
// HR zones env var (HR-zone columns).
type Workout struct {
	ExternalID  string    `json:"external_id"`
	Name        string    `json:"name"`
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time"`
	DurationSec float64   `json:"duration_sec"`
	IsIndoor    bool      `json:"is_indoor"`
	Location    string    `json:"location,omitempty"`

	AvgHRBPM   *float64 `json:"avg_hr_bpm,omitempty"`
	MaxHRBPM   *float64 `json:"max_hr_bpm,omitempty"`
	EnergyKcal *float64 `json:"energy_kcal,omitempty"`
	Intensity  *float64 `json:"intensity,omitempty"`

	DistanceKm     *float64 `json:"distance_km,omitempty"`
	AvgSpeedKmh    *float64 `json:"avg_speed_kmh,omitempty"`
	MaxSpeedKmh    *float64 `json:"max_speed_kmh,omitempty"`
	ElevationUpM   *float64 `json:"elevation_up_m,omitempty"`
	StepCountTotal *int     `json:"step_count_total,omitempty"`
	StepCadenceSpm *float64 `json:"step_cadence_spm,omitempty"`

	TemperatureC *float64 `json:"temperature_c,omitempty"`
	HumidityPct  *float64 `json:"humidity_pct,omitempty"`

	HRZ1Sec *int `json:"hr_z1_sec,omitempty"`
	HRZ2Sec *int `json:"hr_z2_sec,omitempty"`
	HRZ3Sec *int `json:"hr_z3_sec,omitempty"`
	HRZ4Sec *int `json:"hr_z4_sec,omitempty"`
	HRZ5Sec *int `json:"hr_z5_sec,omitempty"`
}

// WorkoutStats is the rollup returned by WorkoutStats.
type WorkoutStats struct {
	Count            int      `json:"count"`
	TotalDurationSec float64  `json:"total_duration_sec"`
	TotalDistanceKm  *float64 `json:"total_distance_km,omitempty"`
	TotalEnergyKcal  *float64 `json:"total_energy_kcal,omitempty"`
	AvgHRBPM         *float64 `json:"avg_hr_bpm,omitempty"`
	MaxHRBPM         *float64 `json:"max_hr_bpm,omitempty"`
	TotalZ1Sec       *int     `json:"total_hr_z1_sec,omitempty"`
	TotalZ2Sec       *int     `json:"total_hr_z2_sec,omitempty"`
	TotalZ3Sec       *int     `json:"total_hr_z3_sec,omitempty"`
	TotalZ4Sec       *int     `json:"total_hr_z4_sec,omitempty"`
	TotalZ5Sec       *int     `json:"total_hr_z5_sec,omitempty"`
}

// UpsertWorkout inserts or updates a workout keyed by external_id (the HAE
// UUID). The conflict resolution always replaces the row on re-upload — HAE
// occasionally re-emits a workout with refined samples after Apple Watch
// re-sync, and we want the latest snapshot to win.
func (s *DB) UpsertWorkout(healthRecordID int64, w Workout) error {
	ctx, cancel := queryCtx()
	defer cancel()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO workouts (
			health_record_id, external_id, name, start_time, end_time, duration_sec,
			is_indoor, location, avg_hr_bpm, max_hr_bpm, energy_kcal, intensity,
			distance_km, avg_speed_kmh, max_speed_kmh, elevation_up_m,
			step_count_total, step_cadence_spm, temperature_c, humidity_pct,
			hr_z1_sec, hr_z2_sec, hr_z3_sec, hr_z4_sec, hr_z5_sec
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25
		)
		ON CONFLICT (external_id) DO UPDATE SET
			received_at = NOW(),
			health_record_id = excluded.health_record_id,
			name = excluded.name,
			start_time = excluded.start_time,
			end_time = excluded.end_time,
			duration_sec = excluded.duration_sec,
			is_indoor = excluded.is_indoor,
			location = excluded.location,
			avg_hr_bpm = excluded.avg_hr_bpm,
			max_hr_bpm = excluded.max_hr_bpm,
			energy_kcal = excluded.energy_kcal,
			intensity = excluded.intensity,
			distance_km = excluded.distance_km,
			avg_speed_kmh = excluded.avg_speed_kmh,
			max_speed_kmh = excluded.max_speed_kmh,
			elevation_up_m = excluded.elevation_up_m,
			step_count_total = excluded.step_count_total,
			step_cadence_spm = excluded.step_cadence_spm,
			temperature_c = excluded.temperature_c,
			humidity_pct = excluded.humidity_pct,
			hr_z1_sec = excluded.hr_z1_sec,
			hr_z2_sec = excluded.hr_z2_sec,
			hr_z3_sec = excluded.hr_z3_sec,
			hr_z4_sec = excluded.hr_z4_sec,
			hr_z5_sec = excluded.hr_z5_sec
	`,
		nullableInt64(healthRecordID),
		w.ExternalID, w.Name, w.StartTime, w.EndTime, w.DurationSec,
		w.IsIndoor, nullableString(w.Location),
		w.AvgHRBPM, w.MaxHRBPM, w.EnergyKcal, w.Intensity,
		w.DistanceKm, w.AvgSpeedKmh, w.MaxSpeedKmh, w.ElevationUpM,
		w.StepCountTotal, w.StepCadenceSpm, w.TemperatureC, w.HumidityPct,
		w.HRZ1Sec, w.HRZ2Sec, w.HRZ3Sec, w.HRZ4Sec, w.HRZ5Sec,
	)
	if err != nil {
		return fmt.Errorf("upsert workout %s: %w", w.ExternalID, err)
	}
	return nil
}

// ListWorkouts returns workouts whose start_time falls in [from, to], sorted
// most-recent first. nameFilter, if non-empty, matches workouts.name exactly
// (e.g. "Outdoor Run").
func (s *DB) ListWorkouts(from, to time.Time, nameFilter string) ([]Workout, error) {
	ctx, cancel := queryCtx()
	defer cancel()

	q := `SELECT
			external_id, name, start_time, end_time, duration_sec,
			is_indoor, COALESCE(location, ''),
			avg_hr_bpm, max_hr_bpm, energy_kcal, intensity,
			distance_km, avg_speed_kmh, max_speed_kmh, elevation_up_m,
			step_count_total, step_cadence_spm, temperature_c, humidity_pct,
			hr_z1_sec, hr_z2_sec, hr_z3_sec, hr_z4_sec, hr_z5_sec
		FROM workouts
		WHERE start_time >= $1 AND start_time <= $2`
	args := []any{from, to}
	if nameFilter != "" {
		q += ` AND name = $3`
		args = append(args, nameFilter)
	}
	q += ` ORDER BY start_time DESC`

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list workouts: %w", err)
	}
	defer rows.Close()

	var out []Workout
	for rows.Next() {
		var w Workout
		if err := rows.Scan(
			&w.ExternalID, &w.Name, &w.StartTime, &w.EndTime, &w.DurationSec,
			&w.IsIndoor, &w.Location,
			&w.AvgHRBPM, &w.MaxHRBPM, &w.EnergyKcal, &w.Intensity,
			&w.DistanceKm, &w.AvgSpeedKmh, &w.MaxSpeedKmh, &w.ElevationUpM,
			&w.StepCountTotal, &w.StepCadenceSpm, &w.TemperatureC, &w.HumidityPct,
			&w.HRZ1Sec, &w.HRZ2Sec, &w.HRZ3Sec, &w.HRZ4Sec, &w.HRZ5Sec,
		); err != nil {
			return nil, fmt.Errorf("scan workout: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// GetWorkout returns one workout by external_id, or nil if not found.
func (s *DB) GetWorkout(externalID string) (*Workout, error) {
	ctx, cancel := queryCtx()
	defer cancel()

	var w Workout
	err := s.pool.QueryRow(ctx, `
		SELECT
			external_id, name, start_time, end_time, duration_sec,
			is_indoor, COALESCE(location, ''),
			avg_hr_bpm, max_hr_bpm, energy_kcal, intensity,
			distance_km, avg_speed_kmh, max_speed_kmh, elevation_up_m,
			step_count_total, step_cadence_spm, temperature_c, humidity_pct,
			hr_z1_sec, hr_z2_sec, hr_z3_sec, hr_z4_sec, hr_z5_sec
		FROM workouts WHERE external_id = $1`,
		externalID,
	).Scan(
		&w.ExternalID, &w.Name, &w.StartTime, &w.EndTime, &w.DurationSec,
		&w.IsIndoor, &w.Location,
		&w.AvgHRBPM, &w.MaxHRBPM, &w.EnergyKcal, &w.Intensity,
		&w.DistanceKm, &w.AvgSpeedKmh, &w.MaxSpeedKmh, &w.ElevationUpM,
		&w.StepCountTotal, &w.StepCadenceSpm, &w.TemperatureC, &w.HumidityPct,
		&w.HRZ1Sec, &w.HRZ2Sec, &w.HRZ3Sec, &w.HRZ4Sec, &w.HRZ5Sec,
	)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil, nil
		}
		return nil, fmt.Errorf("get workout %s: %w", externalID, err)
	}
	return &w, nil
}

// WorkoutStats returns aggregate counters for workouts in [from, to],
// optionally filtered by name. AVG/MAX HR are weighted equally per workout
// (no duration weighting) — for typical workouts of similar length this is
// close enough; callers wanting precise weighting should aggregate from the
// row list.
func (s *DB) WorkoutStats(from, to time.Time, nameFilter string) (WorkoutStats, error) {
	ctx, cancel := queryCtx()
	defer cancel()

	q := `SELECT
			COUNT(*),
			COALESCE(SUM(duration_sec), 0),
			SUM(distance_km),
			SUM(energy_kcal),
			AVG(avg_hr_bpm),
			MAX(max_hr_bpm),
			SUM(hr_z1_sec), SUM(hr_z2_sec), SUM(hr_z3_sec), SUM(hr_z4_sec), SUM(hr_z5_sec)
		FROM workouts
		WHERE start_time >= $1 AND start_time <= $2`
	args := []any{from, to}
	if nameFilter != "" {
		q += ` AND name = $3`
		args = append(args, nameFilter)
	}

	var st WorkoutStats
	err := s.pool.QueryRow(ctx, q, args...).Scan(
		&st.Count, &st.TotalDurationSec,
		&st.TotalDistanceKm, &st.TotalEnergyKcal,
		&st.AvgHRBPM, &st.MaxHRBPM,
		&st.TotalZ1Sec, &st.TotalZ2Sec, &st.TotalZ3Sec, &st.TotalZ4Sec, &st.TotalZ5Sec,
	)
	if err != nil {
		return WorkoutStats{}, fmt.Errorf("workout stats: %w", err)
	}
	return st, nil
}

// nullableString returns NULL for empty strings (avoids storing "" instead
// of NULL for optional location).
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullableInt64 returns NULL for zero values (so we don't FK-reference a
// non-existent health_records row).
func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
