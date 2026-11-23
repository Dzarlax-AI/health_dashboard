package dbtmetrics

import (
	_ "embed"
	"strings"
)

//go:embed macros/illness_suspicion_v1.sql
var illnessSuspicionMacro string

// IllnessSuspicionSQL classifies a profile's dated, evaluated signals.
func IllnessSuspicionSQL(inputs string) string {
	return strings.ReplaceAll(macroBody(illnessSuspicionMacro), "{{ inputs }}", inputs)
}
