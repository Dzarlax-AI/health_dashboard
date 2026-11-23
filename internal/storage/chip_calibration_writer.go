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
	base := ChipCalibration{SubScore: subScore, TargetKind: targetKind, SourceEpoch: epoch,
		Method: ChipCalibrationMethodPercentileP80, CalibrationWindowDays: ChipCalibrationWindowDays}
	ctx, cancel := queryCtx()
	defer cancel()
	err := s.pool.QueryRow(ctx, chipCalibrationSQL, subScore, targetKind, epoch, asOf.Format(isoDate)).Scan(
		&base.NEligible, &base.NPositives, &base.Status, &base.P80, &base.BaseRate, &base.Cutoff)
	if err != nil {
		return nil, fmt.Errorf("recompute chip calibration: %w", err)
	}
	return &base, nil
}
