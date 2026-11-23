package storage

import dbtmetrics "health-receiver/experiments/dbt_metrics"

// A receipt identifies the measurement grain, including a measured zero.
// All SQL fragments passed here are internal column predicates or relation names.
func stepHourInputs(pointScope, cacheTable, hourScope string) (string, string) {
	cache := "select null::text as hour,null::text as source,null::real as avg_val,null::real as min_val,null::real as max_val,null::int as sample_count where false"
	if cacheTable != "" {
		cache = "select hour,source,avg_val,min_val,max_val,sample_count from " + cacheTable + " where metric_name='step_count' and (" + hourScope + ")"
	}
	points := dbtmetrics.StepPointInputsSQL("metric_points", "health_records", pointScope)
	return "(" + points + ") input_points", "(" + cache + ") cached_cells"
}

func physicalStepHoursSQL(pointScope, cacheTable, hourScope string) string {
	return dbtmetrics.PhysicalStepHoursSQL(stepHourInputs(pointScope, cacheTable, hourScope))
}

func physicalStepTotalsSQL(pointScope, cacheTable, hourScope string) string {
	return dbtmetrics.DailyStepTotalsSQL("(" + physicalStepHoursSQL(pointScope, cacheTable, hourScope) + ") selected")
}
