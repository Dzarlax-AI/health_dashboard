// Chip auto-calibration writer — Phase 2 §6.1.
//
// For each binary-chip target (acute_risk/event_t1_t3 and
// chronic_load/chronic_label) the writer pulls the last
// `ChipCalibrationWindowDays` eligible (predicted_value, label) pairs
// inside the candidate date's current source_epoch and derives a
// cutoff via the percentile-p80 method:
//
//   cutoff = max(p80, base_rate)
//
// `p80` is the 80th percentile of `naive_baselines.predicted_value`
// over the window; `base_rate` is the observed positive rate. The
// max-guard is a safety net for the degenerate case where the
// predicted distribution is bottom-heavy (p80 < base_rate) — without
// it the chip would mark more than the base-rate's share of days
// elevated, which contradicts the operational policy ("top 20% of
// your personal history"). In practice on the `health` tenant both
// targets currently have p80 well above base_rate; the guard is
// belt-and-suspenders against future distribution shifts and other
// tenants.
//
// Guards (richer status enum so an operator can tell which one
// fired):
//
//   - n_eligible < `ChipCalibrationMinEligible`  -> insufficient_eligible
//   - n_positives < `ChipCalibrationMinPositives` -> insufficient_positives
//   - epoch resolution fails for today           -> no_current_epoch
//
// The chip read path treats anything other than `active` as
// `calibrating` (UI surface decision in §6.1).

package storage

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// ChipCalibrationWindowDays — rolling window the calibration writer
// looks at for percentile/base-rate computation. 180 days matches
// plan §6.1: enough to be stable against day-to-day noise while not
// dragging in seasonal regimes from > 6 months ago.
const ChipCalibrationWindowDays = 180

// ChipCalibrationMinEligible — minimum eligible (predicted_value,
// label) pairs in the window before the writer trusts the
// distribution. Below this the row stays `insufficient_eligible`
// and the chip stays `calibrating`.
const ChipCalibrationMinEligible = 90

// ChipCalibrationMinPositives — minimum count of positive labels
// (target_value >= 0.5) needed inside the window. Without enough
// positives the base-rate floor guard is too noisy to trust.
const ChipCalibrationMinPositives = 10

// chipCalibrationTargets — the (sub_score, target_kind) pairs the
// writer recomputes. `chronic_load / chronic_acute_density` is
// intentionally absent: it stays silent per §6.1 (Phase 1 closed
// the label below floor with non-overlapping CIs).
var chipCalibrationTargets = []struct {
	SubScore, TargetKind string
}{
	{SubScoreAcuteRisk, TargetKindEventT1T3},
	{SubScoreChronicLoad, TargetKindChronicLabel},
}

// ChipCalibrationRecomputeResult is the per-target outcome of one
// RecomputeChipCalibrations pass. Returned to the admin endpoint so
// the operator sees exactly what landed in chip_calibrations without
// re-querying the table.
type ChipCalibrationRecomputeResult struct {
	SubScore   string           `json:"sub_score"`
	TargetKind string           `json:"target_kind"`
	Saved      *ChipCalibration `json:"saved,omitempty"`
	Error      string           `json:"error,omitempty"`
}

// RecomputeChipCalibrations runs the calibration pass for every
// chip target on this DB's tenant. Always writes one row per target
// (even on insufficient-data outcomes) so a single Load query later
// can tell `calibrating` apart from `never computed`.
//
// `asOfDate` is the tenant-local YYYY-MM-DD date the calibration
// anchors on. The caller resolves it from the tenant's REPORT_TZ
// (`settings.timezone` → env `REPORT_TZ` → UTC) so date arithmetic
// here uses the same calendar the writers and the operational-contract
// preview use. Passing UTC's current date for a non-UTC tenant near
// local midnight is the exact class of bug fixed in #109 for the
// preview surface; we avoid replaying it here by taking the date as
// an explicit argument.
func (s *DB) RecomputeChipCalibrations(asOfDate string) ([]ChipCalibrationRecomputeResult, error) {
	asOf, err := time.Parse(isoDate, asOfDate)
	if err != nil {
		return nil, fmt.Errorf("RecomputeChipCalibrations: parse asOfDate %q: %w", asOfDate, err)
	}
	epoch, err := s.ResolveSourceEpoch(asOfDate)
	if err != nil {
		return nil, fmt.Errorf("RecomputeChipCalibrations: resolve epoch for %s: %w", asOfDate, err)
	}
	out := make([]ChipCalibrationRecomputeResult, 0, len(chipCalibrationTargets))
	for _, target := range chipCalibrationTargets {
		res := ChipCalibrationRecomputeResult{SubScore: target.SubScore, TargetKind: target.TargetKind}
		cal, recomputeErr := s.recomputeOneChipCalibration(target.SubScore, target.TargetKind, epoch, asOf)
		if recomputeErr != nil {
			res.Error = recomputeErr.Error()
			out = append(out, res)
			continue
		}
		if saveErr := s.SaveChipCalibration(*cal); saveErr != nil {
			res.Error = "save: " + saveErr.Error()
			out = append(out, res)
			continue
		}
		// Re-read so callers see the canonical persisted form (in
		// particular `computed_at` populated by DB NOW()).
		stored, loadErr := s.LoadChipCalibration(target.SubScore, target.TargetKind, epoch)
		if loadErr != nil {
			res.Error = "reload: " + loadErr.Error()
			out = append(out, res)
			continue
		}
		res.Saved = stored
		out = append(out, res)
	}
	return out, nil
}

func (s *DB) recomputeOneChipCalibration(subScore, targetKind, epoch string, asOf time.Time) (*ChipCalibration, error) {
	if epoch == "" || epoch == SentinelSourceEpoch {
		return &ChipCalibration{
			SubScore: subScore, TargetKind: targetKind, SourceEpoch: epoch,
			Status: ChipCalibrationStatusNoCurrentEpoch,
			Method: ChipCalibrationMethodPercentileP80,
			CalibrationWindowDays: ChipCalibrationWindowDays,
		}, nil
	}

	preds, labels, err := s.loadChipCalibrationPairs(subScore, targetKind, epoch, asOf, ChipCalibrationWindowDays)
	if err != nil {
		return nil, err
	}

	base := ChipCalibration{
		SubScore: subScore, TargetKind: targetKind, SourceEpoch: epoch,
		Method:                ChipCalibrationMethodPercentileP80,
		CalibrationWindowDays: ChipCalibrationWindowDays,
		NEligible:             len(preds),
	}
	for _, l := range labels {
		if l == 1 {
			base.NPositives++
		}
	}

	if base.NEligible < ChipCalibrationMinEligible {
		base.Status = ChipCalibrationStatusInsufficientEligible
		return &base, nil
	}
	if base.NPositives < ChipCalibrationMinPositives {
		base.Status = ChipCalibrationStatusInsufficientPositive
		return &base, nil
	}

	// Percentile + base-rate guard. Both retained in audit fields so
	// an operator can tell which one chose the final cutoff.
	p80 := percentile(preds, 80)
	baseRate := float64(base.NPositives) / float64(base.NEligible)
	cutoff := math.Max(p80, baseRate)
	base.P80 = &p80
	base.BaseRate = &baseRate
	base.Cutoff = &cutoff
	base.Status = ChipCalibrationStatusActive
	return &base, nil
}

// loadChipCalibrationPairs returns (predictions, labels) over the
// last `windowDays` calendar days inside the given source_epoch,
// anchored on `asOf` (tenant-local "today"). Joins naive_baselines
// with target_snapshots on the same row keys the chip read path
// uses; filters to eligible target rows with populated predictions
// on both sides.
func (s *DB) loadChipCalibrationPairs(subScore, targetKind, epoch string, asOf time.Time, windowDays int) ([]float64, []int, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	to := asOf
	from := to.AddDate(0, 0, -(windowDays - 1))
	// The JOIN constrains `t.source_epoch = n.source_epoch` so a
	// target row written in a previous epoch can't pair with a
	// prediction from the current epoch — same class of cross-epoch
	// leak fixed for naive_baselines reads in #102 and §3.4 of the
	// plan. With baseline writers epoch-clipped (PRs #108, #110) this
	// is the last cross-epoch join that touched calibration data.
	rows, err := s.pool.Query(ctx, `
		SELECT n.predicted_value, t.target_value
		  FROM naive_baselines n
		  JOIN target_snapshots t
		    ON t.date = n.date AND t.sub_score = n.sub_score
		       AND t.target_kind = n.target_kind
		       AND t.source_epoch = n.source_epoch
		 WHERE n.sub_score = $1
		   AND n.target_kind = $2
		   AND n.baseline_kind = $3
		   AND n.source_epoch = $4
		   AND n.predicted_value IS NOT NULL
		   AND t.eligible = TRUE
		   AND t.target_value IS NOT NULL
		   AND n.date BETWEEN $5 AND $6
	`, subScore, targetKind, BaselineKindEventBaseRate, epoch,
		from.Format(isoDate), to.Format(isoDate))
	if err != nil {
		return nil, nil, fmt.Errorf("loadChipCalibrationPairs: %w", err)
	}
	defer rows.Close()
	var preds []float64
	var labels []int
	for rows.Next() {
		var pred, label float32
		if scanErr := rows.Scan(&pred, &label); scanErr != nil {
			return nil, nil, fmt.Errorf("loadChipCalibrationPairs scan: %w", scanErr)
		}
		preds = append(preds, float64(pred))
		l := 0
		if label >= 0.5 {
			l = 1
		}
		labels = append(labels, l)
	}
	return preds, labels, rows.Err()
}

// percentile returns the requested `p`-th percentile of `xs` using
// linear interpolation between order statistics — the standard
// numpy / R `linear` definition. `p` is in [0, 100]; out-of-range
// values panic in callers, but the writer always passes 80 so we
// don't guard.
func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := (p / 100.0) * float64(len(sorted)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return sorted[lo]
	}
	frac := rank - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

