package dbtmetrics

import (
	_ "embed"
	"strings"
)

//go:embed macros/chip_baseline_v1.sql
var chipBaselineMacro string

// ChipBaselineSQL calculates each baseline for the selected dated observations.
func ChipBaselineSQL(inputs string) string {
	body := macroBody(chipBaselineMacro)
	return strings.NewReplacer("{{ inputs }}", inputs, "{{ chip_feature_windows_v1('continuous') }}", ChipFeatureWindowsSQL("continuous")).Replace(body)
}

//go:embed macros/chip_calibration_v1.sql
var chipCalibrationMacro string

// ChipCalibrationSQL limits the paired predictions and labels to the current epoch.
func ChipCalibrationSQL(inputs, pairs string) string {
	body := macroBody(chipCalibrationMacro)
	return strings.NewReplacer("{{ inputs }}", inputs, "{{ pairs }}", pairs).Replace(body)
}

//go:embed macros/chip_targets_v1.sql
var chipTargetsMacro string

// ChipTargetsSQL calculates the eligible forward labels for each requested kind.
func ChipTargetsSQL(inputs string) string {
	body := macroBody(chipTargetsMacro)
	return strings.ReplaceAll(body, "{{ inputs }}", inputs)
}

//go:embed macros/chip_feature_windows_v1.sql
var chipFeatureWindowsMacro string

// ChipFeatureWindowsSQL evaluates date- and epoch-bounded numeric input series.
func ChipFeatureWindowsSQL(inputs string) string {
	body := macroBody(chipFeatureWindowsMacro)
	return strings.ReplaceAll(body, "{{ inputs }}", inputs)
}

//go:embed macros/chip_recovery_sleep_v1.sql
var chipRecoverySleepMacro string

func ChipRecoverySleepSQL(inputs string) string {
	return strings.ReplaceAll(macroBody(chipRecoverySleepMacro), "{{ inputs }}", inputs)
}
