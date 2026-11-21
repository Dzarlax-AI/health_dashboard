package handler

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"time"

	"health-receiver/internal/metriccontract"

	"health-receiver/internal/storage"
)

// The v2 envelope adds declared grain. Its established quantity fields remain
// readable by a previous worker during restart or binary rollback.
func validateCurrentMetricPayload(body []byte) error {
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return err
	}
	for _, metric := range p.Data.Metrics {
		key, unit, grain := storage.CanonicalLiveMetric(metric.Name)
		if storage.IsReservedMetricKey(metric.Name) || key != metric.Name ||
			(grain != "" && (metric.Units != unit || metric.Grain != grain)) ||
			(grain == "" && metric.Grain != "") {
			return errors.New("invalid metric key, unit or grain")
		}
		if grain == "" {
			continue
		}
		for _, raw := range metric.Data {
			var sample struct {
				Date string   `json:"date"`
				Qty  *float64 `json:"qty"`
				Avg  *float64 `json:"Avg"`
			}
			if err := json.Unmarshal(raw, &sample); err != nil {
				return errors.New("invalid quantity observation")
			}
			const dateLayout = "2006-01-02 15:04:05 -0700"
			stamp, err := time.Parse(dateLayout, sample.Date)
			if err != nil || stamp.Format(dateLayout) != sample.Date ||
				(grain == "hour_sum" && (stamp.Minute() != 0 || stamp.Second() != 0)) ||
				(key == "heart_rate" && sample.Avg == nil) || (key != "heart_rate" && sample.Qty == nil) {
				return errors.New("invalid quantity observation")
			}
		}
	}
	return nil
}

func metricContentType(r *http.Request, version metriccontract.Version) string {
	if version == metriccontract.Current {
		return "application/json; metrics-contract=2"
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media == "apple-health-import" {
		return ""
	}
	return media
}
