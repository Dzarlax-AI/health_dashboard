package storage

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/health"
)

// LocalShadowReadiness is the readout served by the private sleep shadow command.
type LocalShadowReadiness struct {
	Date         string                        `json:"date"`
	RawScore     int                           `json:"readiness_raw_score"`
	Score        int                           `json:"readiness_score"`
	Band         string                        `json:"readiness_band"`
	Serving      *health.ReadinessServingState `json:"readiness_serving"`
	Sleep        *LocalShadowSleep             `json:"sleep"`
	SleepQuality *health.SleepQualityBreakdown `json:"sleep_quality"`
}

// LocalShadowSleep distinguishes an unpublished date (no block) from a
// published date with no night left after a transfer (generation, null values).
type LocalShadowSleep struct {
	Generation  int64    `json:"generation"`
	Candidate   *string  `json:"candidate"`
	Revision    *int64   `json:"revision"`
	Total       *float64 `json:"total_hours"`
	Deep        *float64 `json:"deep_hours"`
	REM         *float64 `json:"rem_hours"`
	Core        *float64 `json:"core_hours"`
	Unspecified *float64 `json:"unspecified_hours"`
	Awake       *float64 `json:"awake_hours"`
	Conflict    *bool    `json:"stage_conflict"`
}

// GetLocalShadowReadiness reads the same Go readiness and safety rules used by
// the briefing, with the published shadow sleep block owning covered dates.
// profile is the shadow profile mapped to this DB's schema by the caller.
func (s *DB) GetLocalShadowReadiness(ctx context.Context, profile, date string) (*LocalShadowReadiness, error) {
	if _, inTransaction := s.pool.(*cacheTransaction); inTransaction {
		return s.getLocalShadowReadiness(ctx, profile, date)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	result, err := unit.getLocalShadowReadiness(ctx, profile, date)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *DB) getLocalShadowReadiness(ctx context.Context, profile, date string) (*LocalShadowReadiness, error) {
	if date == "" {
		var latest *string
		err := s.pool.QueryRow(ctx, `
			SELECT greatest(
				(SELECT max(substring(hour,1,10)) FROM hourly_metrics),
				(SELECT max(wake_date)::text FROM sleep_shadow.recalculation WHERE profile=$1))`, profile).
			Scan(&latest)
		if err != nil || latest == nil {
			return nil, err
		}
		date = *latest
	}
	if _, err := time.Parse(time.DateOnly, date); err != nil {
		return nil, err
	}
	blocks, baseline, err := s.loadLocalShadowSleep(ctx, profile, date)
	if err != nil {
		return nil, err
	}
	return s.readinessWithSleep(ctx, date, blocks, baseline), nil
}

func (s *DB) readinessWithSleep(ctx context.Context, date string, blocks map[string]*LocalShadowSleep, baseline []float64) *LocalShadowReadiness {
	data := s.rawMetricsFromDailyScores(date)
	if data == nil {
		data = s.rawMetricsFromPoints(date)
	}
	applyLocalShadowSleep(data, blocks)
	resp := health.ComputeReadinessBriefing(*data, "en")
	var checkin *health.SubjectiveCheckinSummary
	if row, err := s.GetTodayCheckin(date, CheckinSourceTelegram); err == nil && row != nil {
		checkin = &health.SubjectiveCheckinSummary{Status: row.Status, Answer: row.Answer}
	}
	var sleepDisruption *health.MetricEvidenceInput
	if block := blocks[date]; block != nil {
		sleepDisruption = localShadowSleepDisruption(block.Total, baseline)
	}
	input := s.buildIllnessEvidenceInput(date, checkin, sleepDisruption)
	resp.IllnessSuspicion = health.ComputeIllnessSuspicion(input)
	health.ApplyIllnessSafetyCap(resp, health.GetStrings("en"))
	return &LocalShadowReadiness{
		Date: date, RawScore: resp.ReadinessRawScore, Score: resp.ReadinessDisplayScore,
		Band: resp.ReadinessBand, Serving: resp.ReadinessServing,
		Sleep: blocks[date], SleepQuality: resp.SleepQuality,
	}
}

func (s *DB) loadLocalShadowSleep(ctx context.Context, profile, date string) (map[string]*LocalShadowSleep, []float64, error) {
	// Read ownership and all sleep fields in the same snapshot. A recalculated
	// date without a daily row owns the absence as well as a published night.
	rows, err := s.pool.Query(ctx, `
		SELECT day::text,r.generation,d.candidate,d.revision,d.sleep_seconds/3600,
		       d.deep_seconds/3600,d.rem_seconds/3600,d.core_seconds/3600,
		       d.unspecified_seconds/3600,d.awake_seconds/3600,d.stage_conflict,h.sleep_total
		FROM (SELECT $2::text::date-i AS day FROM generate_series(0,30) AS days(i)) days
		LEFT JOIN sleep_shadow.recalculation r ON r.wake_date=day AND r.profile=$1
		LEFT JOIN sleep_shadow.daily d USING (profile,wake_date)
		LEFT JOIN daily_scores h ON h.date=day::text
		ORDER BY day DESC`, profile, date)
	return readSleepBlocks(rows, err, date)
}

func readSleepBlocks(rows pgx.Rows, err error, date string) (map[string]*LocalShadowSleep, []float64, error) {
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	blocks := make(map[string]*LocalShadowSleep)
	var baseline []float64
	for rows.Next() {
		var day string
		var generation *int64
		var legacy *float64
		var block LocalShadowSleep
		if err := rows.Scan(&day, &generation, &block.Candidate, &block.Revision, &block.Total,
			&block.Deep, &block.REM, &block.Core, &block.Unspecified, &block.Awake, &block.Conflict, &legacy); err != nil {
			return nil, nil, err
		}
		value := legacy
		if generation != nil {
			block.Generation = *generation
			blocks[day] = &block
			value = block.Total
		}
		if day != date && value != nil && *value > 0 && isFiniteFloat(*value) {
			baseline = append(baseline, *value)
		}
	}
	return blocks, baseline, rows.Err()
}

func applyLocalShadowSleep(data *health.RawMetrics, blocks map[string]*LocalShadowSleep) {
	firstDate := subtractDays(data.LastDate, 29)
	byDate := make(map[string]health.DailyHealthMetrics, len(data.Daily))
	for _, day := range data.Daily {
		byDate[day.Date] = day
	}
	changed := false
	for date, block := range blocks {
		if date < firstDate {
			continue
		}
		day := byDate[date]
		day.Date = date
		day.Sleep, day.Deep, day.REM = block.Total, block.Deep, block.REM
		day.Core, day.Unspecified, day.Awake = block.Core, block.Unspecified, block.Awake
		byDate[date] = day
		changed = true
	}
	if !changed {
		return
	}
	data.Daily = data.Daily[:0]
	for _, day := range byDate {
		data.Daily = append(data.Daily, day)
	}
	slices.SortFunc(data.Daily, func(a, b health.DailyHealthMetrics) int {
		return strings.Compare(b.Date, a.Date)
	})
	data.Sleep, data.Deep, data.REM, data.Awake = nil, nil, nil, nil
	nightValues := make(map[string]float64, len(data.NightSleepByDate))
	for _, day := range data.NightSleepByDate {
		if day.Date >= firstDate && day.Date <= data.LastDate {
			nightValues[day.Date] = day.Val
		}
	}
	for _, day := range data.Daily {
		owned := blocks[day.Date] != nil
		appendStage := func(series *[]float64, value *float64) {
			if value != nil && (*value > 0 || (owned && *value == 0)) {
				*series = append(*series, *value)
			}
		}
		appendStage(&data.Sleep, day.Sleep)
		appendStage(&data.Deep, day.Deep)
		appendStage(&data.REM, day.REM)
		appendStage(&data.Awake, day.Awake)
		_, hasLegacyNight := nightValues[day.Date]
		if owned || !hasLegacyNight {
			if day.Sleep != nil && (*day.Sleep > 0 || (owned && *day.Sleep == 0)) {
				nightValues[day.Date] = *day.Sleep
			} else {
				delete(nightValues, day.Date)
			}
		}
	}
	nights := make([]health.DatedValue, 0, len(nightValues))
	for date, value := range nightValues {
		nights = append(nights, health.DatedValue{Date: date, Val: value})
	}
	slices.SortFunc(nights, func(a, b health.DatedValue) int { return strings.Compare(b.Date, a.Date) })
	data.NightSleep = datedValues(nights)
	block := blocks[data.LastDate]
	if block == nil {
		return
	}
	// Today's missing values cannot promote yesterday into the acute slot.
	if block.Total == nil {
		data.Sleep, data.NightSleep = nil, nil
	}
	if block.Awake == nil {
		data.Awake = nil
	}
	data.SleepStagesDateAligned = true
	e := data.ReadinessEvidence
	component := func(metric string, value *float64) health.ReadinessComponentEvidence {
		sourceDate := ""
		if value != nil {
			sourceDate = data.LastDate
		}
		return readinessComponent(metric, data.LastDate, sourceDate, value, 0, "")
	}
	e.SleepDuration = component("sleep_total", block.Total)
	e.SleepDeep = component("sleep_deep", block.Deep)
	e.SleepREM = component("sleep_rem", block.REM)
	e.SleepCore = component("sleep_core", block.Core)
	e.SleepAwake = component("sleep_awake", block.Awake)
	e.SleepQuality = sleepQualityEvidence(data.LastDate, e.SleepDuration.SourceDate, block.Total, block.Deep, block.Awake)
}

func localShadowSleepDisruption(current *float64, baseline []float64) *health.MetricEvidenceInput {
	e := &health.MetricEvidenceInput{Metric: "sleep_total", Unit: "hr", Method: "local_shadow_sleep", Status: "missing"}
	if current == nil {
		return e
	}
	e.Value = *current
	if len(baseline) >= 7 {
		if sd := stddevFloat(baseline); sd > 0 {
			e.Status = "ok"
			e.Baseline = avgFloat(baseline)
			e.ZScore = (e.Value - e.Baseline) / sd
			return e
		}
	}
	if len(baseline) > 0 {
		e.Status = "warmup"
	}
	return e
}
