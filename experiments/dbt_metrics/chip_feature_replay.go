package dbtmetrics

import (
	_ "embed"
	"strings"
)

//go:embed models/native_feature_replay_v1.sql
var chipFeatureReplayModel string

// ChipFeatureReplaySQL compiles the native replay model for a runtime
// receipt relation. The dbt model and production writers therefore execute
// the same feature formulas and field mapping.
func ChipFeatureReplaySQL(receipts string) string {
	body := strings.TrimSpace(chipFeatureReplayModel)
	_, body, ok := strings.Cut(body, "\n") // discard the dbt config line
	if !ok {
		panic("invalid native feature replay model")
	}
	body = strings.NewReplacer(
		"{{ recovery_sleep_assessment_v1('sleep_values') }}", RecoverySleepAssessmentSQL("sleep_values"),
		"{{ source('source_data','hdbt_decision_inputs') }}", receipts,
		"{{ chip_feature_windows_v1('streams') }}", ChipFeatureWindowsSQL("streams"),
		"{{ chip_recovery_sleep_v1('sleep_inputs') }}", ChipRecoverySleepSQL("sleep_inputs"),
	).Replace(body)
	if strings.Contains(body, "{{") || strings.Contains(body, "{%") {
		panic("unresolved template in native feature replay model")
	}
	return body
}
