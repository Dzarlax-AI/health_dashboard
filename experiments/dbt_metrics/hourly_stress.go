package dbtmetrics

import (
	_ "embed"
	"strings"
)

//go:embed macros/hourly_stress.sql
var hourlyStressMacro string

var HourlyStressSQL = strings.ReplaceAll(macroBody(hourlyStressMacro), "{{ hours }}", "hours")
