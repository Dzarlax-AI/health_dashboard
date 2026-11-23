package dbtmetrics

import _ "embed"

//go:embed macros/sleep_regularity_result.sql
var sleepRegularityMacro string

var SleepRegularitySQL = macroBody(sleepRegularityMacro)

//go:embed macros/sleep_architecture_days.sql
var sleepArchitectureMacro string

var SleepArchitectureDaysSQL = macroBody(sleepArchitectureMacro)

//go:embed macros/sleep_architecture_windows.sql
var sleepArchitectureWindowsMacro string

var SleepArchitectureWindowsSQL = macroBody(sleepArchitectureWindowsMacro)
