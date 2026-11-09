package health

import (
	"fmt"
	"math"
)

// readableSleepDuration supplies the exact minute-based wording to the model;
// it must not reconstruct hours and minutes from a rounded decimal hour.
func readableSleepDuration(hours float64, locale string) string {
	if math.IsNaN(hours) || math.IsInf(hours, 0) || hours < 0 {
		return "—"
	}
	return readableSleepMinutes(int(math.Round(hours*60)), locale)
}

func readableSleepMinutes(minutes int, locale string) string {
	hourUnit, minuteUnit := "h", "min"
	if normalizeDailyInsightLocale(locale) == "ru" {
		hourUnit, minuteUnit = "ч", "мин"
	}
	hours, rest := minutes/60, minutes%60
	if hours == 0 {
		return fmt.Sprintf("%d %s", rest, minuteUnit)
	}
	if rest == 0 {
		return fmt.Sprintf("%d %s", hours, hourUnit)
	}
	return fmt.Sprintf("%d %s %d %s", hours, hourUnit, rest, minuteUnit)
}
