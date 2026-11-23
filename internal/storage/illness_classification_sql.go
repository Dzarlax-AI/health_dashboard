package storage

import (
	"context"
	"encoding/json"

	dbtmetrics "health-receiver/experiments/dbt_metrics"
	"health-receiver/internal/health"
)

var illnessClassificationSQL = `with inputs as (
	select ''::text as profile,''::text as capture,$1::jsonb as signals
), decision as (` + dbtmetrics.IllnessSuspicionSQL("inputs") + `)
select confidence,pattern from decision`

func (s *DB) classifyIllness(ctx context.Context, in health.IllnessEvidenceInput) (*health.IllnessSuspicion, error) {
	signals, rules := health.IllnessClassificationInputs(in)
	payload, err := json.Marshal(rules)
	if err != nil {
		return nil, err
	}
	var confidence, pattern string
	if err := s.pool.QueryRow(ctx, illnessClassificationSQL, json.RawMessage(payload)).Scan(&confidence, &pattern); err != nil {
		return nil, err
	}
	return health.PresentIllnessSuspicion(in, signals, confidence, pattern), nil
}
