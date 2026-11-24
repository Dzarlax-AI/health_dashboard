package dbtmetrics

import (
	_ "embed"
	"strings"
)

//go:embed macros/wake_candidate_result_v1.sql
var wakeCandidateResultMacro string

// WakeCandidateResultSQL renders the shared candidate selector. cases and
// segments are trusted SQL relations/CTEs; data values remain bind parameters.
func WakeCandidateResultSQL(cases, segments string) string {
	body := macroBody(wakeCandidateResultMacro)
	return strings.NewReplacer("{{ cases }}", cases, "{{ segments }}", segments).Replace(body)
}

//go:embed macros/wake_confirmation_result_v1.sql
var wakeConfirmationResultMacro string

// WakeConfirmationResultSQL renders the shared wall-clock confirmation policy.
func WakeConfirmationResultSQL(observations string) string {
	body := macroBody(wakeConfirmationResultMacro)
	return strings.ReplaceAll(body, "{{ observations }}", observations)
}
