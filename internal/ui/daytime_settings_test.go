package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	clientapi "health-receiver/internal/api"
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

func TestDaytimeSettingsValidatePartialUpdates(t *testing.T) {
	current := storage.NotifyConfig{DaytimeStartHour: 9, DaytimeEndHour: 21}
	for _, clean := range []map[string]string{
		{"report_daytime_enabled": "yes"},
		{"report_daytime_start_hour": "24"},
		{"report_daytime_end_hour": "9"},
		{"report_daytime_start_hour": "-1"},
	} {
		if err := validateDaytimeSettings(clean, current); err == nil {
			t.Fatalf("accepted invalid update: %v", clean)
		}
	}
	if err := validateDaytimeSettings(map[string]string{"report_daytime_start_hour": "22", "report_daytime_end_hour": "7"}, current); err != nil {
		t.Fatalf("overnight window rejected: %v", err)
	}
	for _, lang := range []string{"en", "ru", "sr"} {
		for _, key := range []string{"admin_notify_daytime_title", "admin_notify_daytime_enabled", "admin_notify_daytime_hint", "admin_notify_daytime_start", "admin_notify_daytime_end", "admin_notify_save_error"} {
			if value := health.GetStrings(lang)[key]; value == "" || value == key {
				t.Fatalf("missing %s/%s", lang, key)
			}
		}
	}
}

func TestDaytimeSettingsRemainOffUntilExplicitTenantSave(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()
	if cfg := db.GetNotifyConfig(storage.NotifyConfig{DaytimeEnabled: true}); cfg.DaytimeEnabled || cfg.DaytimeStartHour != 9 || cfg.DaytimeEndHour != 21 {
		t.Fatalf("new tenant settings: %+v", cfg)
	}
	h := &Handler{mgr: &tenants.Manager{}}
	update := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.userSettings(w, requestWithTenant(http.MethodPost, body, db, schema))
		return w
	}
	if w := update(`{"report_daytime_enabled":"true","report_daytime_start_hour":"10","report_daytime_end_hour":"20"}`); w.Code != http.StatusOK {
		t.Fatalf("save status %d: %s", w.Code, w.Body.String())
	}
	if w := update(`{"report_daytime_enabled":"false","report_daytime_start_hour":"20"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid partial update status %d", w.Code)
	}
	w := httptest.NewRecorder()
	h.userSettings(w, requestWithTenant(http.MethodGet, "", db, schema))
	var response clientapi.NotificationSettingsResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || !response.DaytimeEnabled || response.DaytimeStartHour != 10 || response.DaytimeEndHour != 20 {
		t.Fatalf("settings were not preserved: %d %s", w.Code, w.Body.String())
	}
	if db.GetSettingExists("telegram_token") {
		t.Fatal("partial settings save changed Telegram credentials")
	}
}
