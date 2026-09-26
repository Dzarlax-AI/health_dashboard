package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"health-receiver/internal/health"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// VerifyHistoricalDailyInsightReadAccess proves that an offline reviewer can
// read the two retained sources used by historical snapshot reconstruction.
// Without this probe, a connection pointed at a registry or otherwise
// unauthorized schema can degrade into a misleading all-unavailable corpus.
func (s *DB) VerifyHistoricalDailyInsightReadAccess(ctx context.Context) error {
	for _, table := range []string{"daily_scores", "metric_points"} {
		var probe int
		err := s.pool.QueryRow(ctx, fmt.Sprintf("SELECT 1 FROM %s LIMIT 1", table)).Scan(&probe)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("probe historical insight source %s: %w", table, err)
		}
	}
	return nil
}

// BuildHistoricalDailyInsightSnapshot reconstructs a candidate Today snapshot
// for offline B1 review. It reads only the bounded cache/derived state that is
// relevant to date and deliberately does not create a bundle, persist an
// EnergyBank snapshot, call a provider, or observe any tenant feature flag.
//
// It is not a client API and must remain outside serving paths. The resulting
// candidate reflects the retained aggregate state at materialization time;
// it is not a claim about what a browser rendered on the historical date.
func (s *DB) BuildHistoricalDailyInsightSnapshot(ctx context.Context, date, lang string) (*health.DailyInsightSnapshot, error) {
	parsedDate, err := time.Parse("2006-01-02", date)
	if err != nil {
		return nil, fmt.Errorf("invalid historical insight date %q: %w", date, err)
	}

	// Reconstruct the selected day's raw-point sample counts as well as its
	// cached values. With the cache-only path HRV.SampleCount is always zero,
	// so every otherwise-complete historical recovery becomes data_accruing.
	// freshDayFromRaw is date-bounded and read-only for historical dates too.
	data := s.rawMetricsFromDailyScoresAt(date, true)
	if data == nil {
		data = s.rawMetricsFromPoints(date)
	}
	if data == nil {
		return nil, fmt.Errorf("no retained metrics for historical insight date %s", date)
	}
	if len(data.WristTemp) == 0 {
		data.WristTemp = s.fetchDailyMetric("wrist_temperature", date, 30, "AVG")
	}
	s.populateHistoricalActivityContext(data, date)

	resp := health.ComputeBriefing(*data, lang)
	if resp.Sleep != nil && data.ReadinessEvidence != nil {
		latest := data.ReadinessEvidence.SleepDuration
		if latest.Present && latest.Value != nil && latest.SourceDate == date {
			value := *latest.Value
			resp.Sleep.LatestTotal = &value
			resp.Sleep.LatestDate = latest.SourceDate
		}
	}
	s.applyHistoricalEnergySnapshot(ctx, resp, date, lang)
	health.EnrichLabels(resp, health.GetStrings(lang))
	resp.DailyDecision = health.BuildDailyDecision(resp)

	snapshot := health.BuildDailyInsightSnapshot(resp, lang)
	if snapshot == nil {
		return nil, fmt.Errorf("build historical daily insight snapshot for %s", date)
	}

	// B0 is part of the frozen claim packet even when its tenant flag is off:
	// this is an offline candidate exercise, not an attempt to serve an
	// unapproved claim. A time after the finalization boundary makes the
	// historical evaluation deterministic for the requested local date.
	loc := s.reportTZLocation()
	finalizedAt := time.Date(parsedDate.Year(), parsedDate.Month(), parsedDate.Day(), 18, 1, 0, 0, loc)
	claim, err := s.EvaluateRecentSleepBelowReference(ctx, date, finalizedAt)
	if err != nil {
		// B1 corpus preparation is deliberately usable against retained
		// aggregate state before the optional B0 canonical-night contract has
		// been migrated. A missing B0 table means only that the sleep claim is
		// unavailable; it must not erase valid overall/recovery/energy review
		// candidates. Any other database failure remains visible to the caller.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			return snapshot, nil
		}
		return nil, fmt.Errorf("evaluate historical recent sleep claim for %s: %w", date, err)
	}
	return health.ApplyRecentSleepBelowReference(snapshot, claim, lang), nil
}

func (s *DB) populateHistoricalActivityContext(data *health.RawMetrics, date string) {
	ctx, cancel := queryCtx()
	defer cancel()
	var steps, calories *float64
	if err := s.pool.QueryRow(ctx, `
		SELECT steps, calories
		FROM daily_scores
		WHERE date = $1`, date).Scan(&steps, &calories); err == nil {
		if steps != nil {
			data.StepsToday = *steps
		}
		if calories != nil {
			data.ActiveEnergyToday = *calories
		}
	}
	data.StepsChronic28d = s.chronicAvg(date, "steps")
	data.ActiveEnergyChronic28d = s.chronicAvg(date, "calories")
}

func (s *DB) applyHistoricalEnergySnapshot(ctx context.Context, resp *health.BriefingResponse, date, lang string) {
	if resp == nil || resp.EnergyBank == nil {
		return
	}
	snapshot, err := s.GetLatestEnergySnapshotForDate(ctx, date)
	if err != nil || snapshot == nil {
		return
	}
	display := snapshot.Bank
	if display < 0 {
		display = 0
	}
	if display > 100 {
		display = 100
	}
	drain := snapshot.DrainDelta
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
	resp.EnergyBank.Flags = snapshot.Flags

	bands, bandsErr := s.ComputeUserVerdictBandsThrough(ctx, date)
	if bandsErr != nil {
		bands = health.DefaultV2VerdictBands()
	}
	strings := health.GetStrings(lang)
	verdict := health.ChooseVerdictV2(resp.EnergyBank.HRVZRaw, display, bands)
	reason := health.BuildVerdictReasonV2(verdict, display, resp.EnergyBank.HRVZRaw, strings)
	verdict, reason, _ = health.ApplyStressFlagVerdictOverride(verdict, reason, snapshot.Flags, strings)
	resp.EnergyBank.ActionVerdict = verdict
	resp.EnergyBank.VerdictReason = reason
}
