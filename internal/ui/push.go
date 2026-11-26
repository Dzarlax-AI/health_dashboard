package ui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	clientapi "health-receiver/internal/api"
	"health-receiver/internal/apnspush"
	"health-receiver/internal/storage"
)

// ConfigureAPNs enables Direct APNs device registration endpoints using the
// process-level APNs sender and expected bundle topic.
func (h *Handler) ConfigureAPNs(sender *apnspush.Sender) {
	h.apnsSender = sender
}

func (h *Handler) pushDeviceRegister(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	if h.apnsSender == nil || !storage.IsInAppInboxEnabled(db) || !storage.IsAPNSPushEnabled(db) {
		http.NotFound(w, r)
		return
	}
	const maxBodyBytes = 2048
	var req clientapi.PushDeviceRegisterRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		jsonError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		jsonError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	deviceID, err := uuid.Parse(strings.TrimSpace(req.DeviceID))
	if err != nil || deviceID == uuid.Nil {
		jsonError(w, "invalid device_id", http.StatusBadRequest)
		return
	}
	req.APNsToken = strings.TrimSpace(req.APNsToken)
	req.APNsEnv = strings.TrimSpace(req.APNsEnv)
	req.BundleID = strings.TrimSpace(req.BundleID)
	req.Locale = strings.TrimSpace(req.Locale)
	if !storage.ValidAPNsToken(req.APNsToken) {
		jsonError(w, "invalid apns_token", http.StatusBadRequest)
		return
	}
	if !storage.ValidAPNsEnv(req.APNsEnv) {
		jsonError(w, "invalid apns_env", http.StatusBadRequest)
		return
	}
	if req.BundleID != h.apnsSender.EffectiveTopic() {
		jsonError(w, "invalid bundle_id", http.StatusBadRequest)
		return
	}
	if !storage.ValidPushLocale(req.Locale) {
		jsonError(w, "invalid locale", http.StatusBadRequest)
		return
	}

	var attemptID uuid.UUID
	if req.RegistrationAttemptID != nil {
		attemptID, err = uuid.Parse(strings.TrimSpace(*req.RegistrationAttemptID))
		if err != nil || attemptID == uuid.Nil {
			jsonError(w, "invalid registration_attempt_id", http.StatusBadRequest)
			return
		}
	}
	dev, err := db.UpsertPushDevice(r.Context(), storage.UpsertPushDeviceInput{
		RegistrationAttemptID: attemptID,
		DeviceID:              deviceID,
		APNsToken:             req.APNsToken,
		APNsEnv:               req.APNsEnv,
		BundleID:              req.BundleID,
		Locale:                req.Locale,
	})
	if err != nil {
		writePushDeviceError(w, err, "failed to save push device")
		return
	}
	jsonResponse(w, clientapi.PushDeviceRegisterResponse{
		DeviceID:            dev.DeviceID,
		BindingID:           dev.BindingID,
		Enabled:             dev.Enabled,
		RegistrationVersion: dev.RegistrationVersion,
		APNsEnv:             dev.APNsEnv,
	})
}

// pushDeviceDelete disables a specific binding or cancels a registration attempt in
// the authenticated tenant schema. Intentionally executes even when
// apns_push_enabled / in_app_inbox_enabled is disabled so queued offline
// deregistrations still deactivate old bindings cleanly.
func (h *Handler) pushDeviceDelete(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	deviceID, err := uuid.Parse(strings.TrimSpace(r.PathValue("device_id")))
	if err != nil || deviceID == uuid.Nil {
		jsonError(w, "invalid device_id", http.StatusBadRequest)
		return
	}
	query := r.URL.Query()
	bindings, attempts := query["binding_id"], query["registration_attempt_id"]
	if len(bindings)+len(attempts) != 1 {
		jsonError(w, "send exactly one binding_id or registration_attempt_id", http.StatusBadRequest)
		return
	}
	if len(attempts) == 1 {
		attemptID, parseErr := uuid.Parse(strings.TrimSpace(attempts[0]))
		if parseErr != nil || attemptID == uuid.Nil {
			jsonError(w, "invalid registration_attempt_id", http.StatusBadRequest)
			return
		}
		err = db.CancelPushRegistrationAttempt(r.Context(), deviceID, attemptID)
	} else {
		bindingID, parseErr := uuid.Parse(strings.TrimSpace(bindings[0]))
		if parseErr != nil || bindingID == uuid.Nil {
			jsonError(w, "invalid binding_id", http.StatusBadRequest)
			return
		}
		_, err = db.DisablePushDeviceBinding(r.Context(), deviceID, bindingID)
	}
	if err != nil {
		writePushDeviceError(w, err, "failed to disable push device")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writePushDeviceError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, storage.ErrInvalidPushRegistrationAttempt):
		jsonError(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, storage.ErrPushRegistrationAttemptConflict):
		jsonError(w, err.Error(), http.StatusConflict)
	default:
		jsonError(w, fallback, http.StatusInternalServerError)
	}
}
