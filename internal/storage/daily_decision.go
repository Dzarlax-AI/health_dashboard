package storage

import (
	"context"

	dbtmetrics "health-receiver/experiments/dbt_metrics"
	"health-receiver/internal/health"
)

var decisionInputsSQL = `with decision_inputs as (
    select ''::text as profile,0 as capture,$1::int as readiness,$2::text as guidance_action,
       $3::text as bank_action,$4::text as illness,$5::text as sleep
) , result as (` + dbtmetrics.DailyDecisionSQL + `)`

const decisionResult = " select decision_mode,decision_domain from result"

const todayPacketSQL = `, decision_packets as (
    select 'today_decision'::text metric, $6::text decision_key, 1 formula_version,
        jsonb_build_object('readiness', readiness, 'guidance_action', guidance_action,
            'bank_action', bank_action, 'illness', illness, 'sleep', sleep, 'headline', $7::text) inputs,
        jsonb_build_object('mode',decision_mode,'evidence_domains',array[decision_domain],
            'signal_keys',case when $7::text<>'' then array[$7::text] end) result
    from decision_inputs cross join result
    union all
    select 'today_briefing'::text,$6::text,$10::int,$8::jsonb,$9::jsonb where $8::jsonb is not null
)`

// Historical reconstructions have a different clock and do not publish live receipts.
func (s *DB) liveDailyDecision(ctx context.Context, date string, b *health.BriefingResponse) (*health.DailyDecision, error) {
	return withDecisionJournal(ctx, s, func(db *DB, enabled bool) (*health.DailyDecision, error) {
		if enabled {
			return db.evaluateDailyDecision(ctx, b, date)
		}
		return db.dailyDecision(ctx, b)
	})
}

func (s *DB) dailyDecision(ctx context.Context, b *health.BriefingResponse) (*health.DailyDecision, error) {
	return s.evaluateDailyDecision(ctx, b, "")
}

func (s *DB) evaluateDailyDecision(ctx context.Context, b *health.BriefingResponse, date string) (*health.DailyDecision, error) {
	var guidance, bank *string
	var illness, sleep string
	if b.TodayGuidance != nil {
		guidance = &b.TodayGuidance.Action
	}
	if b.EnergyBank != nil {
		bank = &b.EnergyBank.ActionVerdict
	}
	if b.IllnessSuspicion != nil {
		illness = b.IllnessSuspicion.Confidence
	}
	if b.SleepQuality != nil {
		sleep = b.SleepQuality.Confidence
	}
	var mode, domain string
	query := decisionInputsSQL
	args := []any{b.ReadinessToday, guidance, bank, illness, sleep}
	if date != "" {
		headline := ""
		if b.Headline != nil {
			headline = b.Headline.Key
		}
		query += todayPacketSQL + decisionJournalInsertSQL
		inputs, observed, err := todayBriefingCapture(b)
		if err != nil {
			return nil, err
		}
		args = append(args, date, headline, inputs, observed, ScoreVersion)
	}
	if err := s.pool.QueryRow(ctx, query+decisionResult, args...).Scan(&mode, &domain); err != nil {
		return nil, err
	}
	return health.RenderDailyDecision(b, mode, []string{domain}), nil
}
