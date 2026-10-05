package storage

import (
	"context"
	"math"
	"sort"
	"time"

	"health-receiver/internal/health"
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

// appendMultiChannelStressFlags evaluates the §4.3 multi-channel
// flags (illness_signature / recovery_debt / parasympathetic_rebound)
// and appends them to `flags`. Returns the extended slice — caller
// merges into SustainedHRLoadResult.Flags before persisting.
//
// All three flags are independent of the HR coverage gate and the
// sustained_hr_load integral; they read autonomic channels (HRV,
// temp, resp, overnight RHR) that have their own §4.1 calibration
// state machines. A day with stale_stress for HR can still surface
// illness_signature when temp+resp+hrv all break baseline.
//
// `hourZ` is the per-hour HR z-series the caller already computed
// for sustained_hr_load; reused here to derive the daily HR-shift
// summary the parasympathetic_rebound predicate consumes — no second
// pass over hourly_metrics.
func (s *DB) appendMultiChannelStressFlags(
	date string,
	loc *time.Location,
	hourZ []float64,
	flags []string,
) []string {
	ctx, cancel := queryCtx()
	defer cancel()
	flags, _ = s.appendMultiChannelStressFlagsContext(ctx, date, loc, hourZ, flags)
	return flags
}

func (s *DB) appendMultiChannelStressFlagsContext(ctx context.Context, date string, loc *time.Location, hourZ []float64, flags []string) ([]string, error) {
	hrvDrop, hrvOK, err := s.channelDropContext(ctx, date, ChannelHRV, loc)
	if err != nil {
		return nil, err
	}
	tempZ, tempOK, err := s.channelShiftContext(ctx, date, ChannelTemp, loc)
	if err != nil {
		return nil, err
	}
	respZ, respOK, err := s.channelShiftContext(ctx, date, ChannelResp, loc)
	if err != nil {
		return nil, err
	}
	rhrShift, rhrOK, err := s.channelShiftContext(ctx, date, ChannelHROvernight, loc)
	if err != nil {
		return nil, err
	}
	dayHRShift, dayHROK := meanFiniteHourZ(hourZ)

	// illness_signature — all three channels deviating together.
	if hrvOK && tempOK && respOK &&
		health.IllnessSignature(tempZ, respZ, hrvDrop) {
		flags = append(flags, "illness_signature")
	}

	// recovery_debt — overnight HRV down + overnight RHR up.
	if hrvOK && rhrOK &&
		health.RecoveryDebt(hrvDrop, rhrShift) {
		flags = append(flags, "recovery_debt")
	}

	// parasympathetic_rebound — daily HR shift up + HRV above
	// baseline (drop < −1).
	if dayHROK && hrvOK &&
		health.ParasympatheticRebound(dayHRShift, hrvDrop) {
		flags = append(flags, "parasympathetic_rebound")
	}

	return flags, nil
}
