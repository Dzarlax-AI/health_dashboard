package ai

import (
	"encoding/json"
	"math"

	"health-receiver/internal/health"
)

// MorningProviderPayload separates useful night observations from internal
// capture/finalization bookkeeping. Those states decide eligibility in Go;
// they are not material for the reader-facing sleep analysis.
func MorningProviderPayload(evidence health.MorningInsightEvidence) ([]byte, error) {
	raw, err := json.Marshal(evidence)
	if err != nil {
		return nil, err
	}
	if !health.MorningSleepAnalysisEligible(evidence.NightSleep, evidence.Date) {
		return raw, nil
	}
	var payload map[string]any
	if err = json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	sleep := evidence.NightSleep
	night := map[string]any{"date": sleep.Date, "recorded_sleep_minutes": int(math.Round(*sleep.Hours * 60))}
	if sleep.YesterdayActivity != nil {
		activity := map[string]any{"date": sleep.YesterdayActivity.Date}
		for key, value := range map[string]*float64{"steps": sleep.YesterdayActivity.Steps, "calories": sleep.YesterdayActivity.Calories, "exercise": sleep.YesterdayActivity.Exercise} {
			if value != nil {
				activity[key] = int(math.Round(*value))
			}
		}
		night["yesterday_activity"] = activity
	}
	if sleep.RecentRecordedMeanHours != nil {
		night["recent_recorded_mean_minutes"] = int(math.Round(*sleep.RecentRecordedMeanHours * 60))
		night["recent_recorded_nights"] = sleep.RecentRecordedNights
		night["recent_mean_includes_this_night"] = true
		night["difference_from_recent_mean_minutes"] = sleep.RecentRecordedDifferenceMinutes
	}
	if sleep.BaselineHours != nil {
		night["prior_comparable_mean_minutes"] = int(math.Round(*sleep.BaselineHours * 60))
		night["prior_comparable_nights"] = sleep.BaselineNights
	}
	if e := sleep.Episode; e != nil {
		episode := map[string]any{}
		for key, value := range map[string]*int{"deep_minutes": e.DeepMinutes, "rem_minutes": e.REMMinutes, "core_minutes": e.CoreMinutes, "unstaged_minutes": e.UnspecifiedMinutes, "recorded_awake_minutes": e.AwakeMinutes, "recorded_awake_bouts": e.AwakeBouts, "unclassified_minutes": e.UnclassifiedMinutes} {
			if value != nil {
				episode[key] = *value
			}
		}
		if e.Start != nil {
			episode["start_local"] = e.Start.Format("15:04")
		}
		if e.Wake != nil {
			episode["wake_local"] = e.Wake.Format("15:04")
		}
		night["episode"] = episode
	}
	payload["night_sleep"] = night
	return json.Marshal(payload)
}
