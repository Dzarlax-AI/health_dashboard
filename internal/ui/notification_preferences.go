package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	clientapi "health-receiver/internal/api"
	"health-receiver/internal/apnspush"
	"health-receiver/internal/storage"
)

func (h *Handler) buildNotificationPreferencesResponse(r *http.Request, db *storage.DB, schema string) (clientapi.NotificationPreferencesResponse, error) {
	defaults := storage.NotifyConfig{}
	if h.mgr != nil {
		defaults = h.mgr.NotifyDefaultsFor(schema)
	}
	cfg := db.GetNotifyConfig(defaults)
	prefs := storage.GetPushCategoryPreferences(db)
	activeDevices, err := db.CountActivePushDevices(r.Context())
	if err != nil {
		activeDevices = 0
	}
	apnsConfigured := h.apnsTransport != nil
	transportMode := h.apnsTransport.EffectiveMode()
	relayURL := ""
	if transportMode == apnspush.TransportModeRelay && h.apnsTransport != nil {
		relayURL = h.apnsTransport.RelayURL
	}
	return clientapi.NotificationPreferencesResponse{
		InboxEnabled:                 storage.IsInAppInboxEnabled(db),
		APNSPushEnabled:              apnsConfigured && storage.IsAPNSPushEnabled(db),
		APNsPushConfigured:           apnsConfigured,
		APNsTransportMode:            transportMode,
		APNsRelayURL:                 relayURL,
		TelegramConfigured:           cfg.HasTelegramCredentials(),
		TelegramNotificationsEnabled: !cfg.TelegramDisabled,
		ActivePushDevicesCount:       activeDevices,
		MorningWeekdayHour:           cfg.MorningWeekdayHour,
		MorningWeekendHour:           cfg.MorningWeekendHour,
		EveningWeekdayHour:           cfg.EveningWeekdayHour,
		EveningWeekendHour:           cfg.EveningWeekendHour,
		DaytimeEnabled:               cfg.DaytimeEnabled,
		DaytimeStartHour:             cfg.DaytimeStartHour,
		DaytimeEndHour:               cfg.DaytimeEndHour,
		PushMorningReport:            prefs.MorningReport,
		PushEveningReport:            prefs.EveningReport,
		PushCheckins:                 prefs.Checkins,
		PushDaytimeUpdates:           prefs.DaytimeUpdates,
		PushDigestsNudges:            prefs.DigestsNudges,
	}, nil
}

func (h *Handler) notificationPreferencesGet(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	resp, err := h.buildNotificationPreferencesResponse(r, db, h.tenantSchema(r))
	if err != nil {
		jsonError(w, "failed to load notification preferences", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, resp)
}

func (h *Handler) notificationPreferencesPut(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	schema := h.tenantSchema(r)
	const maxBodyBytes = 4096
	var req clientapi.NotificationPreferencesRequest
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

	defaults := storage.NotifyConfig{}
	if h.mgr != nil {
		defaults = h.mgr.NotifyDefaultsFor(schema)
	}
	current := db.GetNotifyConfig(defaults)
	updates := make(map[string]string)

	validateHour := func(name string, val *int) error {
		if val == nil {
			return nil
		}
		if *val < 0 || *val > 23 {
			return fmt.Errorf("%s must be an hour from 0 to 23", name)
		}
		updates[name] = strconv.Itoa(*val)
		return nil
	}
	for _, item := range []struct {
		key string
		val *int
	}{
		{"report_morning_weekday", req.MorningWeekdayHour},
		{"report_morning_weekend", req.MorningWeekendHour},
		{"report_evening_weekday", req.EveningWeekdayHour},
		{"report_evening_weekend", req.EveningWeekendHour},
	} {
		if err := validateHour(item.key, item.val); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	if req.DaytimeEnabled != nil {
		updates["report_daytime_enabled"] = strconv.FormatBool(*req.DaytimeEnabled)
	}
	if req.DaytimeStartHour != nil {
		updates["report_daytime_start_hour"] = strconv.Itoa(*req.DaytimeStartHour)
	}
	if req.DaytimeEndHour != nil {
		updates["report_daytime_end_hour"] = strconv.Itoa(*req.DaytimeEndHour)
	}
	if err := validateDaytimeSettings(updates, current); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.TelegramNotificationsEnabled != nil {
		updates[storage.SettingTelegramNotificationsEnabled] = strconv.FormatBool(*req.TelegramNotificationsEnabled)
	}
	if req.PushMorningReport != nil {
		updates[storage.SettingPushPrefMorningReport] = strconv.FormatBool(*req.PushMorningReport)
	}
	if req.PushEveningReport != nil {
		updates[storage.SettingPushPrefEveningReport] = strconv.FormatBool(*req.PushEveningReport)
	}
	if req.PushCheckins != nil {
		updates[storage.SettingPushPrefCheckins] = strconv.FormatBool(*req.PushCheckins)
	}
	if req.PushDaytimeUpdates != nil {
		updates[storage.SettingPushPrefDaytimeUpdates] = strconv.FormatBool(*req.PushDaytimeUpdates)
	}
	if req.PushDigestsNudges != nil {
		updates[storage.SettingPushPrefDigestsNudges] = strconv.FormatBool(*req.PushDigestsNudges)
	}

	if len(updates) > 0 {
		if err := db.SaveSettings(updates); err != nil {
			jsonError(w, "failed to save notification preferences", http.StatusInternalServerError)
			return
		}
	}

	resp, err := h.buildNotificationPreferencesResponse(r, db, schema)
	if err != nil {
		jsonError(w, "failed to load notification preferences", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, resp)
}
