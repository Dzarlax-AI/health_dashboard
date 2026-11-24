package storage

import (
	"context"

	dbtmetrics "health-receiver/experiments/dbt_metrics"
	"health-receiver/internal/health"
)

const todayInputPacket = `jsonb_build_object('readiness', readiness, 'guidance_action', guidance_action,
            'bank_action', bank_action, 'illness', illness, 'sleep', sleep, 'headline', $7::text)`

const decisionInputsSQL = `with decision_inputs as (
    select ''::text as profile,0 as capture,$1::int as readiness,$2::text as guidance_action,
       $3::text as bank_action,$4::text as illness,$5::text as sleep
)`

const decisionResult = " select decision_mode,decision_domain from result"

const todayPacketSQL = `, decision_packets as (
    select 'today_decision'::text metric, $6::text decision_key, 1 formula_version,
        ` + todayInputPacket + ` inputs,
        jsonb_build_object('mode',decision_mode,'evidence_domains',array[decision_domain],
            'signal_keys',case when $7::text<>'' then array[$7::text] end) result
    from decision_inputs cross join result
    union all
    select 'today_briefing'::text,$6::text,$10::int,$8::jsonb,$9::jsonb where $8::jsonb is not null
)`

// Historical reconstructions have a different clock and do not publish live receipts.
func (s *DB) liveDailyDecision(ctx context.Context, date string, b *health.BriefingResponse) (*health.DailyDecision, error) {
	return withDecisionJournal(ctx, s, func(db *DB, enabled bool) (*health.DailyDecision, error) {
		return db.evaluateDailyDecision(ctx, b, date, enabled)
	})
}

func (s *DB) dailyDecision(ctx context.Context, b *health.BriefingResponse) (*health.DailyDecision, error) {
	return s.evaluateDailyDecision(ctx, b, b.Date, false)
}

func (s *DB) evaluateDailyDecision(ctx context.Context, b *health.BriefingResponse, date string, journal bool) (*health.DailyDecision, error) {
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
	headline := ""
	if b.Headline != nil {
		headline = b.Headline.Key
	}
	args := []any{b.ReadinessToday, guidance, bank, illness, sleep}
	query := decisionInputsSQL + ", result as (" + dbtmetrics.DailyDecisionSQL + ")"
	var mode, domain string
	var usePublished bool
	if date != "" && s.profileGraphSchema == "" {
		keyArgs := append(append([]any{}, args...), date, headline)
		const lookup = decisionInputsSQL + ` select r.result->>'mode',r.result#>>'{evidence_domains,0}'
			from decision_inputs join hdbt_decision_results r on r.metric='today_decision'
			and r.decision_key=$6 and r.formula_version=1 and r.input_hash=
			encode(sha256(convert_to(jsonb_build_array(1,` + todayInputPacket + `)::text,'UTF8')),'hex')
			where ` + decisionPublishedReadPredicate
		_, usePublished, _ = withPublishedDecision(ctx, s, func(db *DB) (bool, bool, error) {
			err := db.pool.QueryRow(ctx, lookup, keyArgs...).Scan(&mode, &domain)
			return err == nil, err == nil, err
		})
		if usePublished {
			if !journal {
				return health.RenderDailyDecision(b, mode, []string{domain}), nil
			}
			// A known decision can accompany a new briefing capture.
			query = decisionInputsSQL + ", result as (select $11::text as decision_mode,$12::text as decision_domain)"
		}
	}
	if journal {
		query += todayPacketSQL + decisionJournalInsertSQL
		inputs, observed, err := todayBriefingCapture(b)
		if err != nil {
			return nil, err
		}
		args = append(args, date, headline, inputs, observed, ScoreVersion)
		if usePublished {
			args = append(args, mode, domain)
		}
	}
	if err := s.pool.QueryRow(ctx, query+decisionResult, args...).Scan(&mode, &domain); err != nil {
		return nil, err
	}
	return health.RenderDailyDecision(b, mode, []string{domain}), nil
}
