package dbtmetrics

import _ "embed"

//go:embed macros/briefing_stress_signals.sql
var briefingStressMacro string

// BriefingStressSignalsSQL applies the same dbt projection to one normalized day.
func BriefingStressSignalsSQL() string {
	return `select signals.load, array_remove(array[
		case when signals.acute then 'acute_stress' end,
		case when signals.sustained then 'sustained_load' end,
		case when signals.illness_signature then 'illness_signature' end,
		case when signals.recovery_debt then 'recovery_debt' end,
		case when signals.rebound then 'parasympathetic_rebound' end
	],null) from (select $1::float8[] as hour_z, $2::float8 as threshold,
		$3::float8 as temp,$4::float8 as resp,$5::float8 as hrv_drop,
		$6::float8 as rhr,$7::float8 as day_hr) i cross join lateral (` +
		macroBody(briefingStressMacro) + `) signals`
}
