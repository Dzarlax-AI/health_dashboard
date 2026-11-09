package storage

import (
	"context"

	"health-receiver/internal/health"
)

// BuildMorningInsightReviewEvidence prepares the same packet as the scheduled
// generator, without generation, cache persistence or delivery reservation.
func (s *DB) BuildMorningInsightReviewEvidence(ctx context.Context, b *health.BriefingResponse, date, lang string) (health.MorningInsightEvidence, error) {
	return s.morningInsightEvidence(ctx, b, s.GetRawMetrics(), date, lang)
}
