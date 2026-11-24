package storage

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"time"

	dbtmetrics "health-receiver/experiments/dbt_metrics"
)

// BaselineChannel names the autonomic metric whose personal baseline
// the v2.2 stress methodology asks for. Per STRESS_MEASUREMENT.md
// §4.1 the per-channel baselines are computed independently — a low
// HRV sample count, for example, must not gate out the HR baseline.
type BaselineChannel string

const (
	// ChannelHRAwake — median hourly_metrics.heart_rate.avg_val for
	// hour-of-day ∈ awake_window[d] over the rolling 30-day window.
	// The §4.4 hourly z-series consumer reads this for sustained
	// load detection.
	ChannelHRAwake BaselineChannel = "hr_awake"

	// ChannelHROvernight — median daily_scores.baseline_hr_overnight
	// (the column added in PR-2 / commit 459ee19) over the window.
	// Feeds the §4.5 channel-2 RHR-shift validation residual.
	ChannelHROvernight BaselineChannel = "hr_overnight"

	// ChannelHRV — median heart_rate_variability from metric_points,
	// filtered to overnight samples (cross-referenced against
	// sleep_analysis when available; falls through to whole-night
	// median otherwise per §4.1 caveat).
	ChannelHRV BaselineChannel = "hrv"

	// ChannelResp — median respiratory_rate from metric_points.
	ChannelResp BaselineChannel = "resp"

	// ChannelTemp — median wrist_temperature from metric_points.
	// Gated: requires ≥14 samples per §4.1 — the wrist sensor is
	// Apple Watch S8+ only and stale-overnight data accumulates
	// slowly, so a thin baseline (≤ ~2 weeks of nightly readings)
	// produces wild SDs that mis-calibrate the illness_signature
	// flag.
	ChannelTemp BaselineChannel = "temp"
)

// CalibrationState reflects how trustworthy the baseline is per the
// §4.1 state machine. Callers MUST respect this — using a
// `cold`-state baseline as a real baseline produces noise-driven
// z-scores that look like stress.
type CalibrationState string

const (
	// CalibrationCold — fewer than 3 valid samples in the 30-day
	// window. PersonalBaseline returns the zero value and ok=false;
	// downstream stress-flag computation must skip this channel for
	// the day.
	CalibrationCold CalibrationState = "cold"

	// CalibrationWarmup — 3-7 samples. PersonalBaseline returns the
	// median + SD, but the consumer should attach a
	// `calibration:warmup` flag so the briefing layer can soften
	// the verdict narrative (the baseline is noisy until ≥7 samples
	// accumulate).
	CalibrationWarmup CalibrationState = "warmup"

	// CalibrationSteady — ≥7 samples AND newest sample ≤14 days
	// old. Full-strength baseline; no calibration flag needed.
	CalibrationSteady CalibrationState = "steady"
)

// PersonalBaselineResult is the §4.1 baseline tuple — same shape
// `ENERGY_BANK.md`'s RHR baseline state machine produces. The MAD-
// based SD is robust to the 1-2 outlier samples that show up on
// post-illness recovery / heavy alcohol nights without manual
// trimming.
type PersonalBaselineResult struct {
	Median      float64
	MADSD       float64
	SampleCount int
	NewestAge   time.Duration
	State       CalibrationState
}

// SD floors per channel — minimum spread imposed on the MAD-derived
// SD before z-score callers consume it. STRESS_MEASUREMENT.md §4.1
// motivates the HR floor: users with very stable daytime HR can
// have MAD-SD as low as 1.5-2 bpm, and without a floor a routinely-
// busier-than-usual day reads as z≈2.5 = "false anomaly". Each
// channel gets its own floor sized for the natural day-to-day
// variation in that signal.
//
// Tunable from cohort data later (§6 Q6 — revisit in v2.5 once
// distributions across ≥3 users are known). For now these are
// best-effort defaults sized off domain knowledge.
const (
	SDFloorHR   = 3.0 // bpm — heart rate (awake or overnight)
	SDFloorHRV  = 5.0 // ms — RMSSD jitter dominates below this
	SDFloorResp = 0.5 // br/min — respiratory rate is tight, small floor OK
	SDFloorTemp = 0.1 // degC — wrist temp samples have ~0.1°C precision
)

// MinTempSamples is the per-channel sample-count override for wrist
// temperature. Apple Watch S8+ records temp only when worn during
// sleep (and only after ~5 nights of "calibration" on a fresh
// device), so a thin 14-sample minimum is significantly stricter
// than the global 3-sample warmup threshold. §4.1 step on the temp
// channel.
const MinTempSamples = 14

// PersonalBaseline computes the §4.1 rolling-30d personal baseline
// for the specified channel ending on `date`.
//
// Returns (result, ok). ok=false on:
//   - Hard DB error / unparseable date
//   - State = `cold` (fewer than 3 samples — caller must skip the
//     channel for that day)
//   - Wrist-temp channel with < MinTempSamples (tighter gate per
//     §4.1)
//
// `windowDays` defaults to 30 when <=0 — spec uses 30 throughout.
// `loc` must be the tenant's REPORT_TZ.
//
// Channel observations use the same dbt-owned statistics and calibration SQL as research reads.
func (s *DB) PersonalBaseline(
	date string,
	channel BaselineChannel,
	windowDays int,
	loc *time.Location,
) (PersonalBaselineResult, bool) {
	ctx, cancel := queryCtx()
	defer cancel()
	result, ok, _ := s.PersonalBaselineContext(ctx, date, channel, windowDays, loc)
	return result, ok
}

// PersonalBaselineContext exposes query failures to bounded cache maintenance.
func (s *DB) PersonalBaselineContext(
	ctx context.Context,
	date string,
	channel BaselineChannel,
	windowDays int,
	loc *time.Location,
) (PersonalBaselineResult, bool, error) {
	type baselineOutcome struct {
		value    PersonalBaselineResult
		eligible bool
	}
	out, err := withDecisionJournal(ctx, s, func(db *DB, journal bool) (baselineOutcome, error) {
		result, ok, err := db.evaluatePersonalBaseline(ctx, date, channel, windowDays, loc, journal)
		return baselineOutcome{result, ok}, err
	})
	return out.value, out.eligible, err
}

func (s *DB) evaluatePersonalBaseline(ctx context.Context, date string, channel BaselineChannel, windowDays int, loc *time.Location, journal bool) (PersonalBaselineResult, bool, error) {
	if loc == nil {
		loc = time.UTC
	}
	if windowDays <= 0 {
		windowDays = 30
	}
	d, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return PersonalBaselineResult{}, false, err
	}
	// Window is [d - windowDays, d - 1] inclusive — per §4.1
	// "rolling 30d ending BEFORE the day being scored" so the
	// current day's data never leaks into its own baseline.
	from := d.AddDate(0, 0, -windowDays).In(loc)
	until := d.In(loc)

	source, fromKey, untilKey := baselineSampleSource(channel, from, until, loc)
	if source == "" {
		return PersonalBaselineResult{State: CalibrationCold}, false, nil
	}
	// Calculate the distribution on the tenant database. Only candidate raw
	// clocks cross the connection. Under the C collation, clocks of equal width
	// with the same explicit offset sort chronologically. Other forms are
	// parsed individually, preserving the Go adapter's nanosecond and
	// local-wall-clock semantics even for old or malformed point timestamps.
	query := `with source_samples(value, clock) as (` + source + `),
		samples as materialized (
			select value::float8, clock from source_samples
			where value > '-Infinity'::float8 and value < 'Infinity'::float8
		), stats as (` + dbtmetrics.PersonalBaselineStatsSQL + `),
		clock_candidates as (
			select max(clock collate "C") as clock from samples
			group by case
				when clock ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2} ([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9]([.][0-9]+)? [+-][0-9]{4}$'
				     and pg_input_is_valid(clock, 'timestamp with time zone')
				then right(clock, 5) || ':' || length(clock)::text
				else clock end
		)
		select sample_count, median, sd, array(select clock from clock_candidates),
		       exists(select from source_samples where value is null)`
	var n int
	var median, sd *float64
	var clocks []string
	var nullValue bool
	var values json.RawMessage
	scan := []any{&n, &median, &sd, &clocks, &nullValue}
	if journal {
		query += `,
            (select coalesce(jsonb_agg(value order by value),'[]') from samples)`
		scan = append(scan, &values)
	}
	err = s.pool.QueryRow(ctx, query+" from stats", fromKey, untilKey).Scan(scan...)
	if err != nil {
		log.Printf("PersonalBaseline %s/%s: %v", date, channel, err)
		return PersonalBaselineResult{}, false, err
	}
	if nullValue {
		// Legacy point adapters scan into float64 and surface a NULL input as an error.
		return PersonalBaselineResult{}, false, errors.New("personal baseline: null sample value")
	}
	var newest time.Time
	for _, raw := range clocks {
		if ts := parseBaselineSampleTS(raw, loc); ts.After(newest) {
			newest = ts
		}
	}
	var ageNS *int64
	if !newest.IsZero() {
		ns := until.Sub(newest).Nanoseconds()
		ageNS = &ns
	}
	var result PersonalBaselineResult
	var eligible bool
	// This scalar projection consumes only the first statement's result. It
	// cannot observe another generation of samples if a sync arrives between calls.
	const inputs = `with summary(channel,sample_count,median,sd,age_ns) as (
		values ($1::text,$2::bigint,$3::float8,$4::float8,$5::bigint))`
	var retainedAge *int64
	sql := inputs + ", solved as (" + dbtmetrics.PersonalBaselineResultSQL + ")"
	args := []any{string(channel), n, median, sd, ageNS}
	if journal {
		args = append(args, date, loc.String(), windowDays, values)
		sql += `, decision_packets as (
          select 'personal_baseline'::text as metric,concat_ws('/',$6::text,$1::text,$8::int) as decision_key,
            1 as formula_version,jsonb_build_object('channel',$1::text,'anchor',$6::text,'zone',$7::text,
              'window_days',$8::int,'values',$9::jsonb,'age_ns',$5::bigint) as inputs,to_jsonb(b) as result from solved b
        )` + decisionJournalInsertSQL
	}
	err = s.pool.QueryRow(ctx, sql+" select * from solved", args...).Scan(&result.SampleCount, &retainedAge,
		&result.State, &eligible, &median, &sd)
	if err != nil {
		return PersonalBaselineResult{}, false, err
	}
	if retainedAge != nil {
		result.NewestAge = time.Duration(*retainedAge)
	}
	if eligible {
		result.Median, result.MADSD = *median, *sd
	}
	return result, eligible, nil
}

// percentileSorted returns the linear-interpolation percentile of a
// sorted slice. Same convention as PostgreSQL `percentile_cont`,
// so SQL-side and Go-side medians match exactly.
func percentileSorted(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return sorted[0]
	}
	pos := p * float64(n-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	return sorted[lo] + (pos-float64(lo))*(sorted[hi]-sorted[lo])
}

// fetchBaselineSamples pulls the channel-specific sample series for
// the window. Returns the raw values + the newest sample's
// timestamp (for the §4.1 14-day staleness check).
//
// Per-channel SQL:
//   - hr_awake: hourly_metrics.avg_val for heart_rate where
//     hour-of-day falls in the per-day awake window. We approximate
//     by counting all rows in the window — the awake-window
//     restriction is more precisely enforced by the §4.4 consumer
//     when it computes hour-level z-scores; the baseline here is
//     for spread estimation, not point comparison.
//   - hr_overnight: daily_scores.baseline_hr_overnight column (one
//     row per date).
//   - hrv / resp / temp: metric_points.qty filtered by metric_name
//     and quality='ok'.
func (s *DB) fetchBaselineSamples(
	channel BaselineChannel,
	from, until time.Time,
	loc *time.Location,
) ([]float64, time.Time, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	return s.fetchBaselineSamplesContext(ctx, channel, from, until, loc)
}

func (s *DB) fetchBaselineSamplesContext(
	ctx context.Context,
	channel BaselineChannel,
	from, until time.Time,
	loc *time.Location,
) ([]float64, time.Time, error) {

	sql, fromKey, untilKey := baselineSampleSource(channel, from, until, loc)
	if sql == "" {
		return nil, time.Time{}, nil
	}
	rows, err := s.pool.Query(ctx, sql, fromKey, untilKey)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()

	var values []float64
	var newest time.Time
	for rows.Next() {
		var v float64
		var dateRaw string
		if err := rows.Scan(&v, &dateRaw); err != nil {
			return nil, time.Time{}, err
		}
		if !isFiniteFloat(v) {
			continue
		}
		values = append(values, v)
		ts := parseBaselineSampleTS(dateRaw, loc)
		if !ts.IsZero() && ts.After(newest) {
			newest = ts
		}
	}
	return values, newest, rows.Err()
}

func baselineSampleSource(channel BaselineChannel, from, until time.Time, loc *time.Location) (string, string, string) {
	const dateLayout = "2006-01-02 15:04:05 -0700"
	fromStr := from.In(loc).Format(dateLayout)
	untilStr := until.In(loc).Format(dateLayout)
	fromDay := from.In(loc).Format("2006-01-02")
	untilDay := until.In(loc).Format("2006-01-02")

	switch channel {
	case ChannelHRAwake:
		// hourly_metrics.hour is TEXT "YYYY-MM-DD HH:00" — same
		// string range comparison works.
		return `
			SELECT avg_val, hour
			  FROM hourly_metrics
			 WHERE metric_name = 'heart_rate'
			   AND hour >= $1
			   AND hour <  $2`, fromStr[:13], untilStr[:13]
	case ChannelHROvernight:
		return `
			SELECT baseline_hr_overnight, date
			  FROM daily_scores
			 WHERE baseline_hr_overnight IS NOT NULL
			   AND date >= $1
			   AND date <  $2`, fromDay, untilDay
	case ChannelHRV:
		// Restrict to overnight samples per §4.1 caveat — Apple
		// reports HRV during Breathe sessions / post-exercise /
		// nocturnal, and the nocturnal subset is the cleanest
		// baseline. Heuristic: samples whose hour-of-day falls
		// between 23:00 and 09:00. Whole-night median for users
		// without standardised Breathe routine.
		return `
			SELECT qty, date
			  FROM metric_points
			 WHERE metric_name = 'heart_rate_variability'
			   AND quality = 'ok'
			   AND date >= $1
			   AND date <  $2
			   AND (SUBSTRING(date, 12, 2) >= '23' OR SUBSTRING(date, 12, 2) < '09')`, fromStr, untilStr
	case ChannelResp:
		return `
			SELECT qty, date
			  FROM metric_points
			 WHERE metric_name = 'respiratory_rate'
			   AND quality = 'ok'
			   AND date >= $1
			   AND date <  $2`, fromStr, untilStr
	case ChannelTemp:
		return `
			SELECT qty, date
			  FROM metric_points
			 WHERE metric_name = 'wrist_temperature'
			   AND quality = 'ok'
			   AND date >= $1
			   AND date <  $2`, fromStr, untilStr
	default:
		return "", "", ""
	}
}

// parseBaselineSampleTS handles the three timestamp formats this
// codebase stores:
//
//   - "YYYY-MM-DD HH:MM:SS ±TZ" (metric_points.date)
//   - "YYYY-MM-DD HH:00"        (hourly_metrics.hour — no seconds, no TZ)
//   - "YYYY-MM-DD"              (daily_scores.date — date-only)
//
// Returns the zero Time on parse failure — callers treat it as "no
// timestamp known" and skip the staleness check.
func parseBaselineSampleTS(s string, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04",
		"2006-01-02",
	} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t
		}
	}
	return time.Time{}
}
