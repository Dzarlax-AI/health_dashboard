package storage

import (
	"context"
	"slices"
	"strings"
	"time"
)

// Keep explicit timestamps inclusive, and include every offset in a whole-day window.
func compatibilityDateWindow(to string) (filter, bound string) {
	if date, wholeDay := strings.CutSuffix(to, " 23:59:59"); wholeDay {
		if day, err := time.Parse(time.DateOnly, date); err == nil {
			return "metric_name=ANY($1::text[]) AND date >= $2 AND date < $3", day.AddDate(0, 0, 1).Format(time.DateOnly)
		}
	}
	return "metric_name=ANY($1::text[]) AND date >= $2 AND date <= $3", to
}

const compatibilityHourly = `, compatible_hours AS (
	SELECT metric,SUBSTRING(date,1,13)||':00' AS hour,source,
	AVG(qty) AS qty,MIN(qty) AS low,MAX(qty) AS high,MIN(unit) AS unit
	FROM compatible WHERE qty>0 AND quality='ok'
	GROUP BY metric,SUBSTRING(date,1,13),source
) `

// Sum individual archive samples only within their own source/hour. A live
// reading wins an overlapping source/hour, but never suppresses another source.
var currentCompatibilityHourly = `, minute_values AS (
	SELECT metric, SUBSTRING(date,1,16) AS minute, SUBSTRING(date,21,5) AS utc_offset, source, grain,
	 CASE WHEN grain='legacy_live' AND metric IN (` + catalogSQLList(overlappingArchiveSampleMetrics) + `)
	      THEN MAX(qty) ELSE SUM(qty) END AS qty,
	 COUNT(*) AS point_count, MIN(qty) AS low, MAX(qty) AS high, MIN(unit) AS unit
	FROM compatible WHERE (qty>0 OR (grain='hour_sum' AND qty=0)) AND quality='ok'
	GROUP BY metric,SUBSTRING(date,1,16),SUBSTRING(date,21,5),source,grain
), hours_by_grain AS (
	SELECT metric, SUBSTRING(minute,1,13)||':00' AS hour, utc_offset, source, grain,
	 CASE WHEN grain IN ('archive_sample','hour_sum') OR metric IN (` + catalogSQLList(overlappingArchiveSampleMetrics) + `)
	      THEN SUM(qty) ELSE SUM(qty)/SUM(point_count) END AS qty,
	 MIN(low) AS low,MAX(high) AS high,MIN(unit) AS unit
	FROM minute_values GROUP BY metric,SUBSTRING(minute,1,13),utc_offset,source,grain
), compatible_hours AS (
	SELECT DISTINCT ON (metric,hour,utc_offset,source) metric,hour,source,qty,low,high,unit
	FROM hours_by_grain
	ORDER BY metric,hour,utc_offset,source,(grain='hour_sum') DESC,(grain='legacy_live') DESC
) `

func (s *DB) compatibleMetricData(metric, from, to, bucket, agg string, bySource bool) ([]SourceDataPoints, error) {
	filter, to := compatibilityDateWindow(to)
	query := metricCompatibilityCTE(filter, false)
	source := "source"
	if !bySource {
		source = "''::text"
	}
	if bucket != "minute" && usesCurrentHourlyMetric(metric) {
		query += currentCompatibilityHourly
		period := "hour"
		if bucket == "day" {
			period = "SUBSTRING(hour,1,10)"
		}
		if bySource {
			query += `SELECT ` + period + ` AS bucket,source,SUM(qty),MIN(low),MAX(high)
				FROM compatible_hours GROUP BY bucket,source ORDER BY bucket,source`
		} else {
			query += `SELECT bucket,''::text,MAX(total),MIN(low),MAX(high) FROM (
				SELECT ` + period + ` AS bucket,source,SUM(qty) AS total,MIN(low) AS low,MAX(high) AS high
				FROM compatible_hours GROUP BY bucket,source
			) totals GROUP BY bucket ORDER BY bucket`
		}
	} else if bucket == "hour" || bucket == "day" {
		period := "hour"
		if bucket == "day" {
			period = "SUBSTRING(hour,1,10)"
		}
		query += compatibilityHourly + `SELECT ` + period + ` AS bucket,` + source + ` AS src,AVG(qty),MIN(low),MAX(high)
		FROM compatible_hours GROUP BY bucket,src ORDER BY bucket,src`
	} else {
		if usesCurrentHourlyMetric(metric) {
			// Keep archive and live out of the same sample-time bucket.
			query += `, minute_samples AS (
				SELECT *, DENSE_RANK() OVER (
				 PARTITION BY metric,SUBSTRING(date,1,16),SUBSTRING(date,21,5),source
				 ORDER BY (grain='archive_sample') DESC, (grain='hour_sum') DESC
				) AS preferred
				FROM compatible WHERE qty>0 AND quality='ok'
			) `
			if !bySource && agg == "SUM" {
				query += `SELECT bucket,''::text,` + sumCombineExpr("source_sum") + `,MIN(low),MAX(high) FROM (
					SELECT ` + bucketExpression(bucket) + ` AS bucket,source,SUM(qty) AS source_sum,MIN(qty) AS low,MAX(qty) AS high
					FROM minute_samples WHERE preferred=1 GROUP BY bucket,source
				) totals GROUP BY bucket ORDER BY bucket`
			} else {
				query += `SELECT ` + bucketExpression(bucket) + ` AS bucket,` + source + ` AS src,` + agg + `(qty),MIN(qty),MAX(qty)
					FROM minute_samples WHERE preferred=1 GROUP BY bucket,src ORDER BY bucket,src`
			}
			return s.scanSourcePointsWithKey(query, metricNamesForRead(metric), from, to)
		}
		query += `SELECT ` + bucketExpression(bucket) + ` AS bucket,` + source + ` AS src,` + agg + `(qty),MIN(qty),MAX(qty)
		FROM compatible WHERE qty>0 GROUP BY bucket,src ORDER BY bucket,src`
	}
	return s.scanSourcePointsWithKey(query, metricNamesForRead(metric), from, to)
}

func (s *DB) compatibleMetricSummaries(ctx context.Context) ([]MetricSummary, error) {
	const filter = "metric_name=ANY($1::text[])"
	query := "WITH " + compatibilityPercentReceipts("metric_points", filter) +
		metricCompatibilityProjection("metric_points", filter, compatibilityReceiptProof, false) + `
	SELECT metric,
	 CASE WHEN COUNT(DISTINCT unit)=1 THEN MIN(unit) ELSE '' END,
	 COUNT(DISTINCT (SUBSTRING(date,1,13),source)), MIN(SUBSTRING(date,1,10)),MAX(SUBSTRING(date,1,10))
	FROM compatible WHERE qty>0 AND quality='ok' GROUP BY metric`
	rows, err := s.pool.Query(ctx, query, compatibilityStoredMetricNames())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []MetricSummary
	for rows.Next() {
		var v MetricSummary
		if err = rows.Scan(&v.Name, &v.Units, &v.Count, &v.Min, &v.Max); err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

func (s *DB) compatibleLatestMetricValues(ctx context.Context, legacy bool) ([]LatestValue, error) {
	hourly := compatibilityHourly
	keys := compatibilityStoredMetricNames()
	if !legacy {
		hourly = currentCompatibilityHourly
		keys = append(keys, currentHourlyStoredKeys...)
	}
	keys = slices.Compact(slices.Sorted(slices.Values(keys)))
	canonicalKeys := make([]string, len(keys))
	for i, key := range keys {
		canonicalKeys[i] = CanonicalMetricName(key)
	}
	rows, err := s.pool.Query(ctx, compatibilityLatestSQL(hourly, legacy), keys, legacy, currentHourlyMetrics, canonicalKeys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []LatestValue
	for rows.Next() {
		var v LatestValue
		if err = rows.Scan(&v.Metric, &v.Unit, &v.Date, &v.Value); err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

// Find a candidate day per canonical metric using the stored keys' date
// indexes. Project the whole day so source totals and archive/live precedence
// stay identical. A rejected candidate falls back only for its own metric.
// The candidate selection and both projections share one MVCC snapshot.
func compatibilityLatestSQL(hourly string, legacy bool) string {
	aggregate := hourly + `,
	latest AS (SELECT metric,MAX(SUBSTRING(hour,1,10)) AS day FROM compatible_hours GROUP BY metric),
	by_source AS (
		SELECT h.metric,h.source,MIN(h.unit) AS unit,l.day,SUM(h.qty) AS total,COUNT(*) AS hours
		FROM compatible_hours h JOIN latest l ON l.metric=h.metric AND SUBSTRING(h.hour,1,10)=l.day
		GROUP BY h.metric,h.source,l.day
	)
	SELECT metric,MIN(unit),day,
	 CASE WHEN NOT $2 AND metric=ANY($3::text[]) THEN MAX(total)
	      ELSE SUM(total)/SUM(hours) END
	FROM by_source GROUP BY metric,day`
	return `WITH metric_keys AS (
		SELECT stored_key,metric FROM unnest($1::text[],$4::text[]) AS k(stored_key,metric)
	), candidate_days AS (
		SELECT k.metric,MAX(SUBSTRING(p.date,1,10)) AS day
		FROM metric_keys k
		CROSS JOIN LATERAL (
			SELECT date FROM metric_points
			WHERE metric_name=k.stored_key AND quality='ok'
			  AND (qty>0 OR (NOT $2 AND k.metric=ANY($3::text[]) AND qty=0))
			ORDER BY date DESC LIMIT 1
		) p
		GROUP BY k.metric
	), recent_points AS (
		SELECT p.* FROM metric_keys k JOIN candidate_days d USING(metric)
		CROSS JOIN LATERAL (
			SELECT * FROM metric_points
			WHERE metric_name=k.stored_key AND SUBSTRING(date,1,10)=d.day OFFSET 0
		) p
	), recent_results AS MATERIALIZED (
		WITH ` + metricCompatibilityProjection("recent_points", "true", compatibilityPointProof, legacy) + aggregate + `
	), remaining_keys AS MATERIALIZED (
		SELECT k.stored_key,d.day FROM metric_keys k JOIN candidate_days d USING(metric)
		WHERE NOT EXISTS (SELECT 1 FROM recent_results r WHERE r.metric=k.metric)
	), older_points AS MATERIALIZED (
		SELECT p.* FROM remaining_keys k
		CROSS JOIN LATERAL (
			SELECT * FROM metric_points
			WHERE metric_name=k.stored_key AND date<k.day OFFSET 0
		) p
	)
	SELECT * FROM recent_results
	UNION ALL
	SELECT * FROM (
		WITH ` + compatibilityPercentReceipts("older_points", "true") +
		metricCompatibilityProjection("older_points", "true", compatibilityReceiptProof, legacy) + aggregate + `
	) fallback`
}

// ListMetricsForContract includes the historical keys expected by legacy
// consumers, backed by the same stable unit projection as their graphs.
func (s *DB) ListMetricsForContract(legacy bool) ([]MetricSummary, error) {
	rows, err := s.ListMetrics()
	if err != nil || !legacy {
		return rows, err
	}
	for _, row := range rows {
		for _, alias := range metricAliases {
			if row.Name == alias.canonical {
				legacyRow := row
				legacyRow.Name = alias.old
				rows = append(rows, legacyRow)
			}
		}
	}
	return rows, nil
}

func (s *DB) LatestMetricsForContract(legacy bool) ([]LatestValue, error) {
	rows, err := s.latestMetricValues(legacy)
	if err != nil || !legacy {
		return rows, err
	}
	for _, row := range rows {
		for _, alias := range metricAliases {
			if row.Metric == alias.canonical {
				legacyRow := row
				legacyRow.Metric = alias.old
				rows = append(rows, legacyRow)
			}
		}
	}
	return rows, nil
}

func (s *DB) MetricDataForContract(metric, from, to, bucket, agg string, legacy bool) ([]DataPoint, error) {
	if !legacy && usesCurrentHourlyMetric(metric) {
		rows, err := s.compatibleMetricData(metric, from, to, bucket, "SUM", false)
		if len(rows) == 0 {
			return nil, err
		}
		return rows[0].Points, err
	}
	if !legacy || !usesMetricCompatibility(metric) {
		return s.GetMetricData(metric, from, to, bucket, agg)
	}
	rows, err := s.compatibleLegacyMetricData(metric, from, to, bucket, agg, false)
	if len(rows) == 0 {
		return nil, err
	}
	return rows[0].Points, err
}

func (s *DB) MetricDataBySourceForContract(metric, from, to, bucket, agg string, legacy bool) ([]SourceDataPoints, error) {
	if !legacy && usesCurrentHourlyMetric(metric) {
		return s.compatibleMetricData(metric, from, to, bucket, "SUM", true)
	}
	if !legacy || !usesMetricCompatibility(metric) {
		return s.GetMetricDataBySource(metric, from, to, bucket, agg)
	}
	return s.compatibleLegacyMetricData(metric, from, to, bucket, agg, true)
}

func (s *DB) compatibleLegacyMetricData(metric, from, to, bucket, agg string, bySource bool) ([]SourceDataPoints, error) {
	if !bySource || (agg != "SUM" && agg != "MAX" && agg != "MIN") {
		agg = "AVG"
	}
	filter, to := compatibilityDateWindow(to)
	query := metricCompatibilityCTE(filter, true)
	source := "''::text"
	if bySource {
		source = "SUBSTRING(source,1,POSITION('|' IN source || '|')-1)"
	}
	if bucket == "minute" {
		sqlAgg := aggFuncFor(metric)
		if SumMetrics[CanonicalMetricName(metric)] && !bySource {
			query += `SELECT bucket,''::text,` + sumCombineExpr("source_sum") + `,MIN(low),MAX(high) FROM (
				SELECT SUBSTRING(date,1,16) AS bucket,source,SUM(qty) AS source_sum,MIN(qty) AS low,MAX(qty) AS high
				FROM compatible WHERE qty>0 GROUP BY bucket,source
			) totals GROUP BY bucket ORDER BY bucket`
		} else {
			query += `SELECT SUBSTRING(date,1,16) AS bucket,` + source + ` AS src,` + sqlAgg + `(qty),MIN(qty),MAX(qty)
				FROM compatible WHERE qty>0 GROUP BY bucket,src ORDER BY bucket,src`
		}
		return s.scanSourcePointsWithKey(query, metricNamesForRead(metric), from, to)
	}
	// The old reader weights observations directly outside the hourly cache
	// and weights each source/hour inside it. Preserve that boundary after
	// normalizing compatible units, including for old archive-only dates.
	query += compatibilityHourly + `, cached AS (
		SELECT hour, source FROM hourly_metrics WHERE metric_name=ANY($1::text[])
	), cache_limits AS (
		SELECT MIN(hour) AS first_hour, COUNT(*) FILTER (WHERE hour >= $2 AND hour <= $3) AS window_hours
		FROM cached
	), inputs AS (
		SELECT h.hour AS date,h.source,h.qty,h.low,h.high,true AS cached FROM compatible_hours h
		WHERE EXISTS (SELECT 1 FROM cached c WHERE c.hour=h.hour AND COALESCE(c.source,'')=h.source)
		UNION ALL
		SELECT date,source,qty,qty,qty,false FROM compatible, cache_limits
		WHERE qty>0 AND (first_hour IS NULL OR
		  ($4='day' AND NOT $5 AND SUBSTRING(date,1,10)<SUBSTRING(first_hour,1,10)) OR
		  (($4<>'day' OR $5) AND window_hours=0))
	) `
	period := "SUBSTRING(date,1,13)||':00'"
	if bucket == "day" {
		period = "SUBSTRING(date,1,10)"
	}
	query += `SELECT ` + period + ` AS bucket,` + source + ` AS src,
			CASE WHEN BOOL_OR(cached) THEN AVG(qty) ELSE ` + agg + `(qty) END,MIN(low),MAX(high)
		FROM inputs GROUP BY bucket,src ORDER BY bucket,src`
	return s.scanSourcePointsWithKey(query, metricNamesForRead(metric), from, to, bucket, bySource)
}
