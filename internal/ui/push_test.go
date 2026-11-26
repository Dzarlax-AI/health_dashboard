package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	clientapi "health-receiver/internal/api"
	"health-receiver/internal/apnspush"
	"health-receiver/internal/storage"
	"health-receiver/internal/testdb"
)

func TestPushDeviceEndpointsGateBindingDeleteAndIOSPushChannel(t *testing.T) {
	t.Setenv("IN_APP_INBOX_ENABLED", "")
	t.Setenv("APNS_PUSH_ENABLED", "")

	db, schema, cleanup := testTenantDB(t)
	defer cleanup()
	ctx := context.Background()
	if err := db.MigrateSchemaContractContext(ctx); err != nil {
		t.Fatal(err)
	}

	dsn := testdb.DSN(t)
	bootstrap, err := testdb.NewPool(ctx, dsn, "")
	if err != nil {
		t.Fatal(err)
	}
	defer bootstrap.Close()
	otherSchema := testdb.SchemaName("push_other")
	if err := testdb.CreateSchema(ctx, bootstrap, otherSchema); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = testdb.DropSchema(context.Background(), bootstrap, otherSchema) }()
	otherPool, err := testdb.NewPool(ctx, dsn, otherSchema)
	if err != nil {
		t.Fatal(err)
	}
	otherDB := storage.NewFromPool(otherPool)
	defer otherDB.Close()
	if err := otherDB.MigrateSchemaContractContext(ctx); err != nil {
		t.Fatal(err)
	}

	h := &Handler{}
	h.ConfigureAPNs(&apnspush.Sender{Topic: apnspush.DefaultTopic})

	devID := uuid.New()
	tok := strings.Repeat("f", 64)
	validBody := func(token string) string {
		raw, _ := json.Marshal(clientapi.PushDeviceRegisterRequest{
			DeviceID:  devID.String(),
			APNsToken: token,
			APNsEnv:   storage.PushEnvSandbox,
			BundleID:  apnspush.DefaultTopic,
			Locale:    "ru",
		})
		return string(raw)
	}

	// 1. Disabled by default -> 404 on register
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/push/devices", bytes.NewBufferString(validBody(tok))).WithContext(adminContext(db, schema))
	h.pushDeviceRegister(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("disabled register status=%d, want 404", w.Code)
	}

	// Enable Inbox + APNs
	if err := db.SaveSettings(map[string]string{
		"timezone":                       "UTC",
		storage.SettingInAppInboxEnabled: "true",
		storage.SettingAPNSPushEnabled:   "true",
	}); err != nil {
		t.Fatal(err)
	}

	// 2. Rejects mismatched bundle_id with 400
	badBundle, _ := json.Marshal(clientapi.PushDeviceRegisterRequest{
		DeviceID:  devID.String(),
		APNsToken: tok,
		APNsEnv:   storage.PushEnvSandbox,
		BundleID:  "com.evil.app",
		Locale:    "en",
	})
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/push/devices", bytes.NewReader(badBundle)).WithContext(adminContext(db, schema))
	h.pushDeviceRegister(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad bundle_id status=%d, want 400", w.Code)
	}

	// 3. Valid registration -> 200 with binding_id
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/push/devices", bytes.NewBufferString(validBody(tok))).WithContext(adminContext(db, schema))
	h.pushDeviceRegister(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", w.Code, w.Body.String())
	}
	var reg clientapi.PushDeviceRegisterResponse
	if err := json.Unmarshal(w.Body.Bytes(), &reg); err != nil || reg.BindingID == uuid.Nil || !reg.Enabled || reg.RegistrationVersion != 1 {
		t.Fatalf("unexpected register response: %+v err=%v", reg, err)
	}

	// 4. Foreign tenant DELETE cannot disable this tenant's device
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodDelete, "/api/push/devices/"+devID.String()+"?binding_id="+reg.BindingID.String(), nil).WithContext(adminContext(otherDB, otherSchema))
	r.SetPathValue("device_id", devID.String())
	h.pushDeviceDelete(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("foreign delete status=%d, want 204", w.Code)
	}
	devs, err := db.ListPushDevices(ctx)
	if err != nil || len(devs) != 1 || !devs[0].Enabled {
		t.Fatalf("foreign tenant must not affect this tenant's device: %+v err=%v", devs, err)
	}

	// Attempt cancellation is scoped to the authenticated tenant even when that
	// tenant has the same device and attempt IDs.
	attemptID, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	attemptText := attemptID.String()
	var request clientapi.PushDeviceRegisterRequest
	if err := json.Unmarshal([]byte(validBody(tok)), &request); err != nil {
		t.Fatal(err)
	}
	request.RegistrationAttemptID = &attemptText
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/push/devices", bytes.NewReader(data)).WithContext(adminContext(db, schema))
	h.pushDeviceRegister(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("attempt registration status=%d body=%s", w.Code, w.Body.String())
	}
	attemptPath := "/api/push/devices/" + devID.String() + "?registration_attempt_id=" + attemptText
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodDelete, attemptPath, nil).WithContext(adminContext(otherDB, otherSchema))
	r.SetPathValue("device_id", devID.String())
	h.pushDeviceDelete(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("foreign attempt cancellation status=%d, want 204", w.Code)
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/push/devices", bytes.NewReader(data)).WithContext(adminContext(db, schema))
	h.pushDeviceRegister(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("foreign tenant cannot cancel this tenant's attempt: status=%d", w.Code)
	}

	// 5. Disable APNs flag, then send DELETE with exact binding_id -> MUST still succeed (204) and disable binding!
	if err := db.SaveSettings(map[string]string{storage.SettingAPNSPushEnabled: "false"}); err != nil {
		t.Fatal(err)
	}
	// Wrong binding_id leaves device active
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodDelete, "/api/push/devices/"+devID.String()+"?binding_id="+uuid.NewString(), nil).WithContext(adminContext(db, schema))
	r.SetPathValue("device_id", devID.String())
	h.pushDeviceDelete(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("wrong binding delete status=%d, want 204", w.Code)
	}
	devs, _ = db.ListPushDevices(ctx)
	if !devs[0].Enabled {
		t.Fatal("wrong binding_id must not disable active device")
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodDelete, attemptPath+"&binding_id="+reg.BindingID.String(), nil).WithContext(adminContext(db, schema))
	r.SetPathValue("device_id", devID.String())
	h.pushDeviceDelete(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("delete with both selectors status=%d, want 400", w.Code)
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodDelete, attemptPath, nil).WithContext(adminContext(db, schema))
	r.SetPathValue("device_id", devID.String())
	h.pushDeviceDelete(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("attempt cancellation with APNs disabled status=%d, want 204", w.Code)
	}
	devs, err = db.ListPushDevices(ctx)
	if err != nil || len(devs) != 1 || devs[0].Enabled {
		t.Fatalf("attempt cancellation must disable the device with APNs disabled (err=%v)", err)
	}

	// Exact binding_id disables even with apns_push_enabled=false
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodDelete, "/api/push/devices/"+devID.String()+"?binding_id="+reg.BindingID.String(), nil).WithContext(adminContext(db, schema))
	r.SetPathValue("device_id", devID.String())
	h.pushDeviceDelete(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete with flag off status=%d, want 204", w.Code)
	}
	devs, _ = db.ListPushDevices(ctx)
	if devs[0].Enabled {
		t.Fatal("DELETE with matching binding_id must disable device even when apns_push_enabled=false")
	}

	// 6. POST /api/prompts/{id}/answer accepts channel=ios_push and repeat from web returns already_answered
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	promptID, _, _, err := db.EnsureCanonicalCheckinPrompt(ctx, today, now.Add(-5*time.Minute), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/prompts/"+promptID.String()+"/answer", bytes.NewBufferString(`{"answer":"ok","channel":"ios_push"}`)).WithContext(adminContext(db, schema))
	r.SetPathValue("prompt_id", promptID.String())
	h.promptAnswer(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("ios_push answer status=%d body=%s", w.Code, w.Body.String())
	}
	var ans1 clientapi.PromptAnswerResponse
	_ = json.Unmarshal(w.Body.Bytes(), &ans1)
	if ans1.Status != storage.CheckinAnswerStatusSaved || ans1.Answer != storage.CheckinAnswerOK {
		t.Fatalf("ans1=%+v", ans1)
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/prompts/"+promptID.String()+"/answer", bytes.NewBufferString(`{"answer":"sick","channel":"web"}`)).WithContext(adminContext(db, schema))
	r.SetPathValue("prompt_id", promptID.String())
	h.promptAnswer(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("web repeat status=%d body=%s", w.Code, w.Body.String())
	}
	var ans2 clientapi.PromptAnswerResponse
	_ = json.Unmarshal(w.Body.Bytes(), &ans2)
	if ans2.Status != storage.CheckinAnswerStatusAlreadyAnswered || ans2.Answer != storage.CheckinAnswerOK {
		t.Fatalf("ans2=%+v", ans2)
	}
}
