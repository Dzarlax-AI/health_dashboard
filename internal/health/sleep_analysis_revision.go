package health

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
)

// SleepAnalysisIdentity follows the observations that can change an analysis,
// not raw sync hashes, receipt times, calendar finality or model phrasing.
func SleepAnalysisIdentity(sleep MorningReportSleep) string {
	if !MorningSleepAnalysisEligible(&sleep, sleep.ReportDate) {
		return ""
	}
	round := func(n int) int { return int(math.Round(float64(n)/5)) * 5 }
	optional := func(n *int) *int {
		if n == nil {
			return nil
		}
		v := round(*n)
		return &v
	}
	fields := map[string]any{"date": sleep.Date, "duration_minutes": round(int(math.Round(*sleep.Hours * 60)))}
	if sleep.RecentRecordedMeanHours != nil {
		fields["recent_mean_minutes"] = round(int(math.Round(*sleep.RecentRecordedMeanHours * 60)))
		fields["recent_nights"] = sleep.RecentRecordedNights
	}
	if e := sleep.Episode; e != nil {
		fields["deep"], fields["rem"], fields["core"], fields["unstaged"], fields["awake"] = optional(e.DeepMinutes), optional(e.REMMinutes), optional(e.CoreMinutes), optional(e.UnspecifiedMinutes), optional(e.AwakeMinutes)
		fields["awake_bouts"], fields["unclassified"] = e.AwakeBouts, optional(e.UnclassifiedMinutes)
		if e.Start != nil {
			fields["start"] = e.Start.Unix() / 300
		}
		if e.Wake != nil {
			fields["wake"] = e.Wake.Unix() / 300
		}
	}
	encoded, _ := json.Marshal(fields)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}
