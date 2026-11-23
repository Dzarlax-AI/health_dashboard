package storage

import (
	"os"
	"strconv"

	dbtmetrics "health-receiver/experiments/dbt_metrics"
)

// LegacySleepDBTSetting selects the daily sleep SQL for a tenant's legacy ingest.
const LegacySleepDBTSetting = "legacy_sleep_dbt_enabled"

func (s *DB) legacySleepDBTEnabled() bool {
	return os.Getenv("HEALTH_LEGACY_SLEEP_DBT_ENABLED") == "1" &&
		s.GetSetting(LegacySleepDBTSetting, "false") == "true"
}

func liveDailySQL(useDBT bool) string {
	var selection string
	if useDBT {
		selection = `legacy_sleep_input AS (SELECT date, metric_name, source, sum_val FROM per_source),
` + legacySleepDBTSelectionSQL() + `,
sleep_picked AS (SELECT source AS src FROM picked),
sleep_picked_complete AS (SELECT complete AS ok FROM sleep_gate)`
	} else {
		selection = aggregateSQLWith("live_daily_go_sleep", map[string]string{
			"SLEEP_PICK_SOURCE":         sleepCrossValidationPickSourceExpr("sleep_total_per_source", "sum_val"),
			"SLEEP_TRADITIONAL_METRICS": sqlStringList(sleepTraditionalMetrics),
			"SLEEP_COARSE_METRICS":      sqlStringList(sleepCoarseMetrics),
		})
	}
	return aggregateSQLWith("live_daily", map[string]string{
		"SLEEP_SELECTION_CTES": selection,
		"SUM_WATCH_CONDITION":  sourcePriorityCondition("source", sumSourcePriority[0]),
		"SUM_IPHONE_CONDITION": sourcePriorityCondition("source", sumSourcePriority[1]),
	})
}

func legacySleepDBTSelectionSQL() string {
	return dbtmetrics.LegacySleepSelectionSQL("legacy_sleep_input",
		sleepSourcePriorityCaseExpr("st.source"),
		sqlPolicyNumber(sleepCrossValidationMinHours), sqlPolicyNumber(sleepCrossValidationDivergence),
		sqlStringList(sleepTraditionalMetrics), strconv.Itoa(len(sleepTraditionalMetrics)),
		sqlStringList(sleepCoarseMetrics), strconv.Itoa(len(sleepCoarseMetrics)),
	)
}
