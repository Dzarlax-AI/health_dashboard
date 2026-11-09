package ui

import (
	"fmt"
	"strconv"

	"health-receiver/internal/storage"
)

// Validation merges partial settings without activating daytime delivery.
func validateDaytimeSettings(clean map[string]string, current storage.NotifyConfig) error {
	if value, present := clean["report_daytime_enabled"]; present {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("report_daytime_enabled must be a boolean")
		}
		clean["report_daytime_enabled"] = strconv.FormatBool(enabled)
	}
	hours := map[string]*int{"report_daytime_start_hour": &current.DaytimeStartHour, "report_daytime_end_hour": &current.DaytimeEndHour}
	changed := false
	for key, hour := range hours {
		if value, present := clean[key]; present {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 0 || parsed > 23 {
				return fmt.Errorf("%s must be an hour from 0 to 23", key)
			}
			*hour = parsed
			clean[key] = strconv.Itoa(parsed)
			changed = true
		}
	}
	if changed && current.DaytimeStartHour == current.DaytimeEndHour {
		return fmt.Errorf("daytime start and end must differ")
	}
	return nil
}
