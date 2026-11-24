package dbtmetrics

import (
	_ "embed"
	"strings"
)

//go:embed macros/recent_recorded_sleep.sql
var recentRecordedSleepMacro string

func sleepRoundSQL(value, scale string) string {
	return strings.NewReplacer("{{ value }}", value, "{{ scale }}", scale).Replace(macroBody(weeklySleepRoundMacro))
}

var RecentRecordedSleepSQL = strings.NewReplacer(
	"{{ inputs }}", "recent_inputs",
	"{{ weekly_sleep_round('average_hours','100.0') }}", sleepRoundSQL("average_hours", "100.0"),
	"{{ weekly_sleep_round('(hours-mean_hours)*60','1.0') }}", sleepRoundSQL("(hours-mean_hours)*60", "1.0"),
).Replace(macroBody(recentRecordedSleepMacro))
