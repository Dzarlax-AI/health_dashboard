package storage

import (
	"context"
	"time"

	"health-receiver/internal/health"
)

// EnsureAIBriefingsTable creates the ai_briefings table if it doesn't exist
// and adds any columns introduced in later versions. Safe to call on every startup.
func (s *DB) EnsureAIBriefingsTable() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS ai_briefings (
			date            TEXT PRIMARY KEY,
			insight         TEXT NOT NULL,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			sent_at         TIMESTAMPTZ,
			request_payload JSONB,
			lang            TEXT NOT NULL DEFAULT ''
		)
	`)
	// Migrate existing tables that predate these columns.
	s.pool.Exec(ctx, `ALTER TABLE ai_briefings ADD COLUMN IF NOT EXISTS request_payload JSONB`)
	s.pool.Exec(ctx, `ALTER TABLE ai_briefings ADD COLUMN IF NOT EXISTS lang TEXT NOT NULL DEFAULT ''`)
}

// SaveAIBriefing stores (or replaces) the AI-generated insight for the given date.
// requestPayload is the raw JSON sent to the AI model — stored for auditing and model comparison.
// lang is the response language (en/ru/sr) — used to invalidate the cache when lang changes.
func (s *DB) SaveAIBriefing(date, insight string, requestPayload []byte, lang string) error {
	ctx, cancel := queryCtx()
	defer cancel()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO ai_briefings (date, insight, created_at, request_payload, lang)
		VALUES ($1, $2, NOW(), $3, $4)
		ON CONFLICT (date) DO UPDATE
			SET insight = excluded.insight,
			    created_at = NOW(),
			    request_payload = excluded.request_payload,
			    lang = excluded.lang
	`, date, insight, requestPayload, lang)
	return err
}

// GetAIBriefing returns the stored AI insight for the given date and language, or "" if none.
// If lang is empty, any cached insight is returned regardless of language.
func (s *DB) GetAIBriefing(date, lang string) string {
	ctx, cancel := queryCtx()
	defer cancel()
	var insight string
	s.pool.QueryRow(ctx,
		`SELECT insight FROM ai_briefings WHERE date = $1 AND (lang = '' OR lang = $2)`,
		date, lang).Scan(&insight)
	return insight
}

// HasSentMorningReport returns true when the morning AI report has already been sent today.
func (s *DB) HasSentMorningReport(date string) bool {
	ctx, cancel := queryCtx()
	defer cancel()
	var exists bool
	s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM ai_briefings WHERE date = $1 AND sent_at IS NOT NULL)`,
		date,
	).Scan(&exists)
	return exists
}

// MarkMorningReportSent records that today's morning report was sent.
// Uses upsert so the guard works even when no AI briefing was generated.
func (s *DB) MarkMorningReportSent(date string) error {
	ctx, cancel := queryCtx()
	defer cancel()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO ai_briefings (date, insight, sent_at)
		VALUES ($1, '', NOW())
		ON CONFLICT (date) DO UPDATE SET sent_at = NOW()
	`, date)
	return err
}

// GetTodayStepCount returns the best-source total step_count for the given date.
func (s *DB) GetTodayStepCount(date string) float64 {
	ctx, cancel := queryCtx()
	defer cancel()
	var steps float64
	s.pool.QueryRow(ctx, `
		WITH source_totals AS (
			SELECT source, SUM(qty) AS source_total
			FROM metric_points
			WHERE metric_name = 'step_count' AND SUBSTRING(date,1,10) = $1 AND qty > 0
			GROUP BY source
		) `+preferredSourceSQL, date).Scan(&steps)
	return steps
}

// GetRawMetrics fetches a 30-day RawMetrics snapshot for use by the AI briefing.
// Returns nil when there is insufficient data.
func (s *DB) GetRawMetrics() *health.RawMetrics {
	ctx, cancel := queryCtx()
	defer cancel()
	var lastDate *string
	if err := s.pool.QueryRow(ctx, `SELECT MAX(SUBSTRING(hour,1,10)) FROM hourly_metrics`).Scan(&lastDate); err != nil || lastDate == nil {
		return nil
	}
	data := s.rawMetricsFromDailyScores(*lastDate)
	if data == nil {
		data = s.rawMetricsFromPoints(*lastDate)
	}
	if len(data.WristTemp) == 0 {
		data.WristTemp = s.fetchDailyMetric("wrist_temperature", *lastDate, 30, "AVG")
	}
	return data
}
