package dbtmetrics

import _ "embed"

//go:embed macros/personal_baseline_stats.sql
var baselineStatsMacro string

//go:embed macros/personal_baseline_result.sql
var baselineResultMacro string

// PersonalBaselineStatsSQL expects samples(value) containing finite channel observations.
var PersonalBaselineStatsSQL = macroBody(baselineStatsMacro)

// PersonalBaselineResultSQL expects summary(channel,sample_count,median,sd,age_ns).
// A missing timestamp has SQL NULL age, including when values themselves are valid.
var PersonalBaselineResultSQL = macroBody(baselineResultMacro)
