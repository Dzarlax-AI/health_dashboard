INSERT INTO daily_scores (date, {{COLUMN}}, computed_at)
VALUES ($1, $2, NOW()::TEXT)
ON CONFLICT(date) DO UPDATE SET {{COLUMN}} = excluded.{{COLUMN}}, computed_at = excluded.computed_at
