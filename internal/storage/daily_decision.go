package storage

import (
	"context"

	dbtmetrics "health-receiver/experiments/dbt_metrics"
	"health-receiver/internal/health"
)

var decisionSQL = `with decision_inputs as (
    select ''::text as profile,0 as capture,$1::int as readiness,$2::text as guidance_action,
       $3::text as bank_action,$4::text as illness,$5::text as sleep
) select decision_mode,decision_domain from (` + dbtmetrics.DailyDecisionSQL + `) result`

func (s *DB) dailyDecision(ctx context.Context, b *health.BriefingResponse) (*health.DailyDecision, error) {
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
	if err := s.pool.QueryRow(ctx, decisionSQL, b.ReadinessToday, guidance, bank, illness, sleep).Scan(&mode, &domain); err != nil {
		return nil, err
	}
	return health.RenderDailyDecision(b, mode, []string{domain}), nil
}
