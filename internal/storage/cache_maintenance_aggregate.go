package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CacheMaintenanceDateRange returns the inclusive lexical date range touched
// by either source points or daily cache rows. Callers walk each date in the
// range so gaps are repaired too. A truly empty source and cache returns
// empty=true.
func (s *DB) CacheMaintenanceDateRange(ctx context.Context) (from, to string, empty bool, err error) {
	var first, last sql.NullString
	err = s.pool.QueryRow(ctx, `
		SELECT MIN(day), MAX(day)
		FROM (
			SELECT SUBSTRING(date, 1, 10) AS day FROM metric_points
			UNION ALL
			SELECT date AS day FROM daily_scores
		) dates`).Scan(&first, &last)
	if err != nil {
		return "", "", false, fmt.Errorf("read cache maintenance date range: %w", err)
	}
	if !first.Valid || !last.Valid {
		return "", "", true, nil
	}
	return first.String, last.String, false, nil
}

// RebuildHistoricalCacheDate rebuilds the legacy historical cache values for
// one date, using only bounded SQL. The caller owns cacheMu and any broader
// maintenance lock. The stages intentionally match the legacy backfill path:
// per-metric hourly aggregation (including unknown sleep metrics' legacy
// SUM/AVG classification), per-column daily aggregation, atomic sleep block,
// overnight baseline, then sustained HR load.
func (s *DB) RebuildHistoricalCacheDate(ctx context.Context, date string) error {
	return s.runCacheTransaction(ctx, func(unit *DB) error {
		if err := unit.rebuildHistoricalAggregateDate(ctx, date); err != nil {
			return err
		}
		loc := unit.reportTZLocation()
		if err := unit.upsertBaselineHROvernightForDateContext(ctx, date, loc); err != nil {
			return err
		}
		_, err := unit.upsertSustainedHRLoadForDateContext(ctx, date, loc)
		return err
	})
}

func (s *DB) rebuildHistoricalAggregateDate(ctx context.Context, date string) error {
	if err := validateCacheMaintenanceDate(date); err != nil {
		return err
	}

	metrics, err := s.listMetricNamesContext(ctx, date)
	if err != nil {
		return fmt.Errorf("list metrics for %s: %w", date, err)
	}
	for _, metric := range metrics {
		if err := s.rebuildHistoricalHourlyMetric(ctx, date, metric); err != nil {
			return fmt.Errorf("hourly %s for %s: %w", metric, date, err)
		}
	}

	for _, spec := range dailyMetricSpecs {
		if isSleepMetric(spec.metric) {
			continue
		}
		if err := s.rebuildHistoricalDailyMetric(ctx, date, spec.column, spec.metric); err != nil {
			return fmt.Errorf("daily %s (%s) for %s: %w", spec.column, spec.metric, date, err)
		}
	}
	if err := s.rebuildHistoricalDailySleepBlock(ctx, date); err != nil {
		return fmt.Errorf("daily sleep block for %s: %w", date, err)
	}

	return nil
}

func validateCacheMaintenanceDate(date string) error {
	d, err := time.Parse("2006-01-02", date)
	if err != nil || d.Format("2006-01-02") != date {
		return fmt.Errorf("invalid cache maintenance date %q", date)
	}
	return nil
}

func (s *DB) listMetricNamesContext(ctx context.Context, date string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT metric_name FROM metric_points WHERE SUBSTRING(date, 1, 10) = $1
		UNION
		SELECT metric_name FROM hourly_metrics WHERE SUBSTRING(hour, 1, 10) = $1
		ORDER BY metric_name`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var metric string
		if err := rows.Scan(&metric); err != nil {
			return nil, err
		}
		out = append(out, metric)
	}
	return out, rows.Err()
}

func (s *DB) rebuildHistoricalHourlyMetric(ctx context.Context, date, metric string) error {
	name := "legacy_hourly_avg"
	if aggFuncFor(metric) == "SUM" {
		name = "legacy_hourly_sum"
	}
	query := aggregateSQLWith(name, map[string]string{
		"SLEEP_DEDUP": sleepDedupClause(metric),
		"FROM_CLAUSE": "AND SUBSTRING(date,1,10) = $2",
	})
	if !isSleepMetric(metric) {
		_, err := s.pool.Exec(ctx, query, metric, date)
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM hourly_metrics WHERE metric_name = $1 AND SUBSTRING(hour,1,10) = $2`, metric, date); err != nil {
		return fmt.Errorf("delete stale sleep hours: %w", err)
	}
	if _, err := tx.Exec(ctx, query, metric, date); err != nil {
		return fmt.Errorf("insert sleep hours: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *DB) rebuildHistoricalDailyMetric(ctx context.Context, date, col, metric string) error {
	if !allowedDailyMetricColumn(col) {
		return fmt.Errorf("unsupported daily metric column %q", col)
	}
	name := "legacy_daily_avg"
	replacements := map[string]string{"FROM_CLAUSE": "AND SUBSTRING(hour,1,10) = $2"}
	if SumMetrics[metric] {
		name = "legacy_daily_sum"
		replacements["SUM_SOURCE_RANK"] = legacySumSourceRankExpr()
	}
	var resultDate string
	var value float64
	err := s.pool.QueryRow(ctx, aggregateSQLWith(name, replacements), metric, date).Scan(&resultDate, &value)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if resultDate != date {
		return fmt.Errorf("bounded daily query returned unexpected date %q", resultDate)
	}
	_, err = s.pool.Exec(ctx, aggregateSQLWith("legacy_daily_upsert", map[string]string{"COLUMN": col}), resultDate, value)
	return err
}

func (s *DB) rebuildHistoricalDailySleepBlock(ctx context.Context, date string) error {
	query := aggregateSQLWith("legacy_daily_sleep", map[string]string{
		"FROM_CLAUSE":               "AND SUBSTRING(hour,1,10) = $1",
		"SLEEP_METRICS":             sqlStringList(sleepMetricNames),
		"SLEEP_TRADITIONAL_METRICS": sqlStringList(sleepTraditionalMetrics),
		"SLEEP_COARSE_METRICS":      sqlStringList(sleepCoarseMetrics),
		"SLEEP_PRIORITY_CASE":       sleepSourcePriorityCaseExpr("source"),
		"SLEEP_MIN_HOURS":           sqlPolicyNumber(sleepCrossValidationMinHours),
		"SLEEP_DIVERGENCE":          sqlPolicyNumber(sleepCrossValidationDivergence),
	})
	_, err := s.pool.Exec(ctx, query, date)
	return err
}
