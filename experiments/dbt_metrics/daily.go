package dbtmetrics

import (
	_ "embed"
	"strings"
)

//go:embed macros/daily_candidates.sql
var dailyCandidatesMacro string

//go:embed macros/daily_preferred_sum.sql
var dailyPreferredSumMacro string

//go:embed macros/daily_retained_values.sql
var dailyRetainedMacro string

func DailyPreferredSumSQL(watch, phone string) string {
	return strings.NewReplacer("{{ watch }}", watch, "{{ phone }}", phone).Replace(macroBody(dailyPreferredSumMacro))
}

func DailyCandidatesSQL(watch, phone string) string {
	return strings.NewReplacer("{{ daily_source_mean() }}", DailySourceMeanSQL(), "{{ daily_preferred_sum(watch, phone) }}", DailyPreferredSumSQL(watch, phone)).Replace(macroBody(dailyCandidatesMacro))
}

//go:embed macros/daily_retained_sleep.sql
var dailyRetainedSleepMacro string

func DailyRetainedSleepSQL(fresh, prior string) string {
	return strings.NewReplacer("{{ fresh }}", fresh, "{{ prior }}", prior).Replace(macroBody(dailyRetainedSleepMacro))
}

func DailyRetainedValuesSQL(fresh, prior, stepWithdrawn string) string {
	return strings.NewReplacer("{{ daily_retained_sleep(fresh, prior) }}", DailyRetainedSleepSQL(fresh, prior), "{{ fresh }}", fresh, "{{ prior }}", prior, "{{ step_withdrawn }}", stepWithdrawn).Replace(macroBody(dailyRetainedMacro))
}
