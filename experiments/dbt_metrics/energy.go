package dbtmetrics

import (
	_ "embed"
	"strings"
)

//go:embed macros/energy_bank_v3.sql
var energyBankMacro string

// EnergyBankSQL uses caller-owned SQL relations with the run and day schemas
// expected by the dbt model. Neither argument is a request parameter.
func EnergyBankSQL(runs, days string) string {
	body := macroBody(energyBankMacro)
	return strings.NewReplacer("{{ runs }}", runs, "{{ days }}", days).Replace(body)
}

//go:embed macros/energy_verdict_bands_v1.sql
var energyBandMacro string

// EnergyVerdictBandsSQL returns the calibration CTEs following a decisions CTE.
func EnergyVerdictBandsSQL(history string) string {
	body := macroBody(energyBandMacro)
	return strings.ReplaceAll(body, "{{ history }}", history)
}

//go:embed macros/energy_guidance_v1.sql
var energyGuidanceSQLMacro string

// EnergyGuidanceSQL evaluates the dbt model on a caller-owned dated input relation.
func EnergyGuidanceSQL(bands string) string {
	body := macroBody(energyGuidanceSQLMacro)
	return strings.ReplaceAll(body, "{{ bands }}", bands)
}

//go:embed macros/energy_legacy_v1.sql
var energyLegacySQLMacro string

// EnergyLegacySQL evaluates the dbt model on a caller-owned dated input relation.
func EnergyLegacySQL(inputs string) string {
	body := macroBody(energyLegacySQLMacro)
	return strings.ReplaceAll(body, "{{ inputs }}", inputs)
}
