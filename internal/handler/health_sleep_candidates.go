package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"health-receiver/internal/ctxdb"
	"health-receiver/internal/sleepwire"
	"health-receiver/internal/storage"
)

const sleepCandidatesAckHeader = "X-Health-Sleep-Candidates-Ack"
const maxSleepCandidatesPerBatch = 32

var errInvalidSleepCandidateBatch = errors.New("invalid sleep candidate batch")

// EnableSleepCandidateBatches is called before registering the ingest routes
// on servers whose runtime pools are isolated.
func (h *Handler) EnableSleepCandidateBatches(prepare bool) {
	h.sleepCandidateBatches = true
	h.sleepCandidatePrepare = prepare
}

// acceptSleepCandidateBatch handles the optional extension on eligible tenants.
// handled is false when the request must take the ordinary legacy path.
func (h *Handler) acceptSleepCandidateBatch(w http.ResponseWriter, r *http.Request, db *storage.DB, record storage.Record, body []byte) (int64, bool) {
	if !h.sleepCandidateBatches {
		return 0, false
	}
	var envelope struct {
		Data struct {
			Metrics    json.RawMessage `json:"metrics"`
			Candidates json.RawMessage `json:"sleep_candidates_v1"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Data.Candidates) == 0 {
		return 0, false
	}
	ctx := r.Context()
	// Extract only the receipt identity before validating the current
	// format. An already committed receipt owns its original validation.
	var receipt struct {
		BatchID string `json:"batch_id"`
	}
	_ = json.Unmarshal(envelope.Data.Candidates, &receipt)
	batchID, err := uuid.Parse(receipt.BatchID)
	if err != nil || batchID == uuid.Nil || batchID.String() != receipt.BatchID {
		if !h.sleepCandidatePrepare {
			return 0, false
		}
		prepare, modeErr := db.SleepRolloutPreparing(ctx)
		switch {
		case modeErr != nil:
			http.Error(w, "sleep candidate intake unavailable", http.StatusServiceUnavailable)
		case prepare:
			http.Error(w, "invalid sleep candidate batch", http.StatusBadRequest)
		default:
			return 0, false
		}
		return 0, true
	}
	if err := h.mgr.VerifyTenantContract(ctx, ctxdb.SchemaFromContext(ctx), db); err != nil {
		http.Error(w, "sleep candidate intake unavailable", http.StatusServiceUnavailable)
		return 0, true
	}
	total, _ := strconv.Atoi(r.Header.Get(hdrSyncSessionTotal))
	if total < 0 || total > 1<<31-1 {
		total = 0
	}
	session := storage.IngestSyncSession{ID: r.Header.Get(hdrSyncSession), Total: total}
	id, err := db.AcceptSleepRolloutBatch(ctx, batchID, record, h.sleepCandidatePrepare, session,
		func() ([]storage.SleepRolloutInput, error) {
			metrics := bytes.TrimSpace(envelope.Data.Metrics)
			if len(metrics) == 0 || metrics[0] != '[' || validateSleepCandidateMetrics(body) != nil {
				return nil, errInvalidSleepCandidateBatch
			}
			candidates, err := parseSleepCandidateBatch(envelope.Data.Candidates)
			if err != nil && !errors.Is(err, sleepwire.ErrUnknownWakeZone) {
				return nil, errInvalidSleepCandidateBatch
			}
			return candidates, err
		})
	switch {
	case errors.Is(err, storage.ErrSleepRolloutDisabled), errors.Is(err, sleepwire.ErrUnknownWakeZone):
		return 0, false
	case errors.Is(err, errInvalidSleepCandidateBatch):
		http.Error(w, "invalid sleep candidate batch", http.StatusBadRequest)
	case errors.Is(err, storage.ErrSleepRolloutConflict):
		http.Error(w, "sleep batch or revision conflict", http.StatusConflict)
	case err != nil:
		log.Print("health ingest: sleep candidate batch acceptance failed")
		http.Error(w, "sleep candidate batch could not be accepted", http.StatusServiceUnavailable)
	default:
		w.Header().Set(sleepCandidatesAckHeader, batchID.String())
		return id, true
	}
	return 0, true
}

// ProcessSleepCandidateMetrics advances the legacy lane of the mixed intake.
// Downstream notifications are scheduled only after the legacy cache commits.
func (h *Handler) ProcessSleepCandidateMetrics(ctx context.Context, db *storage.DB) (bool, error) {
	var dates []string
	var scores, valid bool
	var session storage.IngestSyncSession
	processed, err := db.ProcessSleepRolloutLegacyNext(ctx, func(unit *storage.DB, record storage.SleepRolloutLegacyRecord) error {
		body := []byte(record.Payload)
		if err := validateSleepCandidateMetrics(body); err != nil {
			// Older receipts may predate intake validation. Quarantine their
			// ordinary part durably so the next valid receipt can proceed.
			return unit.SetHealthRecordProcessing(record.ID, "failed", err)
		}
		points, err := h.processAcceptedRecord(unit, record.ID, body, "all")
		if err != nil {
			return err
		}
		dates, scores, session = datesOf(points), affectsReadiness(points), record.Session
		valid = true
		if len(dates) == 0 {
			return nil
		}
		return h.refreshCache(unit, dates, scores)
	})
	if err == nil && processed && valid {
		h.finishSyncChunk(session, db, dates, scores, true)
	}
	return processed, err
}

func validateSleepCandidateMetrics(body []byte) error {
	parsed, err := parseMetricPayload(body)
	if err != nil {
		return err
	}
	for _, point := range parsed.Points {
		if err := storage.ValidateIngestMetricTime(point.Date); err != nil {
			return errors.New("sleep candidate batch contains an invalid metric timestamp")
		}
	}
	return nil
}

func parseSleepCandidateBatch(raw json.RawMessage) ([]storage.SleepRolloutInput, error) {
	var packet struct {
		BatchID    string            `json:"batch_id"`
		Candidates []json.RawMessage `json:"candidates"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&packet); err != nil {
		return nil, err
	}
	batchID, err := uuid.Parse(packet.BatchID)
	if err != nil || batchID == uuid.Nil || batchID.String() != packet.BatchID ||
		len(packet.Candidates) == 0 || len(packet.Candidates) > maxSleepCandidatesPerBatch {
		return nil, errors.New("invalid sleep batch identity or count")
	}
	inputs := make([]storage.SleepRolloutInput, 0, len(packet.Candidates))
	seen := make(map[[2]string]bool, len(packet.Candidates))
	unknownZone := false
	for _, rawCandidate := range packet.Candidates {
		var item struct {
			SourceID  string              `json:"source_id"`
			Candidate sleepwire.Candidate `json:"candidate"`
		}
		decoder := json.NewDecoder(bytes.NewReader(rawCandidate))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&item); err != nil {
			return nil, err
		}
		if item.Candidate.Observation != nil && !storage.SleepEarlyObservationsEnabled() {
			return nil, errInvalidSleepCandidateBatch
		}
		date, err := validateSleepRolloutCandidate(item.SourceID, &item.Candidate)
		if errors.Is(err, sleepwire.ErrUnknownWakeZone) {
			unknownZone = true
		} else if err != nil {
			return nil, err
		}
		key := [2]string{item.SourceID, item.Candidate.ID}
		if seen[key] {
			return nil, errors.New("duplicate sleep candidate key")
		}
		seen[key] = true
		inputs = append(inputs, storage.SleepRolloutInput{
			SourceID: item.SourceID, Candidate: item.Candidate, WakeDate: date, Raw: rawCandidate,
		})
	}
	if unknownZone {
		return nil, sleepwire.ErrUnknownWakeZone
	}
	return inputs, nil
}
