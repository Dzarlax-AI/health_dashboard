package storage

import (
	"maps"
	"slices"
	"strconv"
	"strings"

	dbtmetrics "health-receiver/experiments/dbt_metrics"
)

type metricAlias struct {
	old, canonical string
}

var metricAliases = loadMetricAliases()

func loadMetricAliases() []metricAlias {
	definitions := dbtmetrics.HealthKitAliases()
	aliases := make([]metricAlias, 0, len(definitions))
	for _, name := range slices.Sorted(maps.Keys(definitions)) {
		aliases = append(aliases, metricAlias{name, definitions[name]})
	}
	return aliases
}

type metricUnitRule struct {
	metric, old, canonical string
	divisor, multiplier    float64
}

var metricUnitRules = loadMetricUnitRules()
var liveMetricDefinitions = dbtmetrics.HealthKitLiveQuantities()

const archiveSamplePrefix = "__healthkit_archive_sample:"

func IsReservedMetricKey(metric string) bool {
	return strings.HasPrefix(metric, archiveSamplePrefix)
}

// These v1 metrics aggregate with AVG. The v2 view can sum archive samples
// independently of live hourly totals, while the established cache stays intact.
var separatedArchiveMetrics = catalogMetricKeys(func(q dbtmetrics.HealthKitQuantity) bool { return q.SeparateArchiveSamples })
var currentHourlyMetrics = catalogMetricKeys(func(q dbtmetrics.HealthKitQuantity) bool { return q.LiveGrain == "hour_sum" })
var overlappingArchiveSampleMetrics = catalogMetricKeys(func(q dbtmetrics.HealthKitQuantity) bool {
	return q.LiveGrain == "hour_sum" && !q.SeparateArchiveSamples
})
var currentHourlyStoredKeys = func() []string {
	keys := slices.Clone(currentHourlyMetrics)
	for _, key := range currentHourlyMetrics {
		keys = append(keys, archiveSamplePrefix+key)
	}
	return keys
}()

func usesCurrentHourlyMetric(metric string) bool {
	return slices.Contains(currentHourlyMetrics, CanonicalMetricName(metric))
}

func usesSeparatedArchiveSamples(metric string) bool {
	return slices.Contains(separatedArchiveMetrics, CanonicalMetricName(metric))
}

// UsesCurrentMetricSum reports whether the canonical chart and MCP view use
// per-source sums for a metric.
func UsesCurrentMetricSum(metric string) bool {
	return SumMetrics[CanonicalMetricName(metric)] || usesCurrentHourlyMetric(metric)
}

// CurrentMetricAggregation exposes the effective aggregation for v2 totals.
func CurrentMetricAggregation(metric, requested string) string {
	if usesCurrentHourlyMetric(metric) {
		return "SUM"
	}
	return requested
}

// These archive types had two numeric scales both stored as "%".
var revisionSensitivePercent = catalogMetricKeys(func(q dbtmetrics.HealthKitQuantity) bool { return q.ArchiveScaleRequiresRevision })

func catalogMetricKeys(selectKey func(dbtmetrics.HealthKitQuantity) bool) []string {
	var keys []string
	for key, row := range liveMetricDefinitions {
		if selectKey(row) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

// Keys here come from the embedded catalog. Quoting also handles a future
// catalog label containing a quote.
func catalogSQLList(keys []string) string {
	quoted := make([]string, len(keys))
	for i, key := range keys {
		quoted[i] = "'" + strings.ReplaceAll(key, "'", "''") + "'"
	}
	return strings.Join(quoted, ",")
}

func CanonicalMetricUnit(metric string) string {
	return liveMetricDefinitions[CanonicalMetricName(metric)].Unit
}

func CanonicalLiveMetric(metric string) (key, unit, grain string) {
	key = CanonicalMetricName(metric)
	definition := liveMetricDefinitions[key]
	return key, definition.Unit, definition.LiveGrain
}

func loadMetricUnitRules() []metricUnitRule {
	definitions := dbtmetrics.HealthKitUnitRules()
	var rules []metricUnitRule
	for _, metric := range slices.Sorted(maps.Keys(definitions)) {
		for _, unit := range slices.Sorted(maps.Keys(definitions[metric])) {
			rule := definitions[metric][unit]
			rules = append(rules, metricUnitRule{metric, unit, rule.Unit, rule.Divisor, rule.Multiplier})
		}
	}
	return rules
}

// CanonicalMetricName resolves a historical metric alias for reads.
func CanonicalMetricName(metric string) string {
	return dbtmetrics.CanonicalHealthKitKey(metric)
}

// CanonicalMetricPoint applies the shared wire and archive compatibility rules.
func CanonicalMetricPoint(point MetricPoint) MetricPoint {
	point.MetricName = CanonicalMetricName(point.MetricName)
	rule, ok := dbtmetrics.ResolveHealthKitUnit(point.MetricName, point.Units)
	if !ok {
		return point
	}
	point.Units = rule.Unit
	if rule.Multiplier != 0 {
		point.Qty *= rule.Multiplier
	} else {
		point.Qty /= rule.Divisor
	}
	return point
}

func normalizeStoredMetricPoint(point MetricPoint) MetricPoint {
	return CanonicalMetricPoint(point)
}

// A historical quantity only needs special reads when its key or numeric scale
// changed. Equivalent labels are normalized on ingestion and use normal caches.
func usesMetricCompatibility(metric string) bool {
	key := CanonicalMetricName(metric)
	if slices.Contains(revisionSensitivePercent, key) || usesSeparatedArchiveSamples(key) || IsReservedMetricKey(key) {
		return true
	}
	for _, alias := range metricAliases {
		if metric == alias.old || metric == alias.canonical {
			return true
		}
	}
	for _, rule := range metricUnitRules {
		if rule.metric == metric && !SumMetrics[key] && (rule.divisor != 1 || rule.multiplier != 0) {
			return true
		}
	}
	return false
}

// The CASE expressions below use only the package-owned metric catalog.
// Caller data enters queries as parameters.
func canonicalMetricSQL(column string) string {
	var sql strings.Builder
	sql.WriteString("(CASE " + column)
	for _, alias := range metricAliases {
		sql.WriteString(" WHEN '" + alias.old + "' THEN '" + alias.canonical + "'")
	}
	for _, name := range currentHourlyMetrics {
		sql.WriteString(" WHEN '" + archiveSamplePrefix + name + "' THEN '" + name + "'")
	}
	sql.WriteString(" ELSE " + column + " END)")
	return sql.String()
}

func legacyMetricSQL(column string) string {
	var sql strings.Builder
	sql.WriteString("(CASE " + column)
	for _, alias := range metricAliases {
		sql.WriteString(" WHEN '" + alias.canonical + "' THEN '" + alias.old + "'")
	}
	sql.WriteString(" END)")
	return sql.String()
}

func newerLegacyMetricExists(candidate, snapshotParam string) string {
	return `EXISTS (
		SELECT 1 FROM metric_points alias_point
		WHERE alias_point.metric_name=` + legacyMetricSQL(candidate+".metric_name") + `
		 AND alias_point.date=` + candidate + `.date
		 AND COALESCE(alias_point.source,'')=` + candidate + `.source
		 AND (` + snapshotParam + `::timestamptz IS NULL OR COALESCE(alias_point.source_snapshot_at,
		      CASE WHEN alias_point.origin <> 'apple_health_xml' THEN alias_point.received_at END, '-infinity')>` + snapshotParam + `)
	)`
}

func metricNamesForRead(metric string) []string {
	metric = CanonicalMetricName(metric)
	out := []string{metric}
	if usesCurrentHourlyMetric(metric) {
		out = append(out, archiveSamplePrefix+metric)
	}
	for _, alias := range metricAliases {
		if alias.canonical == metric {
			out = append(out, alias.old)
		}
	}
	return out
}

func compatibilityStoredMetricNames() []string {
	out := slices.Clone(revisionSensitivePercent)
	for _, key := range separatedArchiveMetrics {
		out = append(out, key, archiveSamplePrefix+key)
	}
	for _, rule := range metricUnitRules {
		if !SumMetrics[rule.metric] && (rule.divisor != 1 || rule.multiplier != 0) && !slices.Contains(out, rule.metric) {
			out = append(out, rule.metric)
		}
	}
	for _, alias := range metricAliases {
		out = append(out, alias.old, alias.canonical)
	}
	return slices.Compact(slices.Sorted(slices.Values(out)))
}

const compatibilityPercentEvidence = `
	hr.content_type IN ('apple-health-import; catalog=1','application/json; metrics-contract=2')
	OR CASE WHEN hr.content_type='application/json' AND pg_input_is_valid(hr.payload,'jsonb') THEN
		jsonb_path_exists(hr.payload::jsonb, '$.data.metrics[*] ? (@.name == $metric && @.units == "%")',
			jsonb_build_object('metric', point.metric_name))
	ELSE false END`

const compatibilityPointProof = `EXISTS (
	SELECT 1 FROM health_records hr WHERE hr.id=point.health_record_id AND (` + compatibilityPercentEvidence + `)
)`

const compatibilityReceiptProof = `(point.health_record_id,point.metric_name) IN (SELECT health_record_id,metric_name FROM proven_percent_receipts)`

// Wide reads prove each receipt/metric pair once. Other reads use the primary
// key of health_records directly, without building a history-wide working set.
func compatibilityPercentReceipts(relation, filter string) string {
	return `proven_percent_receipts AS MATERIALIZED (
		SELECT point.health_record_id,point.metric_name
		FROM (
			SELECT DISTINCT health_record_id,metric_name FROM ` + relation + `
			WHERE metric_name IN (` + catalogSQLList(revisionSensitivePercent) + `) AND (` + filter + `)
		) point
		JOIN health_records hr ON hr.id=point.health_record_id
		WHERE ` + compatibilityPercentEvidence + `
	), `
}

func metricCompatibilityCTE(filter string, legacy bool) string {
	return "WITH " + metricCompatibilityProjection("metric_points", filter, compatibilityPointProof, legacy)
}

func metricCompatibilityProjection(relation, filter, proof string, legacy bool) string {
	grainKey := ",grain"
	if legacy {
		// The original key admitted one row for the exact date and source.
		grainKey = ""
	}
	canonical := canonicalMetricSQL("metric_name")
	var quantity, unit strings.Builder
	quantity.WriteString("CASE")
	unit.WriteString("CASE")
	for _, rule := range metricUnitRules {
		condition := " WHEN metric='" + rule.metric + "' AND units='" + rule.old + "' THEN "
		unit.WriteString(condition + "'" + rule.canonical + "'")
		if rule.multiplier != 0 {
			quantity.WriteString(condition + "qty::double precision*" + strconv.FormatFloat(rule.multiplier, 'f', -1, 64))
		} else if rule.divisor != 1 {
			quantity.WriteString(condition + "qty::double precision/" + strconv.FormatFloat(rule.divisor, 'f', -1, 64))
		}
	}
	quantity.WriteString(" ELSE qty::double precision END")
	unit.WriteString(" ELSE COALESCE(units,'') END")
	return `identified AS (
	SELECT ` + canonical + ` AS metric, date, COALESCE(source,'') AS source, qty, units, quality,
	COALESCE(source_snapshot_at,CASE WHEN origin <> 'apple_health_xml' THEN received_at END,
	         '-infinity'::timestamptz) AS snapshot_at, origin,id,
	CASE WHEN metric_name IN (` + catalogSQLList(currentHourlyStoredKeys) + `) THEN
	  CASE WHEN origin='apple_health_xml' OR EXISTS (
	      SELECT 1 FROM health_records hr WHERE hr.id=health_record_id AND hr.content_type IN ('apple-health-import','apple-health-import; catalog=1')
	  ) THEN 'archive_sample'
	  WHEN EXISTS (SELECT 1 FROM health_records hr WHERE hr.id=health_record_id
	               AND hr.content_type='application/json; metrics-contract=2') THEN 'hour_sum'
	  ELSE 'legacy_live' END
	ELSE '' END AS grain
	FROM ` + relation + ` point
	WHERE (` + filter + `)
	  AND (metric_name NOT IN (` + catalogSQLList(revisionSensitivePercent) + `) OR ` + proof + `)
), normalized AS (
	SELECT metric,date,source,` + quantity.String() + ` AS qty,` + unit.String() + ` AS unit,
	       quality,grain,snapshot_at,origin,id
	FROM identified
), compatible AS (
	SELECT DISTINCT ON (metric,date,source` + grainKey + `) metric,date,source,qty,unit,quality,grain
	FROM normalized
	WHERE unit = CASE metric` + compatibilityUnitsSQL() + ` ELSE unit END
	ORDER BY metric,date,source` + grainKey + `,snapshot_at DESC,(origin='live') DESC,id DESC
) `
}

func compatibilityUnitsSQL() string {
	var sql strings.Builder
	for _, name := range append(compatibilityStoredMetricNames(), currentHourlyMetrics...) {
		key := CanonicalMetricName(name)
		if unit := CanonicalMetricUnit(key); unit != "" {
			sql.WriteString(" WHEN '" + key + "' THEN '" + unit + "'")
		}
	}
	return sql.String()
}
