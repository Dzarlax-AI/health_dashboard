package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	clientapi "health-receiver/internal/api"
	"health-receiver/internal/apnspush"
	"health-receiver/internal/storage"
)

func TestPushRelayRegistrationAndPreferencesModes(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	if err := db.MigrateSchemaContractContext(ctx); err != nil {
		t.Fatalf("migrate v24: %v", err)
	}
	if err := db.SaveSettings(map[string]string{
		storage.SettingInAppInboxEnabled: "true",
		storage.SettingAPNSPushEnabled:   "true",
	}); err != nil {
		t.Fatalf("enable inbox/apns: %v", err)
	}

	h := New(nil, nil, false)

	// 1. Mode "none": even if apns_push_enabled=true in DB, preferences MUST report
	// apns_push_configured=false, apns_push_enabled=false, apns_transport_mode="none".
	recNone := httptest.NewRecorder()
	h.notificationPreferencesGet(recNone, httptest.NewRequest(http.MethodGet, "/api/notifications/preferences", nil).WithContext(adminContext(db, schema)))
	if recNone.Code != http.StatusOK {
		t.Fatalf("GET preferences (none) status=%d", recNone.Code)
	}
	var prefsNone clientapi.NotificationPreferencesResponse
	if err := json.Unmarshal(recNone.Body.Bytes(), &prefsNone); err != nil {
		t.Fatal(err)
	}
	if prefsNone.APNsPushConfigured || prefsNone.APNSPushEnabled || prefsNone.APNsTransportMode != apnspush.TransportModeNone || prefsNone.APNsRelayURL != "" {
		t.Fatalf("unexpected none preferences: %+v", prefsNone)
	}

	// 2. Mode "relay": preferences report relay mode + configured URL.
	relayClient, err := apnspush.NewRelayClient("https://push.dzarlax.dev", apnspush.DefaultTopic, false)
	if err != nil {
		t.Fatalf("NewRelayClient: %v", err)
	}
	h.ConfigureAPNsTransport(apnspush.RelayTransportConfig(relayClient))

	recRelayPrefs := httptest.NewRecorder()
	h.notificationPreferencesGet(recRelayPrefs, httptest.NewRequest(http.MethodGet, "/api/notifications/preferences", nil).WithContext(adminContext(db, schema)))
	if recRelayPrefs.Code != http.StatusOK {
		t.Fatalf("GET preferences (relay) status=%d", recRelayPrefs.Code)
	}
	var prefsRelay clientapi.NotificationPreferencesResponse
	if err := json.Unmarshal(recRelayPrefs.Body.Bytes(), &prefsRelay); err != nil {
		t.Fatal(err)
	}
	if !prefsRelay.APNsPushConfigured || !prefsRelay.APNSPushEnabled || prefsRelay.APNsTransportMode != apnspush.TransportModeRelay || prefsRelay.APNsRelayURL != "https://push.dzarlax.dev" {
		t.Fatalf("unexpected relay preferences: %+v", prefsRelay)
	}

	// 3. Direct payload rejected when server is in relay mode.
	directBody, _ := json.Marshal(map[string]any{
		"device_id":  uuid.New().String(),
		"apns_token": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"apns_env":   "production",
		"bundle_id":  apnspush.DefaultTopic,
		"locale":     "en",
	})
	recWrongMode := httptest.NewRecorder()
	h.pushDeviceRegister(recWrongMode, httptest.NewRequest(http.MethodPost, "/api/push/devices", bytes.NewReader(directBody)).WithContext(adminContext(db, schema)))
	if recWrongMode.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for direct payload on relay server, got %d %s", recWrongMode.Code, recWrongMode.Body.String())
	}

	// 4. Valid relay registration stores binding_id, relay_grant_id, relay_grant_secret (empty apns_token) and replays idempotently.
	deviceID := uuid.New()
	bindingID := uuid.New()
	grantID := uuid.New()
	attemptID, _ := uuid.NewV7()
	secret := "client-generated-delivery-secret-32bytes"
	relayTransport := storage.PushTransportRelay
	contractV2 := storage.InboxContractV2
	bindingStr := bindingID.String()
	grantStr := grantID.String()
	attemptStr := attemptID.String()

	regReq := clientapi.PushDeviceRegisterRequest{
		RegistrationAttemptID: &attemptStr,
		DeviceID:              deviceID.String(),
		Transport:             &relayTransport,
		BindingID:             &bindingStr,
		RelayGrantID:          &grantStr,
		RelayGrantSecret:      &secret,
		APNsEnv:               storage.PushEnvProduction,
		BundleID:              apnspush.DefaultTopic,
		Locale:                "ru",
		InboxContractVersion:  &contractV2,
	}
	bodyBytes, _ := json.Marshal(regReq)
	recReg := httptest.NewRecorder()
	h.pushDeviceRegister(recReg, httptest.NewRequest(http.MethodPost, "/api/push/devices", bytes.NewReader(bodyBytes)).WithContext(adminContext(db, schema)))
	if recReg.Code != http.StatusOK {
		t.Fatalf("relay POST /api/push/devices status=%d body=%s", recReg.Code, recReg.Body.String())
	}
	var regResp clientapi.PushDeviceRegisterResponse
	if err := json.Unmarshal(recReg.Body.Bytes(), &regResp); err != nil {
		t.Fatal(err)
	}
	if regResp.BindingID != bindingID || !regResp.Enabled || regResp.RegistrationVersion != 1 {
		t.Fatalf("unexpected relay register response: %+v", regResp)
	}

	// Idempotent replay returns exact same binding/version.
	recReplay := httptest.NewRecorder()
	h.pushDeviceRegister(recReplay, httptest.NewRequest(http.MethodPost, "/api/push/devices", bytes.NewReader(bodyBytes)).WithContext(adminContext(db, schema)))
	if recReplay.Code != http.StatusOK {
		t.Fatalf("relay replay status=%d body=%s", recReplay.Code, recReplay.Body.String())
	}
	var replayResp clientapi.PushDeviceRegisterResponse
	if err := json.Unmarshal(recReplay.Body.Bytes(), &replayResp); err != nil {
		t.Fatal(err)
	}
	if replayResp.BindingID != bindingID || replayResp.RegistrationVersion != 1 {
		t.Fatalf("unexpected relay replay response: %+v", replayResp)
	}

	// Verify DB row stores relay fields and empty apns_token, and end-to-end delivery through RelayClient works!
	devs, err := db.ListPushDevices(ctx)
	if err != nil || len(devs) != 1 {
		t.Fatalf("ListPushDevices: len=%d err=%v", len(devs), err)
	}
	if devs[0].TransportMode != storage.PushTransportRelay || devs[0].APNsToken != "" || devs[0].RelayGrantID == nil || *devs[0].RelayGrantID != grantID {
		t.Fatalf("unexpected stored relay device: %+v", devs[0])
	}

	// Publish an evening report and deliver through mock Relay server!
	type capturedRequest struct {
		auth    string
		request apnspush.RelayDeliveryRequest
	}
	requests := make(chan capturedRequest, 1)
	mockRelay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var gotReq apnspush.RelayDeliveryRequest
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			http.Error(w, "invalid Relay request", http.StatusBadRequest)
			return
		}
		select {
		case requests <- capturedRequest{r.Header.Get("Authorization"), gotReq}:
		default:
			http.Error(w, "duplicate Relay request", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(apnspush.RelayDeliveryResult{
			Status:     http.StatusOK,
			Outcome:    apnspush.RelayOutcomeAccepted,
			APNsID:     gotReq.APNsID,
			APNsStatus: http.StatusOK,
		})
	}))
	defer mockRelay.Close()

	testRelayClient, err := apnspush.NewRelayClient(mockRelay.URL, apnspush.DefaultTopic, true)
	if err != nil {
		t.Fatalf("testRelayClient: %v", err)
	}
	key := "report:evening:2026-11-26"
	rh := storage.ReportHistory{DeliveryKey: &key, Kind: "evening", LocalDate: "2026-11-26", Timezone: "UTC", Language: "ru", PlainBody: "Evening"}
	itemID, created, err := db.PublishEveningReportToInbox(ctx, &rh, false)
	if err != nil || !created || itemID == uuid.Nil {
		t.Fatalf("PublishEveningReportToInbox: id=%s created=%v err=%v", itemID, created, err)
	}
	if err := apnspush.ProcessTenantDeliveries(ctx, db, testRelayClient, storage.NotifyConfig{}, time.Now(), 10); err != nil {
		t.Fatalf("ProcessTenantDeliveries via relay: %v", err)
	}
	select {
	case got := <-requests:
		if got.auth != "Bearer "+secret || got.request.Kind != storage.InboxKindEveningReport ||
			got.request.RegID != bindingID.String() || got.request.InboxItemID != itemID.String() {
			t.Fatalf("unexpected relay delivery request: %+v", got.request)
		}
	default:
		t.Fatal("expected a Relay delivery request")
	}
}
