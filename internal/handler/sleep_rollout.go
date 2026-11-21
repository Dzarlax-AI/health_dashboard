package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"health-receiver/internal/sleepwire"
	"health-receiver/internal/storage"
)

// SleepRolloutHandler uses the server's API-key resolver. The resolver must
// return an isolated, verified tenant pool and may not derive it from payload data.
func SleepRolloutHandler(resolve func(*http.Request) (*storage.DB, bool)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		db, ok := resolve(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if db.GetSetting(storage.SleepRolloutModeSetting, "off") != "prepare" {
			http.NotFound(w, r)
			return
		}
		body, err := readIngestBody(w, r)
		if err != nil {
			writeIngestReadError(w, err)
			return
		}
		var packet struct {
			SourceID  string              `json:"source_id"`
			Candidate sleepwire.Candidate `json:"candidate"`
			Legacy    json.RawMessage     `json:"legacy_payload"`
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&packet); err != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid sleep packet", http.StatusBadRequest)
			return
		}
		date, err := validateSleepRolloutCandidate(packet.SourceID, &packet.Candidate)
		if err != nil || !validSleepRolloutLegacy(packet.Legacy) {
			http.Error(w, "invalid sleep candidate or legacy payload", http.StatusBadRequest)
			return
		}
		status, err := db.AcceptSleepRollout(r.Context(), storage.SleepRolloutInput{
			SourceID: packet.SourceID, Candidate: packet.Candidate, WakeDate: date, Raw: body, Legacy: packet.Legacy,
		})
		switch {
		case errors.Is(err, storage.ErrSleepRolloutDisabled):
			http.NotFound(w, r)
		case errors.Is(err, storage.ErrSleepRolloutConflict):
			http.Error(w, "sleep revision conflict", http.StatusConflict)
		case err != nil:
			log.Printf("sleep rollout: durable acceptance failed")
			http.Error(w, "sleep packet could not be accepted", http.StatusServiceUnavailable)
		default:
			w.Header().Set("Content-Type", "application/json")
			if status == "accepted" {
				w.WriteHeader(http.StatusAccepted)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
		}
	}
}

func validateSleepRolloutCandidate(sourceID string, candidate *sleepwire.Candidate) (string, error) {
	if strings.TrimSpace(sourceID) == "" || len(sourceID) > 256 ||
		len(candidate.ID) > 256 || len(candidate.Stages) > 4096 {
		return "", errors.New("invalid sleep candidate bounds")
	}
	// The effective priority is selected from the tenant's policy on apply.
	candidate.Priority = 0
	return candidate.ValidateSource(sourceID)
}

func validSleepRolloutLegacy(body json.RawMessage) bool {
	parsed, err := parseMetricPayload(body)
	if err != nil || len(parsed.Points) == 0 || len(parsed.NightSleepCoverageErr) != 0 || len(parsed.SleepPeriodCoverageErr) != 0 {
		return false
	}
	for _, point := range parsed.Points {
		if !strings.HasPrefix(point.MetricName, "sleep_") && !strings.HasPrefix(point.MetricName, "night_sleep_") && point.MetricName != "nap_total" {
			return false
		}
	}
	return true
}
