package storage

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"health-receiver/internal/health"
	"health-receiver/internal/sleepstudy"
	"health-receiver/internal/sleepwire"
)

const (
	// Deliberately synthetic values for exercising the shadow calculation.
	// They are neither fitted nor used by the serving bank.
	napShadowDayLimit = 8
	napShadowHalfDose = 30
	napShadowRamp     = 30 * time.Minute
	napShadowFade     = 4 * time.Hour
	napShadowPolicy   = "synthetic-nap-recovery-v1"
)

type energyNap struct {
	End        time.Time         `json:"end"`
	ObservedAt time.Time         `json:"observed_at"`
	Seconds    float64           `json:"seconds"`
	Spans      []sleepstudy.Span `json:"spans,omitempty"`
}

func selectedEnergyNaps(day *rolloutEpisodeDay, main rolloutEpisode, decision sleepstudy.Decision, selected map[string]rolloutEpisode) []energyNap {
	naps := make([]energyNap, 0)
	var counted [][]sleepstudy.Span
	var candidates []rolloutEpisode
	for _, candidate := range day.episodes {
		if candidate.candidate.Coverage != nil && slices.Contains(decision.Additional, candidate.episode.ID) {
			candidates = append(candidates, candidate)
		}
	}
	slices.SortStableFunc(candidates, func(a, b rolloutEpisode) int {
		return a.candidate.Coverage.ObservedAt.Compare(b.candidate.Coverage.ObservedAt)
	})
	for _, candidate := range candidates {
		spans := sleepstudy.Union(candidate.episode.Asleep)
		if len(spans) == 0 || spans[0].Start.Before(main.episode.Asleep[len(main.episode.Asleep)-1].End) {
			continue
		}
		overlaps := false
		for _, previous := range counted {
			overlaps = overlaps || studyIntersects(spans, previous)
		}
		for _, otherMain := range selected {
			overlaps = overlaps || studyIntersects(spans, otherMain.episode.Asleep)
		}
		if overlaps {
			continue
		}
		counted = append(counted, spans)
		naps = append(naps, energyNap{End: spans[len(spans)-1].End.UTC(), ObservedAt: candidate.candidate.Coverage.ObservedAt, Seconds: sleepstudy.Duration(spans).Seconds(), Spans: spans})
	}
	return naps
}

type EnergyNapShadowResult struct {
	Policy                     string                  `json:"policy"`
	Base                       string                  `json:"base"`
	Status                     string                  `json:"status"`
	SourceAvailableAfterWindow int                     `json:"source_available_after_window"`
	Episodes                   int                     `json:"episodes"`
	DisplayBefore              *int                    `json:"display_before,omitempty"`
	Immediate                  *EnergyNapShadowVariant `json:"immediate,omitempty"`
	Gradual                    *EnergyNapShadowVariant `json:"gradual,omitempty"`
}

type EnergyNapDisplayReason string

const (
	NapNoActiveCredit    EnergyNapDisplayReason = "no_active_credit"
	NapCreditRamping     EnergyNapDisplayReason = "ramping"
	NapCreditShown       EnergyNapDisplayReason = "shown"
	NapCreditCapped      EnergyNapDisplayReason = "ceiling_limited"
	NapAtCeiling         EnergyNapDisplayReason = "at_ceiling"
	NapBelowDisplayFloor EnergyNapDisplayReason = "below_display_floor"
	NapCreditRoundedAway EnergyNapDisplayReason = "rounded_away"
)

type EnergyNapShadowVariant struct {
	PointsBeforeLimit float64                `json:"points_before_bank_limit"`
	SignedDelta       int                    `json:"signed_delta"`
	VisibleDelta      int                    `json:"visible_delta"`
	DisplayAfter      int                    `json:"display_after"`
	Reason            EnergyNapDisplayReason `json:"reason"`
}

func compareEnergyNapShadow(base BankResult, unroundedBank float64, naps []energyNap, asOf time.Time) EnergyNapShadowResult {
	out := EnergyNapShadowResult{Policy: napShadowPolicy, Base: "latest_database_snapshot", Status: "compared"}
	if base.State == "stale" {
		out.Status = "base_stale"
		return out
	}
	out.DisplayBefore = &base.Display
	var immediate, gradual float64
	for _, nap := range naps {
		elapsed := asOf.Sub(nap.End)
		if nap.Seconds <= 0 || nap.ObservedAt.IsZero() || asOf.Before(nap.ObservedAt) || elapsed < 0 || elapsed >= napShadowFade {
			continue
		}
		minutes := nap.Seconds / 60
		points := napShadowDayLimit * minutes / (minutes + napShadowHalfDose)
		points *= 1 - float64(elapsed)/float64(napShadowFade)
		immediate += points
		gradual += points * min(1, float64(elapsed)/float64(napShadowRamp))
		out.Episodes++
	}
	project := func(points float64) *EnergyNapShadowVariant {
		points = min(napShadowDayLimit, points)
		projection := unroundedBank + points
		signed := int(math.Round(health.ClampSignedBank(projection)))
		display := clampDisplay(signed)
		variant := &EnergyNapShadowVariant{
			PointsBeforeLimit: points, SignedDelta: signed - base.Bank,
			VisibleDelta: display - base.Display, DisplayAfter: display,
		}
		switch {
		case out.Episodes == 0:
			variant.Reason = NapNoActiveCredit
		case points == 0:
			variant.Reason = NapCreditRamping
		case variant.VisibleDelta > 0 && projection > float64(display) && display == 100:
			variant.Reason = NapCreditCapped
		case variant.VisibleDelta > 0:
			variant.Reason = NapCreditShown
		case base.Display == 100:
			variant.Reason = NapAtCeiling
		case variant.SignedDelta > 0:
			variant.Reason = NapBelowDisplayFloor
		default:
			variant.Reason = NapCreditRoundedAway
		}
		return variant
	}
	out.Immediate, out.Gradual = project(immediate), project(gradual)
	return out
}

// CompareEnergyNapShadow compares policies on the local-day inputs in this
// database snapshot. It does not reconstruct historical base-bank snapshots.
func (s *DB) CompareEnergyNapShadow(ctx context.Context, tenant uuid.UUID, asOf time.Time) (EnergyNapShadowResult, error) {
	out := EnergyNapShadowResult{Policy: napShadowPolicy, Base: "latest_database_snapshot", Status: "unknown_selection"}
	if !s.sleepRolloutTenant || tenant == uuid.Nil || asOf.IsZero() {
		return out, errors.New("nap shadow requires a restricted tenant and comparison time")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(context.Background())
	if err := verifySleepRolloutTenant(ctx, tx, tenant); err != nil {
		return out, err
	}
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	loc := unit.reportTZLocation()
	date := asOf.In(loc).Format(time.DateOnly)
	projection, err := loadSleepGoProjection(ctx, tx)
	if err != nil || projection == nil || date < projection.From || date > projection.Through {
		return out, err
	}
	var raw []byte
	var appliedAfterComparison bool
	var mainEnd *time.Time
	var mainZone string
	var mainMinSeconds float64
	err = tx.QueryRow(ctx, `SELECT d.episode_selection->'energy_naps',p.applied_at>$2,
		(d.episode_selection->>'energy_main_end')::timestamptz,
		COALESCE(d.episode_selection->>'energy_main_zone',''),
		COALESCE((d.episode_selection->>'energy_main_min_seconds')::double precision,0) FROM sleep_rollout_daily d
		JOIN sleep_rollout_packets p ON p.sequence=d.generation AND p.applied_at IS NOT NULL
		WHERE d.wake_date=$1::text::date AND `+ownedSleepGoProjectionSQL+`
		AND (`+readySleepGoProjectionSQL+`)`, date, asOf, projection.Sources).Scan(&raw, &appliedAfterComparison, &mainEnd, &mainZone, &mainMinSeconds)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	// An absent key predates the per-episode projection. A present empty
	// array records a measured selection with no eligible additional sleep.
	if raw == nil {
		out.Status = "no_episode_evidence"
		return out, nil
	}
	var naps []energyNap
	if err := json.Unmarshal(raw, &naps); err != nil {
		return out, err
	}
	// Keep this increment within the tenant-local day even close to midnight.
	naps = energyNapsOnDate(naps, date, loc)
	localTime := asOf.In(loc)
	nextMidnight := time.Date(localTime.Year(), localTime.Month(), localTime.Day()+1, 0, 0, 0, 0, loc)
	out.SourceAvailableAfterWindow = lateEnergyNapCount(naps, nextMidnight)
	if appliedAfterComparison {
		out.Status = "selection_unavailable_at_time"
		if len(naps) > 0 && out.SourceAvailableAfterWindow == len(naps) {
			out.Status = "source_available_after_window"
		}
		return out, nil
	}
	if SleepEarlyObservationsEnabled() && mainEnd != nil && mainMinSeconds > 0 {
		early, err := unit.loadEarlyEnergyNaps(ctx, date, asOf, *mainEnd, mainZone, mainMinSeconds, projection)
		if err != nil {
			return out, err
		}
		naps = mergeShadowEnergyNaps(naps, early)
		naps = energyNapsOnDate(naps, date, loc)
	}
	blocks, _, err := unit.loadReaderSleepWindow(ctx, date, SleepRolloutPublication{goProjection: projection})
	if err != nil {
		return out, err
	}
	inputs, err := unit.loadServedSleepBankInputs(ctx, date, blocks)
	if err != nil {
		return out, err
	}
	base, unrounded := calculateCalendarEnergyBank(inputs, date, unit.GetEnergyConfig())
	result := compareEnergyNapShadow(base, unrounded, naps, asOf)
	result.SourceAvailableAfterWindow = out.SourceAvailableAfterWindow
	if result.Episodes == 0 && out.SourceAvailableAfterWindow == len(naps) && len(naps) > 0 {
		out.Status = "source_available_after_window"
		return out, nil
	}
	return result, nil
}

func energyNapsOnDate(naps []energyNap, date string, loc *time.Location) []energyNap {
	return slices.DeleteFunc(naps, func(nap energyNap) bool { return nap.End.In(loc).Format(time.DateOnly) != date })
}

func lateEnergyNapCount(naps []energyNap, nextMidnight time.Time) int {
	var late int
	for _, nap := range naps {
		effectUntil := minTime(nap.End.Add(napShadowFade), nextMidnight)
		if !nap.ObservedAt.Before(effectUntil) {
			late++
		}
	}
	return late
}

func (s *DB) loadEarlyEnergyNaps(ctx context.Context, date string, asOf, mainEnd time.Time, mainZone string, mainMinSeconds float64, projection *sleepGoProjection) ([]energyNap, error) {
	rows, err := s.pool.Query(ctx, `WITH current_packets AS (
		SELECT DISTINCT ON (source_id,candidate_id) source_id,candidate_id,wake_date,candidate,applied_at
		FROM sleep_rollout_packets
		WHERE source_id=ANY($1::text[]) AND applied_at <= $2 AND candidate ? 'observation'
		ORDER BY source_id,candidate_id,revision DESC
	)
	SELECT e.candidate,e.applied_at FROM current_packets e
	WHERE e.wake_date=$3::text::date AND NOT EXISTS (
		SELECT 1 FROM sleep_rollout_packets p WHERE p.source_id=e.source_id
		AND p.candidate_id=e.candidate_id AND p.candidate ? 'coverage' AND p.applied_at <= $2
	)`, projection.Sources, asOf, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var naps []energyNap
	for rows.Next() {
		var raw []byte
		var applied time.Time
		if err := rows.Scan(&raw, &applied); err != nil {
			return nil, err
		}
		var candidate sleepwire.Candidate
		if err := json.Unmarshal(raw, &candidate); err != nil {
			return nil, err
		}
		if nap, ok := earlyEnergyNap(candidate, applied, mainEnd, mainZone, mainMinSeconds); ok {
			naps = append(naps, nap)
		}
	}
	return naps, rows.Err()
}

func earlyEnergyNap(candidate sleepwire.Candidate, applied, mainEnd time.Time, mainZone string, mainMinSeconds float64) (energyNap, bool) {
	if candidate.Zone != mainZone {
		return energyNap{}, false
	}
	var stages []sleepstudy.Stage
	for _, stage := range candidate.Stages {
		stages = append(stages, sleepstudy.Stage{Kind: stage.Kind, Span: sleepstudy.Span{Start: stage.Start, End: stage.End}})
	}
	spans, coarseEdge := sleepstudy.StagedCoverage(stages, studyBridgeLimit)
	if len(spans) == 0 || coarseEdge || len(sleepstudy.Group(spans, studyBridgeLimit)) != 1 ||
		spans[0].Start.Sub(mainEnd) <= studyBridgeLimit {
		return energyNap{}, false
	}
	seconds := sleepstudy.Duration(spans).Seconds()
	block := studySleepBlock(studyCandidate{candidate: candidate, episode: sleepstudy.Episode{Asleep: spans}})
	if seconds >= mainMinSeconds || *block.Conflict {
		return energyNap{}, false
	}
	return energyNap{End: spans[len(spans)-1].End, ObservedAt: maxTime(applied, candidate.Observation.ObservedAt),
		Seconds: seconds, Spans: spans}, true
}

// Equal evidence from two devices contributes once. Different overlapping
// bounds are held out unless one set is already fully confirmed.
func mergeShadowEnergyNaps(confirmed, early []energyNap) []energyNap {
	out := slices.Clone(confirmed)
	for i, observation := range early {
		if slices.ContainsFunc(confirmed, func(nap energyNap) bool { return studyIntersects(nap.Spans, observation.Spans) }) {
			continue
		}
		skip := false
		for j, other := range early {
			if i == j || !studyIntersects(observation.Spans, other.Spans) {
				continue
			}
			equal := slices.EqualFunc(observation.Spans, other.Spans, func(a, b sleepstudy.Span) bool {
				return a.Start.Equal(b.Start) && a.End.Equal(b.End)
			})
			if !equal || other.ObservedAt.Before(observation.ObservedAt) || (other.ObservedAt.Equal(observation.ObservedAt) && j < i) {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, observation)
		}
	}
	return out
}
