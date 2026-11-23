package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	dbtmetrics "health-receiver/experiments/dbt_metrics"
	"health-receiver/internal/health"
)

var legacyBankSQL = `with selected as (select ''::text as profile, ''::text as capture_id,
	$1::integer as readiness,$2::text as headline,$3::float8[] as sleep,$4::float8[] as hrv,$5::float8[] as rhr,
	$6::float8 as steps,$7::float8 as usual_steps,$8::float8 as kcal,$9::float8 as usual_kcal),
legacy as (` + dbtmetrics.EnergyLegacySQL("selected") + `)
select capacity,current,drained,strain,stress,hrv_z,rhr_base,verdict,reason from legacy where state='available'`

// Coherence selects the morning capacity before the daily fallback is calculated.
func (s *DB) computeBriefingSQL(ctx context.Context, d *health.RawMetrics, lang string, projection *health.ReadinessProjection) (*health.BriefingResponse, error) {
	trends, weekly, err := s.briefingMetrics(ctx, d)
	if err != nil {
		return nil, err
	}
	resp := health.ComputeBriefingWithMetrics(*d, lang, projection, trends, weekly)
	headline := ""
	if resp.Headline != nil {
		headline = resp.Headline.Key
	}
	e := health.EnergyBank{}
	var reason string
	var rhrBase float64
	err = s.pool.QueryRow(ctx, legacyBankSQL, resp.ReadinessScore, headline, d.Sleep, d.HRV, d.RHR, d.StepsToday, d.StepsChronic28d, d.ActiveEnergyToday, d.ActiveEnergyChronic28d).Scan(
		&e.Capacity, &e.Current, &e.DrainSoFar, &e.Strain, &e.Stress, &e.HRVZRaw, &rhrBase, &e.ActionVerdict, &reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return resp, nil
	}
	if err != nil {
		return nil, err
	}
	ls := health.GetStrings(lang)
	e.VerdictReason = energyReasonText(ls, reason, e.HRVZRaw, e.Stress, e.Strain)
	e.Components = []health.EnergyBankComponent{
		{Name: "morning_capacity", Value: e.Capacity, Note: fmt.Sprintf(ls["energy_note_capacity"], d.Sleep[0])},
		{Name: "activity_load", Value: e.Strain, Note: fmt.Sprintf("steps %.0f vs 28d avg %.0f, kcal %.0f vs %.0f", d.StepsToday, d.StepsChronic28d, d.ActiveEnergyToday, d.ActiveEnergyChronic28d)},
		{Name: "autonomic_stress", Value: e.Stress, Note: fmt.Sprintf("HRV %.0fms (%+.1f SD), RHR %.0fbpm (%+.1f bpm)", d.HRV[0], e.HRVZRaw, d.RHR[0], d.RHR[0]-rhrBase)},
	}
	resp.EnergyBank = &e
	return resp, nil
}

func energyReasonText(ls health.LangStrings, reason string, z float64, stress, strain int) string {
	t := ls["energy_reason_"+reason]
	switch reason {
	case "high_stress":
		return fmt.Sprintf(t, z, stress)
	case "acwr_spike":
		return fmt.Sprintf(t, float64(strain))
	default:
		return t
	}
}

var guidanceSQL = `with selected as (select ''::text as profile,''::text as capture_id,''::text as mode,
	0 as latest_days,0 as compatible_days,0 as used_days,$1::float8 as hrv_z,$2::int as display_bank,
	$3::float8[] as thresholds,coalesce($4::text[],'{}') as flags,$5::text as confidence,$6::text as pattern),
guidance as (` + dbtmetrics.EnergyGuidanceSQL("selected") + `)
select verdict,reason,rebound from guidance`

func (s *DB) applySnapshotGuidanceSQL(ctx context.Context, resp *health.BriefingResponse, bands health.VerdictBands, ls health.LangStrings) error {
	b := resp.EnergyBank
	var confidence, pattern, verdict, reason string
	if illness := resp.IllnessSuspicion; illness != nil {
		confidence, pattern = illness.Confidence, illness.Pattern
	}
	var rebound bool
	err := s.pool.QueryRow(ctx, guidanceSQL, b.HRVZRaw, b.Current, []float64{float64(bands.Rest), float64(bands.Recovery), float64(bands.PushHard)}, b.Flags, confidence, pattern).Scan(&verdict, &reason, &rebound)
	if err != nil {
		return err
	}
	b.ActionVerdict = verdict
	b.VerdictReason = energyReasonText(ls, reason, b.HRVZRaw, 0, 0)
	if rebound {
		b.VerdictReason += " " + ls["energy_reason_rebound_addon"]
	}
	return nil
}
