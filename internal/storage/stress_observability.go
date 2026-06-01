package storage

import (
	"context"
	"fmt"
	"time"

	"health-receiver/internal/health"
)

// StressObservabilitySummary is a read-only operational view for the
// v2.2 stress-drain gate. It reports observed stress-load data and the
// current effective-beta state without enabling or recomputing drain.
type StressObservabilitySummary struct {
	From               string                  `json:"from"`
	To                 string                  `json:"to"`
	WindowDays         int                     `json:"window_days"`
	Timezone           string                  `json:"timezone"`
	StressDrainEnabled bool                    `json:"stress_drain_enabled"`
	Beta               float64                 `json:"beta"`
	EffectiveBeta      float64                 `json:"effective_beta"`
	ZThreshold         float64                 `json:"z_threshold"`
	Applied            bool                    `json:"applied"`
	Mode               string                  `json:"mode"`
	Distribution       StressDistributionStats `json:"distribution"`
	Validation         health.ValidationReport `json:"validation"`
}

// ComputeStressObservabilitySummary reads existing daily_scores,
// metric_points, and settings only. It must remain safe for ordinary
// admin page loads: no backfills, no settings writes, no validation
// cache refresh.
func (s *DB) ComputeStressObservabilitySummary(
	ctx context.Context,
	tz, asOfDate string,
	windowDays int,
) (StressObservabilitySummary, error) {
	if windowDays <= 0 {
		windowDays = 30
	}
	if windowDays < 7 || windowDays > 90 {
		return StressObservabilitySummary{}, fmt.Errorf("windowDays must be in [7, 90]")
	}
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return StressObservabilitySummary{}, fmt.Errorf("load tz %q: %w", tz, err)
	}
	var asOf time.Time
	if asOfDate == "" {
		asOf = time.Now().In(loc)
	} else {
		asOf, err = time.ParseInLocation("2006-01-02", asOfDate, loc)
		if err != nil {
			return StressObservabilitySummary{}, fmt.Errorf("parse asOfDate %q: %w", asOfDate, err)
		}
	}
	from := asOf.AddDate(0, 0, -windowDays).Format("2006-01-02")
	to := asOf.Format("2006-01-02")

	dist, err := s.ComputeStressDistributionStats(ctx, from, to)
	if err != nil {
		return StressObservabilitySummary{}, err
	}
	validation, err := s.ComputeStressValidationReport(ctx, tz, to, windowDays)
	if err != nil {
		return StressObservabilitySummary{}, err
	}
	cfg := s.GetEnergyConfig()
	mode := "observed_only"
	if cfg.EffectiveBeta() != 0 {
		mode = "applied"
	}
	return StressObservabilitySummary{
		From:               from,
		To:                 to,
		WindowDays:         windowDays,
		Timezone:           tz,
		StressDrainEnabled: cfg.StressDrainEnabled,
		Beta:               cfg.Beta,
		EffectiveBeta:      cfg.EffectiveBeta(),
		ZThreshold:         cfg.ZThreshold,
		Applied:            cfg.EffectiveBeta() != 0,
		Mode:               mode,
		Distribution:       dist,
		Validation:         validation,
	}, nil
}
