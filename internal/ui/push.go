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
	h.apnsTransport = apnspush.DirectTransportConfig(sender)
}

// ConfigureAPNsTransport configures either Direct APNs or APNs Relay mode.
func (h *Handler) ConfigureAPNsTransport(cfg *apnspush.TransportConfig) {
	h.apnsTransport = cfg
}

func (h *Handler) pushDeviceRegister(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	if h.apnsTransport == nil || !storage.IsInAppInboxEnabled(db) || !storage.IsAPNSPushEnabled(db) {
		http.NotFound(w, r)
		return
	}
	const maxBodyBytes = 4096
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
	transport := storage.PushTransportDirect
	if req.Transport != nil && strings.TrimSpace(*req.Transport) != "" {
		transport = strings.TrimSpace(*req.Transport)
	}
	if transport != h.apnsTransport.EffectiveMode() {
		jsonError(w, "push transport mode mismatch", http.StatusBadRequest)
		return
	}

	req.APNsToken = strings.TrimSpace(req.APNsToken)
	req.APNsEnv = strings.TrimSpace(req.APNsEnv)
	req.BundleID = strings.TrimSpace(req.BundleID)
	req.Locale = strings.TrimSpace(req.Locale)
	if !storage.ValidAPNsEnv(req.APNsEnv) {
		jsonError(w, "invalid apns_env", http.StatusBadRequest)
		return
	}
	if req.BundleID != h.apnsTransport.EffectiveTopic() {
		jsonError(w, "invalid bundle_id", http.StatusBadRequest)
		return
	}
	if !storage.ValidPushLocale(req.Locale) {
		jsonError(w, "invalid locale", http.StatusBadRequest)
		return
	}

	var bindingID uuid.UUID
	var relayGrantID *uuid.UUID
	var relayGrantSecret string
	if transport == storage.PushTransportDirect {
		if !storage.ValidAPNsToken(req.APNsToken) {
			jsonError(w, "invalid apns_token", http.StatusBadRequest)
			return
		}
		if req.RelayGrantID != nil || req.RelayGrantSecret != nil || req.BindingID != nil {
			jsonError(w, "relay fields are not allowed in direct mode", http.StatusBadRequest)
			return
		}
	} else {
		if req.APNsToken != "" {
			jsonError(w, "apns_token must be empty in relay mode", http.StatusBadRequest)
			return
		}
		if req.BindingID == nil {
			jsonError(w, "binding_id is required in relay mode", http.StatusBadRequest)
			return
		}
		bindingID, err = uuid.Parse(strings.TrimSpace(*req.BindingID))
		if err != nil || bindingID == uuid.Nil {
			jsonError(w, "invalid binding_id", http.StatusBadRequest)
			return
		}
		if req.RelayGrantID == nil {
			jsonError(w, "relay_grant_id is required in relay mode", http.StatusBadRequest)
			return
		}
		gid, parseErr := uuid.Parse(strings.TrimSpace(*req.RelayGrantID))
		if parseErr != nil || gid == uuid.Nil {
			jsonError(w, "invalid relay_grant_id", http.StatusBadRequest)
			return
		}
		relayGrantID = &gid
		if req.RelayGrantSecret == nil {
			jsonError(w, "relay_grant_secret is required in relay mode", http.StatusBadRequest)
			return
		}
		relayGrantSecret = strings.TrimSpace(*req.RelayGrantSecret)
		if !storage.ValidRelayGrantSecret(relayGrantSecret) {
			jsonError(w, "invalid relay_grant_secret", http.StatusBadRequest)
			return
		}
	}

	var attemptID uuid.UUID
	if req.RegistrationAttemptID != nil {
		attemptID, err = uuid.Parse(strings.TrimSpace(*req.RegistrationAttemptID))
		if err != nil || attemptID == uuid.Nil {
			jsonError(w, "invalid registration_attempt_id", http.StatusBadRequest)
			return
		}
	}
	contractVer := storage.InboxContractV1
	if req.InboxContractVersion != nil {
		if *req.InboxContractVersion < storage.InboxContractV1 || *req.InboxContractVersion > storage.InboxContractV2 {
			jsonError(w, "invalid inbox_contract_version", http.StatusBadRequest)
			return
		}
		contractVer = *req.InboxContractVersion
	}
	dev, err := db.UpsertPushDevice(r.Context(), storage.UpsertPushDeviceInput{
		RegistrationAttemptID: attemptID,
		DeviceID:              deviceID,
		TransportMode:         transport,
		BindingID:             bindingID,
		APNsToken:             req.APNsToken,
		RelayGrantID:          relayGrantID,
		RelayGrantSecret:      relayGrantSecret,
		APNsEnv:               req.APNsEnv,
		BundleID:              req.BundleID,
		Locale:                req.Locale,
		InboxContractVersion:  contractVer,
	})
	if err != nil {
		writePushDeviceError(w, err, "failed to save push device")
		return
	}
	respContractVer := dev.InboxContractVersion
	if respContractVer < storage.InboxContractV1 {
		respContractVer = contractVer
	}
	jsonResponse(w, clientapi.PushDeviceRegisterResponse{
		DeviceID:             dev.DeviceID,
		BindingID:            dev.BindingID,
		Enabled:              dev.Enabled,
		RegistrationVersion:  dev.RegistrationVersion,
		APNsEnv:              dev.APNsEnv,
		InboxContractVersion: &respContractVer,
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
