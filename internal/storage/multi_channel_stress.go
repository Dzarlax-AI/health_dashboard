package storage

import (
	"context"
	"math"
	"sort"
	"time"

	dbtmetrics "health-receiver/experiments/dbt_metrics"
)

// todayChannelMedian returns the median of `channel`'s samples taken
// on `date` (one 24h window in `loc`). Reuses fetchBaselineSamples so
// the per-channel SQL shapes (hourly_metrics.heart_rate.avg_val for
// hr_awake, daily_scores.baseline_hr_overnight for hr_overnight,
// metric_points.qty + overnight-hour filter for hrv, etc.) stay in one
// place and can't drift between the baseline window read and the
// "today" read.
//
// Returns ok=false when there are no samples on `date` or the DB call
// errored — both treated as "no signal", caller skips the flag.
func (s *DB) todayChannelMedian(
	date string,
	channel BaselineChannel,
	loc *time.Location,
) (float64, bool) {
	ctx, cancel := queryCtx()
	defer cancel()
	value, ok, _ := s.todayChannelMedianContext(ctx, date, channel, loc)
	return value, ok
}

func (s *DB) todayChannelMedianContext(ctx context.Context, date string, channel BaselineChannel, loc *time.Location) (float64, bool, error) {
	d, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return 0, false, err
	}
	from := d.In(loc)
	until := d.AddDate(0, 0, 1).In(loc)
	samples, _, err := s.fetchBaselineSamplesContext(ctx, channel, from, until, loc)
	if err != nil {
		return 0, false, err
	}
	if len(samples) == 0 {
		return 0, false, nil
	}
	sort.Float64s(samples)
	return percentileSorted(samples, 0.5), true, nil
}

// channelShift returns `(today − baseline) / sd`, the standard
// "positive means above baseline" z-shift used by temp/resp/rhr in
// §4.3 flag predicates. Returns (0, false) when any prerequisite
// failed — today missing, baseline not usable, sd≤0.
//
// Baselines are fetched fresh per channel; PR-9b accepts the extra DB
// round-trips because the multi-channel flag layer only runs once per
// date and the queries are small (indexed daily-window reads).
func (s *DB) channelShift(
	date string,
	channel BaselineChannel,
	loc *time.Location,
) (float64, bool) {
	ctx, cancel := queryCtx()
	defer cancel()
	value, ok, _ := s.channelShiftContext(ctx, date, channel, loc)
	return value, ok
}

func (s *DB) channelShiftContext(ctx context.Context, date string, channel BaselineChannel, loc *time.Location) (float64, bool, error) {
	bl, ok, err := s.PersonalBaselineContext(ctx, date, channel, 30, loc)
	if err != nil {
		return 0, false, err
	}
	if !ok {
		return 0, false, nil
	}
	today, ok, err := s.todayChannelMedianContext(ctx, date, channel, loc)
	if err != nil {
		return 0, false, err
	}
	if !ok {
		return 0, false, nil
	}
	if bl.MADSD <= 0 {
		return 0, false, nil
	}
	return (today - bl.Median) / bl.MADSD, true, nil
}

// channelDrop returns `(baseline − today) / sd`, the inverted-sign
// convention used by `hrv_drop` in §4.3 ("positive means today below
// baseline" — the illness / fatigue direction). Inverse of
// channelShift; kept as a separate helper so callers spell out which
// physiological direction they care about.
func (s *DB) channelDrop(
	date string,
	channel BaselineChannel,
	loc *time.Location,
) (float64, bool) {
	ctx, cancel := queryCtx()
	defer cancel()
	v, ok, _ := s.channelDropContext(ctx, date, channel, loc)
	return v, ok
}

func (s *DB) channelDropContext(ctx context.Context, date string, channel BaselineChannel, loc *time.Location) (float64, bool, error) {
	v, ok, err := s.channelShiftContext(ctx, date, channel, loc)
	if err != nil {
		return 0, false, err
	}
	if !ok {
		return 0, false, nil
	}
	return -v, true, nil
}

// meanFiniteHourZ returns the mean of a per-hour z-slice, skipping
// NaN/±Inf entries. Used as the daily `hr_shift` summary in §4.2 —
// "mean over the awake window of per-hour z-shifts" — fed into
// ParasympatheticRebound. Returns (0, false) when no finite hour is
// present (whole day was a coverage gap).
func meanFiniteHourZ(hourZ []float64) (float64, bool) {
	var sum float64
	var n int
	for _, z := range hourZ {
		if math.IsNaN(z) || math.IsInf(z, 0) {
			continue
		}
		sum += z
		n++
	}
	if n == 0 {
		return 0, false
	}
	return sum / float64(n), true
}

// applyStressSignals evaluates the shared daily load and flag predicates after
// resolving each channel's own calibration and coverage. A missing HR series
// still allows the independent overnight signals to fire.
func (s *DB) applyStressSignals(ctx context.Context, date string, loc *time.Location, hourZ []float64, threshold float64, result *SustainedHRLoadResult) error {
	hrvDrop, hrvOK, err := s.channelDropContext(ctx, date, ChannelHRV, loc)
	if err != nil {
		return err
	}
	tempZ, tempOK, err := s.channelShiftContext(ctx, date, ChannelTemp, loc)
	if err != nil {
		return err
	}
	respZ, respOK, err := s.channelShiftContext(ctx, date, ChannelResp, loc)
	if err != nil {
		return err
	}
	rhrShift, rhrOK, err := s.channelShiftContext(ctx, date, ChannelHROvernight, loc)
	if err != nil {
		return err
	}
	dayHRShift, dayHROK := meanFiniteHourZ(hourZ)

	inputs := &stressSignalInputs{hourZ: hourZ, threshold: threshold, temp: availableStressShift(tempZ, tempOK),
		resp: availableStressShift(respZ, respOK), drop: availableStressShift(hrvDrop, hrvOK),
		rhr: availableStressShift(rhrShift, rhrOK), dayHR: availableStressShift(dayHRShift, dayHROK)}
	var signals []string
	err = s.pool.QueryRow(ctx, dbtmetrics.BriefingStressSignalsSQL(), inputs.hourZ, inputs.threshold,
		inputs.temp, inputs.resp, inputs.drop, inputs.rhr, inputs.dayHR).Scan(&result.SustainedHRLoadZ, &signals)
	if err != nil {
		return err
	}
	result.signalInputs = inputs
	result.Flags = append(result.Flags, signals...)
	return nil
}

func availableStressShift(value float64, available bool) *float64 {
	if !available {
		return nil
	}
	return &value
}
