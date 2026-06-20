package storage

import (
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/health"
)

// rawMetricsFromDailyScores reads 30 days of pre-aggregated metrics from the
// daily_scores cache in a single query. Returns nil if the cache is empty or
// has no usable rows (cold start).
func (s *DB) rawMetricsFromDailyScores(lastDate string) *health.RawMetrics {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT date, hrv_avg, rhr_avg, sleep_total, sleep_deep, sleep_rem,
		       sleep_core, sleep_awake, steps, calories, exercise_min,
		       spo2_avg, vo2_avg, resp_avg, baseline_hr_overnight
		FROM daily_scores
		WHERE date >= $1 AND date <= $2
		  AND (hrv_avg IS NOT NULL OR sleep_total IS NOT NULL OR steps IS NOT NULL)
		ORDER BY date DESC
		LIMIT 30`,
		subtractDays(lastDate, 29), lastDate)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var all []dailyScoreRow
	for rows.Next() {
		var r dailyScoreRow
		if err := rows.Scan(
			&r.date, &r.hrv, &r.rhr, &r.slp, &r.deep, &r.rem,
			&r.core, &r.awake, &r.steps, &r.cal, &r.ex,
			&r.spo2, &r.vo2, &r.resp, &r.nightHR,
		); err == nil {
			all = append(all, r)
		}
	}
	if len(all) == 0 {
		return nil
	}

	// appendIfPositive only appends real positive values — NULL or zero days
	// are skipped so they don't dilute averages used in scoring.
	appendIfPositive := func(dst *[]float64, p *float64) {
		if p != nil && *p > 0 {
			*dst = append(*dst, *p)
		}
	}

	// For the most recent day, daily_scores may be stale (backfill hasn't run
	// yet after a sync). Read fresh values from metric_points directly — they
	// are always up-to-date (INSERT writes there immediately).
	freshToday := s.freshDayFromRaw(lastDate)

	d := &health.RawMetrics{LastDate: lastDate}
	for i, r := range all {
		isLatest := i == 0
		if isLatest && freshToday != nil {
			// Override stale daily_scores with fresh hourly data for today.
			appendIfPositive(&d.HRV, coalesce(freshToday.hrv, r.hrv))
			appendIfPositive(&d.RHR, coalesce(freshToday.rhr, r.rhr))
			appendIfPositive(&d.Sleep, coalesce(freshToday.slp, r.slp))
			appendIfPositive(&d.Deep, coalesce(freshToday.deep, r.deep))
			appendIfPositive(&d.REM, coalesce(freshToday.rem, r.rem))
			appendIfPositive(&d.Awake, coalesce(freshToday.awake, r.awake))
			appendIfPositive(&d.Steps, coalesce(freshToday.steps, r.steps))
			appendIfPositive(&d.Cal, coalesce(freshToday.cal, r.cal))
			appendIfPositive(&d.Exercise, coalesce(freshToday.ex, r.ex))
			appendIfPositive(&d.SpO2, coalesce(freshToday.spo2, r.spo2))
			appendIfPositive(&d.VO2, coalesce(freshToday.vo2, r.vo2))
			appendIfPositive(&d.Resp, coalesce(freshToday.resp, r.resp))
		} else {
			appendIfPositive(&d.HRV, r.hrv)
			appendIfPositive(&d.RHR, r.rhr)
			appendIfPositive(&d.Sleep, r.slp)
			appendIfPositive(&d.Deep, r.deep)
			appendIfPositive(&d.REM, r.rem)
			appendIfPositive(&d.Awake, r.awake)
			appendIfPositive(&d.Steps, r.steps)
			appendIfPositive(&d.Cal, r.cal)
			appendIfPositive(&d.Exercise, r.ex)
			appendIfPositive(&d.SpO2, r.spo2)
			appendIfPositive(&d.VO2, r.vo2)
			appendIfPositive(&d.Resp, r.resp)
		}
	}

	// StepsWithDates and HRVWithDates — last 7 rows with actual data.
	for _, r := range all {
		if len(d.StepsWithDates) >= 7 {
			break
		}
		if r.steps != nil && *r.steps > 0 {
			d.StepsWithDates = append(d.StepsWithDates, health.DatedValue{Date: r.date, Val: *r.steps})
		}
		if r.hrv != nil && *r.hrv > 0 {
			d.HRVWithDates = append(d.HRVWithDates, health.DatedValue{Date: r.date, Val: *r.hrv})
		}
	}

	// night_sleep_total / nap_total live in metric_points only (no
	// daily_scores column yet), so the cached path needs a separate
	// fetch. Empty slices when iOS hasn't synced new-format data —
	// callers fall back to d.Sleep.
	d.NightSleep = s.metricPointDailySums("night_sleep_total", lastDate, 30)
	d.Nap = s.metricPointDailySums("nap_total", lastDate, 30)
	// NapToday is point-in-time (today only) so the dashboard badge
	// can't latch onto a prior day's nap. Slice filters qty>0 → its
	// index 0 is the latest day someone napped, not necessarily today.
	d.NapToday = s.metricPointDailyPoint("nap_total", lastDate)
	d.ReadinessEvidence = buildReadinessEvidence(lastDate, all[0], freshToday)

	return d
}

type dailyScoreRow struct {
	date                                  string
	hrv, rhr, slp, deep, rem, core, awake *float64
	steps, cal, ex, spo2, vo2, resp       *float64
	nightHR                               *float64
}

func buildReadinessEvidence(date string, latest dailyScoreRow, fresh *dayRow) *health.ReadinessEvidenceInput {
	pick := func(freshVal, cachedVal *float64) (*float64, string) {
		if freshVal != nil {
			return freshVal, date
		}
		if cachedVal != nil && latest.date == date {
			return cachedVal, latest.date
		}
		return nil, ""
	}
	evidence := &health.ReadinessEvidenceInput{Date: date}
	hrv, hrvDate := pick(nil, latest.hrv)
	if fresh != nil {
		hrv, hrvDate = pick(fresh.hrv, latest.hrv)
	}
	rhr, rhrDate := pick(nil, latest.rhr)
	if fresh != nil {
		rhr, rhrDate = pick(fresh.rhr, latest.rhr)
	}
	slp, slpDate := pick(nil, latest.slp)
	deep, _ := pick(nil, latest.deep)
	awake, _ := pick(nil, latest.awake)
	resp, respDate := pick(nil, latest.resp)
	if fresh != nil {
		slp, slpDate = pick(fresh.slp, latest.slp)
		deep, _ = pick(fresh.deep, latest.deep)
		awake, _ = pick(fresh.awake, latest.awake)
		resp, respDate = pick(fresh.resp, latest.resp)
	}
	evidence.HRV = readinessComponent("heart_rate_variability", date, hrvDate, hrv, sampleCount(fresh, "hrv"), "")
	if evidence.HRV.Present {
		if evidence.HRV.SampleCount >= health.MinSleepWindowHRVSamplesForFullConfidence {
			evidence.HRV.Confidence = health.ReadinessConfidenceFinal
		} else if evidence.HRV.SampleCount >= health.MinUnalignedHRVSamplesForProvisionalUse {
			evidence.HRV.Confidence = health.ReadinessConfidenceProvisional
		} else {
			evidence.HRV.Confidence = health.ReadinessConfidenceProvisional
		}
	}
	evidence.RHR = readinessComponent("resting_heart_rate", date, rhrDate, rhr, sampleCount(fresh, "rhr"), "")
	evidence.OvernightHR = readinessComponent("baseline_hr_overnight", date, latest.date, latest.nightHR, 0, "")
	evidence.SleepDuration = readinessComponent("sleep_total", date, slpDate, slp, 0, "")
	evidence.Respiratory = readinessComponent("respiratory_rate", date, respDate, resp, sampleCount(fresh, "resp"), "")
	evidence.SleepQuality = sleepQualityEvidence(date, slpDate, slp, deep, awake)
	return evidence
}

func readinessComponent(metric, evaluatedDate, sourceDate string, value *float64, samples int, missingReason string) health.ReadinessComponentEvidence {
	c := health.ReadinessComponentEvidence{
		Metric:        metric,
		EvaluatedDate: evaluatedDate,
		SourceDate:    sourceDate,
		Value:         value,
		SampleCount:   samples,
		Confidence:    health.ReadinessConfidenceFinal,
	}
	if value == nil || sourceDate == "" {
		c.Freshness = health.ReadinessFreshnessMissing
		c.MissingReason = missingReason
		if c.MissingReason == "" {
			c.MissingReason = "missing_same_day_value"
		}
		return c
	}
	c.Present = true
	if sourceDate != evaluatedDate {
		c.Freshness = health.ReadinessFreshnessStale
		c.Confidence = health.ReadinessConfidenceProvisional
	} else {
		c.Freshness = health.ReadinessFreshnessOK
	}
	return c
}

func sleepQualityEvidence(date, sourceDate string, sleep, deep, awake *float64) health.ReadinessComponentEvidence {
	c := readinessComponent("sleep_quality", date, sourceDate, nil, 0, "")
	if sleep == nil || *sleep <= 0 {
		return c
	}
	if deep == nil || awake == nil {
		c.MissingReason = "missing_sleep_stage_details"
		return c
	}
	deepPct := 0.0
	if deep != nil {
		deepPct = *deep / *sleep * 100
	}
	awakePct := 0.0
	if awake != nil {
		awakePct = *awake / *sleep * 100
	}
	value := deepPct
	c.Value = &value
	c.Present = true
	c.Freshness = health.ReadinessFreshnessOK
	c.Confidence = health.ReadinessConfidenceFinal
	if deepPct < 8 || awakePct > 10 {
		c.Confidence = health.ReadinessConfidenceLow
	}
	return c
}

func sampleCount(r *dayRow, metric string) int {
	if r == nil {
		return 0
	}
	switch metric {
	case "hrv":
		return r.hrvN
	case "rhr":
		return r.rhrN
	case "resp":
		return r.respN
	case "spo2":
		return r.spo2N
	default:
		return 0
	}
}

// metricPointDailyPoint returns the per-source-MAX daily SUM for a single
// metric on a specific date (`date` = YYYY-MM-DD). Returns 0 when there is
// no row — callers use this for "today only" badges where falling back to
// the most-recent prior day would be wrong (e.g. the nap badge on the
// dashboard sleep card).
func (s *DB) metricPointDailyPoint(metric, date string) float64 {
	ctx, cancel := queryCtx()
	defer cancel()
	var v *float64
	err := s.pool.QueryRow(ctx, `
		SELECT MAX(source_sum)
		FROM (
			SELECT source, SUM(qty) AS source_sum
			FROM metric_points
			WHERE metric_name = $1 AND SUBSTRING(date,1,10) = $2 AND qty > 0 AND quality = 'ok'
			GROUP BY source
		) sub`,
		metric, date).Scan(&v)
	if err != nil || v == nil {
		return 0
	}
	return *v
}

// metricPointDailySums returns up to `days` per-day SUM totals (most-recent
// first) for a single metric, read directly from metric_points with the
// standard preferred-source aggregation (MAX of per-source SUM). Used for
// metrics that are not yet cached in daily_scores — currently
// night_sleep_total and nap_total.
func (s *DB) metricPointDailySums(metric, lastDate string, days int) []float64 {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT MAX(source_sum)
		FROM (
			SELECT SUBSTRING(date,1,10) AS d, source, SUM(qty) AS source_sum
			FROM metric_points
			WHERE metric_name = $1 AND SUBSTRING(date,1,10) >= $2 AND qty > 0 AND quality = 'ok'
			GROUP BY d, source
		) sub
		GROUP BY d
		ORDER BY d DESC
		LIMIT $3`,
		metric, subtractDays(lastDate, days), days)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []float64
	for rows.Next() {
		var v float64
		if err := rows.Scan(&v); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// rawMetricsFromPoints reads raw metric time-series from metric_points using
// per-metric queries. This is the fallback path when daily_scores cache is cold.
func (s *DB) rawMetricsFromPoints(lastDate string) *health.RawMetrics {
	ctx, cancel := queryCtx()
	defer cancel()

	getDailyValues := func(metric string, days int, agg string) []float64 {
		var err error
		var rows pgx.Rows
		if agg == "SUM" {
			sleepDedup := sleepDedupClause(metric)
			r, e := s.pool.Query(ctx, fmt.Sprintf(`
				SELECT MAX(source_sum)
				FROM (
					SELECT SUBSTRING(date,1,10) AS d, source, SUM(qty) AS source_sum
					FROM metric_points
					WHERE metric_name = $1 AND SUBSTRING(date,1,10) >= $2 AND qty > 0 AND quality = 'ok' %s
					GROUP BY d, source
				) sub
				GROUP BY d
				ORDER BY d DESC
				LIMIT $3`, sleepDedup),
				metric, subtractDays(lastDate, days), days)
			rows = r
			err = e
		} else {
			r, e := s.pool.Query(ctx, `
				SELECT `+agg+`(qty)
				FROM metric_points
				WHERE metric_name = $1 AND SUBSTRING(date,1,10) >= $2 AND qty > 0 AND quality = 'ok'
				GROUP BY SUBSTRING(date,1,10)
				ORDER BY SUBSTRING(date,1,10) DESC
				LIMIT $3`,
				metric, subtractDays(lastDate, days), days)
			rows = r
			err = e
		}
		if err != nil {
			return nil
		}
		defer rows.Close()
		var vals []float64
		for rows.Next() {
			var v float64
			if err := rows.Scan(&v); err == nil {
				vals = append(vals, v)
			}
		}
		return vals
	}

	getDailyWithDates := func(metric string, days int, agg string) []health.DatedValue {
		var err error
		var rows pgx.Rows
		if agg == "SUM" {
			sleepDedup := sleepDedupClause(metric)
			r, e := s.pool.Query(ctx, fmt.Sprintf(`
				SELECT d, MAX(source_sum)
				FROM (
					SELECT SUBSTRING(date,1,10) AS d, source, SUM(qty) AS source_sum
					FROM metric_points
					WHERE metric_name = $1 AND SUBSTRING(date,1,10) >= $2 AND qty > 0 AND quality = 'ok' %s
					GROUP BY d, source
				) sub
				GROUP BY d
				ORDER BY d DESC
				LIMIT $3`, sleepDedup),
				metric, subtractDays(lastDate, days), days)
			rows = r
			err = e
		} else {
			r, e := s.pool.Query(ctx, `
				SELECT SUBSTRING(date,1,10), `+agg+`(qty)
				FROM metric_points
				WHERE metric_name = $1 AND SUBSTRING(date,1,10) >= $2 AND qty > 0 AND quality = 'ok'
				GROUP BY SUBSTRING(date,1,10)
				ORDER BY SUBSTRING(date,1,10) DESC
				LIMIT $3`,
				metric, subtractDays(lastDate, days), days)
			rows = r
			err = e
		}
		if err != nil {
			return nil
		}
		defer rows.Close()
		var out []health.DatedValue
		for rows.Next() {
			var dv health.DatedValue
			if err := rows.Scan(&dv.Date, &dv.Val); err == nil {
				out = append(out, dv)
			}
		}
		return out
	}

	out := &health.RawMetrics{
		LastDate: lastDate,
		HRV:      getDailyValues("heart_rate_variability", 30, "AVG"),
		RHR:      getDailyValues("resting_heart_rate", 30, "AVG"),
		Sleep:    getDailyValues("sleep_total", 30, "SUM"),
		Deep:     getDailyValues("sleep_deep", 30, "SUM"),
		REM:      getDailyValues("sleep_rem", 30, "SUM"),
		Awake:    getDailyValues("sleep_awake", 30, "SUM"),
		// New-format split written by health-sync iOS — see RawMetrics doc.
		NightSleep:     getDailyValues("night_sleep_total", 30, "SUM"),
		Nap:            getDailyValues("nap_total", 30, "SUM"),
		NapToday:       s.metricPointDailyPoint("nap_total", lastDate),
		Steps:          getDailyValues("step_count", 30, "SUM"),
		Cal:            getDailyValues("active_energy", 30, "SUM"),
		Exercise:       getDailyValues("apple_exercise_time", 30, "SUM"),
		SpO2:           getDailyValues("blood_oxygen_saturation", 30, "AVG"),
		VO2:            getDailyValues("vo2_max", 30, "AVG"),
		Resp:           getDailyValues("respiratory_rate", 30, "AVG"),
		WristTemp:      getDailyValues("wrist_temperature", 30, "AVG"),
		StepsWithDates: getDailyWithDates("step_count", 7, "SUM"),
		HRVWithDates:   getDailyWithDates("heart_rate_variability", 7, "AVG"),
	}
	out.ReadinessEvidence = buildReadinessEvidence(lastDate, dailyScoreRow{date: lastDate}, s.freshDayFromRaw(lastDate))
	return out
}

// intradayPartialSum returns today's accumulated total for a SUM metric
// (steps, active_energy) read from hourly_metrics. Source dedup picks the
// PREFERRED source for the day — Apple Watch first, then iPhone, falling
// back to the source with the highest daily total when neither is present.
// This mirrors buildDailyMetricCol exactly, so the intraday numerator
// stays consistent with the chronic denominator computed via daily_scores.
// Returns 0 when no data is present yet (e.g. early morning before any sync).
func (s *DB) intradayPartialSum(date, metric string) float64 {
	ctx, cancel := queryCtx()
	defer cancel()
	var v *float64
	err := s.pool.QueryRow(ctx, `
		WITH source_totals AS (
			SELECT source, SUM(avg_val) AS source_total
			FROM hourly_metrics
			WHERE metric_name = $1
			  AND SUBSTRING(hour, 1, 10) = $2
			  AND avg_val > 0
			GROUP BY source
		)
		SELECT COALESCE(
			(SELECT source_total FROM source_totals
			  WHERE source LIKE '%Ultra%' OR source LIKE '%Apple Watch%'
			  ORDER BY source_total DESC LIMIT 1),
			(SELECT source_total FROM source_totals
			  WHERE source LIKE '%iPhone%'
			  ORDER BY source_total DESC LIMIT 1),
			(SELECT MAX(source_total) FROM source_totals)
		)`, metric, date).Scan(&v)
	if err != nil || v == nil {
		return 0
	}
	return *v
}

// chronicAvg returns the 28-day average daily total of a daily_scores column
// (e.g. "steps", "calories"), excluding today so the chronic baseline is not
// biased by an active morning. Used as ACWR denominator in Energy Bank
// (Gabbett 2016 acute:chronic workload ratio).
func (s *DB) chronicAvg(lastDate, col string) float64 {
	ctx, cancel := queryCtx()
	defer cancel()
	var v *float64
	q := fmt.Sprintf(`
		SELECT AVG(%s) FROM daily_scores
		WHERE SUBSTRING(date, 1, 10) >= $1
		  AND SUBSTRING(date, 1, 10) <= $2
		  AND %s IS NOT NULL AND %s > 0`,
		col, col, col)
	err := s.pool.QueryRow(ctx, q,
		subtractDays(lastDate, 28), subtractDays(lastDate, 1)).Scan(&v)
	if err != nil || v == nil {
		return 0
	}
	return *v
}

// GetHealthBriefing fetches raw metric time series from the DB and delegates
// all scoring and insight computation to the health package.
// lang selects the output language ("en", "ru", "sr").
func (s *DB) GetHealthBriefing(lang string) (*health.BriefingResponse, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	// Use hourly_metrics for lastDate — avoids full scan of metric_points.
	var lastDate *string
	if err := s.pool.QueryRow(ctx, `SELECT MAX(SUBSTRING(hour,1,10)) FROM hourly_metrics`).Scan(&lastDate); err != nil || lastDate == nil {
		return &health.BriefingResponse{Greeting: "Welcome! No health data yet."}, nil
	}

	// Try reading 30-day metric history from daily_scores (1 query).
	// Fall back to per-metric queries against metric_points if cache is cold.
	data := s.rawMetricsFromDailyScores(*lastDate)
	if data == nil {
		data = s.rawMetricsFromPoints(*lastDate)
	}

	// Supplement wrist_temperature (not in daily_scores) for anomaly detection.
	if len(data.WristTemp) == 0 {
		data.WristTemp = s.fetchDailyMetric("wrist_temperature", *lastDate, 30, "AVG")
	}

	// Intraday partial-day sums + chronic 28-day baselines for Energy Bank.
	// Source-deduplicated via MAX-per-hour across devices (same pattern as
	// SumMetrics dedup documented in SCORING.md).
	data.StepsToday = s.intradayPartialSum(*lastDate, "step_count")
	data.ActiveEnergyToday = s.intradayPartialSum(*lastDate, "active_energy")
	data.StepsChronic28d = s.chronicAvg(*lastDate, "steps")
	data.ActiveEnergyChronic28d = s.chronicAvg(*lastDate, "calories")

	// Sleep Regularity Index (Phillips & Czeisler 2017) over a 14-day rolling
	// window of minute-level sleep state. ComputeSRI returns ok=false when
	// the user has fewer than 7 calendar days of per-segment sleep data
	// (HAE midnight-summary nights cannot drive this — only iOS pushes can).
	// scoreSleep falls back to the legacy stddev display in that case.
	if sri, n, ok := s.ComputeSRI(14); ok {
		v := sri
		data.SleepRegularityIndex = &v
		data.SleepRegularityNights = n
	}

	resp := health.ComputeBriefing(*data, lang)

	// EnergyBank v2 half-cutover (PR #43): when a v2 snapshot exists
	// for `lastDate`, override the displayed bank/capacity/drain with
	// the v2 numbers but leave ActionVerdict + VerdictReason on the
	// v1 readiness-derived path. Reasoning: we have <1 week of v2
	// distribution data, so the v1 verdict thresholds [25, 45, 60]
	// can't be safely re-applied to the v2 [0, 100] scale yet; mixing
	// half-calibrated verdicts into AI/Telegram outputs would degrade
	// the recommendations. Components are dropped — they describe v1
	// inputs and would visibly contradict the new numbers in the
	// dashboard <details>. The v2 hourly chart (PR #42) provides the
	// new breakdown via `components` JSONB.
	//
	// Final cutover (verdict thresholds re-tuned on real distribution)
	// tracked in Todoist 6gc2R6692674v3Gf.
	//
	// `lastDate` (NOT today-in-TZ) is the lookup key, deliberately —
	// when HAE is mid-sync and today has no data yet, the briefing's
	// whole frame is yesterday, and the override should match that
	// frame. Looking up "today" instead would split frame and override
	// across day boundaries.
	if resp.EnergyBank != nil && lastDate != nil {
		ctxSnap, cancelSnap := queryCtx()
		snap, err := s.GetLatestEnergySnapshotForDate(ctxSnap, *lastDate)
		cancelSnap()
		if err != nil {
			// Don't break the briefing on a transient pool/scan error;
			// fall through to v1 numbers, but surface so an operator
			// can investigate sustained failures.
			log.Printf("[ENERGY_V2] briefing override read (date=%s): %v", *lastDate, err)
		}
		if err == nil && snap != nil {
			display := snap.Bank
			if display < 0 {
				display = 0
			}
			if display > 100 {
				display = 100
			}
			// Defensive drain floor: PR3 floors α and kcal so
			// DrainDelta is non-negative by construction, but a
			// future formula tweak or a manual settings override
			// could break that. Floor here so the drain badge can't
			// surface a nonsense "-5" and so capacity stays ≥ current
			// (bar-fill invariant).
			drain := snap.DrainDelta
			if drain < 0 {
				drain = 0
			}
			capacity := display + drain
			if capacity > 100 {
				capacity = 100
			}
			resp.EnergyBank.Current = display
			resp.EnergyBank.Capacity = capacity
			resp.EnergyBank.DrainSoFar = drain
			resp.EnergyBank.Components = nil
			// v2.2 stress flags: surface the §4.3 set (acute_stress,
			// sustained_load, calibration_warmup, stale_stress) plus the
			// v2.0 imputed_* set from the snapshot. computeBankFromDays
			// merges today's StressFlags into BankResult.Flags before the
			// snapshot is written, so snap.Flags is the authoritative
			// "today's flags" carrier — read it here instead of going
			// back to daily_scores.stress_flags.
			resp.EnergyBank.Flags = snap.Flags
			// v1→v2 verdict cutover (PR #47): recompute ActionVerdict
			// and VerdictReason against the v2 bank using personal
			// percentile bands instead of v1's hardcoded
			// readiness-scale thresholds. The v1 path was degenerate
			// in production — its current=readiness−drain calculation
			// almost always landed below the 25-cutoff, producing
			// "rest" on every day regardless of actual recovery state.
			//
			// Bands are derived from the tenant's own backfilled
			// energy_snapshots (≥30 non-imputed days) or fall back to
			// documented defaults below that threshold; see
			// ComputeUserVerdictBands. HRV gate (Plews 2014 SWC
			// thresholds) is preserved across the cutover — it's the
			// physiologically-grounded "your body says rest" override
			// that doesn't depend on bank scale.
			//
			// On bands-fetch error: log and fall through to defaults
			// rather than failing the briefing. Verdict produced from
			// defaults is degraded but not wrong; failing the whole
			// /api/dashboard call would be worse UX.
			bandsCtx, cancelBands := queryCtx()
			bands, bandsErr := s.ComputeUserVerdictBands(bandsCtx)
			cancelBands()
			if bandsErr != nil {
				log.Printf("[ENERGY_V2] compute verdict bands (date=%s): %v — falling back to defaults", *lastDate, bandsErr)
				bands = health.DefaultV2VerdictBands()
			}
			ls := health.GetStrings(lang)
			newVerdict := health.ChooseVerdictV2(resp.EnergyBank.HRVZRaw, display, bands)
			newReason := health.BuildVerdictReasonV2(newVerdict, display, resp.EnergyBank.HRVZRaw, ls)
			// §4.3 stress-flag overrides: illness_signature forces
			// rest, recovery_debt suppresses push_hard,
			// parasympathetic_rebound enriches reason text. Snapshot
			// flags are the authoritative carrier — populated by
			// ComputeBankForDate from daily_scores.stress_flags so
			// the same data drives the bank, the verdict, and the AI
			// prompt.
			newVerdict, newReason, _ = health.ApplyStressFlagVerdictOverride(
				newVerdict, newReason, snap.Flags, ls,
			)
			resp.EnergyBank.ActionVerdict = newVerdict
			resp.EnergyBank.VerdictReason = newReason
		}
	}

	// Persist EnergyBank EOD snapshot AFTER the v2 override and illness
	// safety cap apply.
	// Persisting the v1 numbers here would lock guaranteed-wrong values
	// (saturated capacity on typical days, no multi-day carryover —
	// exactly the bug v2 was built to fix) into daily_scores, where
	// the legacy 14d sparkline reads them. Persisting the overridden
	// values means the sparkline starts showing v2 from the cutover
	// day forward; existing pre-cutover rows decay out of the window
	// over ~14 days and the chart becomes fully v2. The visible
	// discontinuity on cutover day is intentional — it's the cutover.
	//
	// Briefing is the single entry point through which the bank gets
	// recomputed (no scheduled EOD job exists), so each call rewrites
	// the row for *lastDate. The last call before midnight effectively
	// becomes the EOD snapshot — once the day rolls over no further
	// computes target this date and the row freezes. Best-effort:
	// errors logged inside the helper.
	// Subjective check-in (Telegram one-tap). Only populated when a
	// row exists for the briefing date — nil otherwise. GetTodayCheckin
	// returns (nil, nil) on no-row, so the dashboard simply doesn't
	// render the confirmation line until the user answers.
	if row, cerr := s.GetTodayCheckin(*lastDate, CheckinSourceTelegram); cerr == nil && row != nil {
		resp.SubjectiveCheckin = &health.SubjectiveCheckinSummary{
			Status: row.Status,
			Answer: row.Answer,
		}
	}

	illnessInput := s.BuildIllnessEvidenceInput(*lastDate, resp.SubjectiveCheckin)
	resp.IllnessSuspicion = health.ComputeIllnessSuspicion(illnessInput)
	health.ApplyIllnessSafetyCap(resp, health.GetStrings(lang))

	if IsContextCaveatsEnabled(s) {
		if annotations, aerr := s.GetContextAnnotationsForDate(*lastDate); aerr == nil {
			for _, a := range annotations {
				resp.ContextAnnotations = append(resp.ContextAnnotations, health.ContextAnnotationSummary{
					Date:           a.Date,
					DetectedReason: a.DetectedReason,
					Category:       a.Category,
					SleepHours:     a.SleepHours,
					BaselineAvg:    a.BaselineAvg,
					ZScore:         a.ZScore,
				})
			}
		} else {
			log.Printf("context annotations: %v", aerr)
		}
	}

	if resp.EnergyBank != nil {
		go s.SaveEnergyBankSnapshot(*lastDate, resp.EnergyBank)
	}

	// Attach per-source sleep breakdown for the most recent night.
	// Query hourly_metrics (indexed by hour) instead of metric_points.
	if resp.Sleep != nil {
		sleepRows, qErr := s.pool.Query(ctx, `
			SELECT source,
				SUM(CASE WHEN metric_name='sleep_total'       THEN avg_val ELSE 0 END),
				SUM(CASE WHEN metric_name='sleep_deep'        THEN avg_val ELSE 0 END),
				SUM(CASE WHEN metric_name='sleep_rem'         THEN avg_val ELSE 0 END),
				SUM(CASE WHEN metric_name='sleep_core'        THEN avg_val ELSE 0 END),
				SUM(CASE WHEN metric_name='sleep_unspecified' THEN avg_val ELSE 0 END),
				SUM(CASE WHEN metric_name='sleep_awake'       THEN avg_val ELSE 0 END)
			FROM hourly_metrics
			WHERE metric_name IN ('sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_unspecified','sleep_awake')
			  AND SUBSTRING(hour,1,10) = $1
			GROUP BY source
			ORDER BY SUM(CASE WHEN metric_name='sleep_total' THEN avg_val ELSE 0 END) DESC`,
			*lastDate)
		if qErr == nil {
			defer sleepRows.Close()
			for sleepRows.Next() {
				var ss health.SleepSourceSummary
				if sErr := sleepRows.Scan(&ss.Source, &ss.Total, &ss.Deep, &ss.REM, &ss.Core, &ss.Unspecified, &ss.Awake); sErr == nil && ss.Total > 0 {
					resp.Sleep.Sources = append(resp.Sleep.Sources, ss)
				}
			}
		}
	}

	// Surface server-localized labels for the enum fields iOS / other
	// non-template consumers would otherwise mirror in their own i18n
	// tables (issue #83). Called last so labels reflect the final
	// values — section statuses are settled by ComputeBriefing, verdict
	// + flags by the v2 override block above.
	health.EnrichLabels(resp, health.GetStrings(lang))

	return resp, nil
}

// GetReadinessHistory returns readiness scores for the last `outputDays` days.
// Results are served from the daily_scores cache when available and fresh;
// otherwise the full sliding-window computation runs and the cache is updated.
func (s *DB) GetReadinessHistory(outputDays int) ([]health.ReadinessPoint, error) {
	cached, err := s.readinessFromCache(outputDays)
	if err == nil && isCacheRecent(cached) {
		fillReadinessBands(cached)
		return cached, nil
	}
	pts, err := s.computeReadinessHistory(outputDays)
	if err != nil {
		return nil, err
	}
	go s.saveReadinessScores(pts)
	fillReadinessBands(pts)
	return pts, nil
}

// fillReadinessBands populates the Band field for every point using
// the same threshold logic the briefing path uses. Cheap (one switch
// per point) and means /api/readiness-history carries the same
// canonical band as /api/health-briefing, so clients (iOS especially)
// don't have to maintain a parallel band-from-score mapping for the
// sparkline path. See issue #83 item #4.
func fillReadinessBands(pts []health.ReadinessPoint) {
	for i := range pts {
		pts[i].Band = health.ReadinessBand(pts[i].Score)
	}
}

// computeReadinessHistory is the raw sliding-window computation (no caching).
// For each output day D it uses HRV/RHR/sleep data from D-29..D
// (most-recent-first) and calls health.ComputeReadinessScore.
func (s *DB) computeReadinessHistory(outputDays int) ([]health.ReadinessPoint, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	window := 30
	total := outputDays + window

	// Determine the latest date from data (not server time) to avoid TZ mismatch.
	var lastDate *string
	s.pool.QueryRow(ctx, `SELECT SUBSTRING(date,1,10) FROM metric_points ORDER BY SUBSTRING(date,1,10) DESC LIMIT 1`).Scan(&lastDate)
	if lastDate == nil {
		return nil, fmt.Errorf("no metric data found")
	}
	fromDate := subtractDays(*lastDate, total)

	// Fetch date-keyed maps for the full look-back period.
	fetch := func(metric, agg string, isSleep bool) (map[string]float64, error) {
		var pgxRows pgx.Rows
		var err error
		if isSleep {
			// Pick best source per day via shared helper (Apple Watch > RingConn
			// with MIN > 1h-gated cross-validation).
			r, e := s.pool.Query(ctx, `
				SELECT d, `+sleepCrossValidationPickExpr("source_sum")+` AS val
				FROM (
				    SELECT SUBSTRING(date,1,10) AS d, source, SUM(qty) AS source_sum
				    FROM metric_points
				    WHERE metric_name = $1
				      AND qty > 0
				      AND quality = 'ok'
				      AND SUBSTRING(date,1,10) >= $2
				    GROUP BY SUBSTRING(date,1,10), source
				) sub
				GROUP BY d`,
				metric, fromDate)
			pgxRows = r
			err = e
		} else {
			r, e := s.pool.Query(ctx, `
				SELECT SUBSTRING(date,1,10), `+agg+`(qty)
				FROM metric_points
				WHERE metric_name = $1
				  AND qty > 0
				  AND quality = 'ok'
				  AND SUBSTRING(date,1,10) >= $2
				GROUP BY SUBSTRING(date,1,10)`,
				metric, fromDate)
			pgxRows = r
			err = e
		}
		if err != nil {
			return nil, err
		}
		defer pgxRows.Close()
		m := make(map[string]float64)
		for pgxRows.Next() {
			var d string
			var v float64
			if err := pgxRows.Scan(&d, &v); err == nil {
				m[d] = v
			}
		}
		return m, nil
	}

	hrvMap, err := fetch("heart_rate_variability", "AVG", false)
	if err != nil {
		return nil, err
	}
	rhrMap, err := fetch("resting_heart_rate", "AVG", false)
	if err != nil {
		return nil, err
	}
	sleepMap, err := fetch("sleep_total", "SUM", true)
	if err != nil {
		return nil, err
	}

	// Build a sorted list of all days we have any data for.
	dateSet := make(map[string]bool)
	for d := range hrvMap {
		dateSet[d] = true
	}
	for d := range rhrMap {
		dateSet[d] = true
	}
	for d := range sleepMap {
		dateSet[d] = true
	}
	allDates := make([]string, 0, len(dateSet))
	for d := range dateSet {
		allDates = append(allDates, d)
	}
	sort.Strings(allDates)

	// For each output day (last outputDays dates) compute score using 30-day window.
	if len(allDates) > outputDays {
		allDates = allDates[len(allDates)-outputDays:]
	}

	// valsBefore returns values for all dates <= anchor, sorted by DATE descending
	// (most recent first) so that vals[:3] is the last 3 days, vals[3:] is the
	// historical baseline. Sorting by value (as before) was a bug: it put the
	// best HRV days first, artificially inflating the "recent" average.
	valsBefore := func(m map[string]float64, anchor string) []float64 {
		type dateval struct {
			d string
			v float64
		}
		var pairs []dateval
		for d, v := range m {
			if d <= anchor {
				pairs = append(pairs, dateval{d, v})
			}
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].d > pairs[j].d })
		if len(pairs) > window {
			pairs = pairs[:window]
		}
		out := make([]float64, len(pairs))
		for i, p := range pairs {
			out[i] = p.v
		}
		return out
	}

	out := make([]health.ReadinessPoint, 0, len(allDates))
	for _, d := range allDates {
		hrv := valsBefore(hrvMap, d)
		rhr := valsBefore(rhrMap, d)
		sleep := valsBefore(sleepMap, d)
		score := health.ComputeReadinessScore(hrv, rhr, sleep)
		out = append(out, health.ReadinessPoint{Date: d, Score: score})
	}
	return out, nil
}

// fetchDailyMetric reads a single metric's daily values from metric_points.
// Used for metrics not stored in daily_scores (e.g. wrist_temperature).
func (s *DB) fetchDailyMetric(metric, lastDate string, days int, agg string) []float64 {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT `+agg+`(qty)
		FROM metric_points
		WHERE metric_name = $1 AND SUBSTRING(date,1,10) >= $2 AND qty > 0 AND quality = 'ok'
		GROUP BY SUBSTRING(date,1,10)
		ORDER BY SUBSTRING(date,1,10) DESC
		LIMIT $3`,
		metric, subtractDays(lastDate, days), days)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var vals []float64
	for rows.Next() {
		var v float64
		if err := rows.Scan(&v); err == nil {
			vals = append(vals, v)
		}
	}
	return vals
}

// dayRow mirrors the daily_scores column set, used for fresh-day override.
type dayRow struct {
	hrv, rhr, slp, deep, rem, core, awake *float64
	steps, cal, ex, spo2, vo2, resp       *float64
	hrvN, rhrN, respN, spo2N              int
}

// freshDayFromRaw reads today's values directly from metric_points (always
// up-to-date, unlike hourly_metrics which may be stale after cache invalidation).
// Uses smart combine for SUM metrics (pipe-source aware dedup).
func (s *DB) freshDayFromRaw(date string) *dayRow {
	ctx, cancel := queryCtx()
	defer cancel()
	type spec struct {
		metric string
		dest   **float64
		isSum  bool
		count  *int
	}
	r := &dayRow{}
	specs := []spec{
		{"heart_rate_variability", &r.hrv, false, &r.hrvN},
		{"resting_heart_rate", &r.rhr, false, &r.rhrN},
		{"sleep_total", &r.slp, true, nil},
		{"sleep_deep", &r.deep, true, nil},
		{"sleep_rem", &r.rem, true, nil},
		{"sleep_core", &r.core, true, nil},
		{"sleep_awake", &r.awake, true, nil},
		{"step_count", &r.steps, true, nil},
		{"active_energy", &r.cal, true, nil},
		{"apple_exercise_time", &r.ex, true, nil},
		{"blood_oxygen_saturation", &r.spo2, false, &r.spo2N},
		{"vo2_max", &r.vo2, false, nil},
		{"respiratory_rate", &r.resp, false, &r.respN},
	}
	anyFound := false
	for _, sp := range specs {
		var val float64
		var err error
		if sp.isSum {
			sleepDedup := sleepDedupClause(sp.metric)
			err = s.pool.QueryRow(ctx, fmt.Sprintf(`
				WITH source_totals AS (
					SELECT source, SUM(qty) AS source_total
					FROM metric_points
					WHERE metric_name=$1 AND SUBSTRING(date,1,10)=$2 AND qty > 0 AND quality = 'ok' %s
					GROUP BY source
				) `, sleepDedup)+preferredSourceForMetric(sp.metric), sp.metric, date).Scan(&val)
		} else {
			var n int
			err = s.pool.QueryRow(ctx, `
				SELECT COALESCE(AVG(qty), 0), COUNT(*)
				FROM metric_points
				WHERE metric_name=$1 AND SUBSTRING(date,1,10)=$2 AND qty > 0 AND quality = 'ok'`,
				sp.metric, date).Scan(&val, &n)
			if sp.count != nil {
				*sp.count = n
			}
		}
		if err == nil && val > 0 {
			v := val
			*sp.dest = &v
			anyFound = true
		}
	}
	if !anyFound {
		return nil
	}
	return r
}

// coalesce returns the first non-nil pointer, or nil if both are nil.
func coalesce(a, b *float64) *float64 {
	if a != nil {
		return a
	}
	return b
}

func subtractDays(dateStr string, days int) string {
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return dateStr
	}
	return t.AddDate(0, 0, -days).Format("2006-01-02")
}
