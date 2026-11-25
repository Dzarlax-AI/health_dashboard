package apnspush

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"health-receiver/internal/storage"
	"health-receiver/internal/testdb"
)

func testECKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	return k, string(pemBytes)
}

func TestParseCredentialAndSignJWT(t *testing.T) {
	_, pemStr := testECKey(t)
	raw, _ := json.Marshal(Credential{
		KeyID:      "ABC1234567",
		TeamID:     "EMVJLWPLVL",
		PrivateKey: pemStr,
	})
	cred, key, err := ParseCredential(raw)
	if err != nil || cred.KeyID != "ABC1234567" || key == nil {
		t.Fatalf("ParseCredential failed: cred=%+v err=%v", cred, err)
	}
	jwtStr, err := Sign(cred, key, time.Unix(1700000000, 0).UTC())
	if err != nil || strings.Count(jwtStr, ".") != 2 {
		t.Fatalf("Sign failed: jwt=%q err=%v", jwtStr, err)
	}

	// Reject malformed IDs and duplicate JSON keys.
	badRaw := `{"key_id":"short","team_id":"EMVJLWPLVL","private_key":"bad"}`
	if _, _, err := ParseCredential([]byte(badRaw)); !errors.Is(err, ErrCredential) {
		t.Fatalf("expected ErrCredential for short key_id, got %v", err)
	}
	dupRaw := `{"key_id":"ABC1234567","key_id":"ABC1234567","team_id":"EMVJLWPLVL","private_key":"x"}`
	if _, _, err := ParseCredential([]byte(dupRaw)); !errors.Is(err, ErrCredential) {
		t.Fatalf("expected ErrCredential for duplicate JSON key, got %v", err)
	}
}

func TestNeutralPayloadsLeakNoDatesOrHealthMetrics(t *testing.T) {
	itemID := uuid.New()
	bindingID := uuid.New()
	for _, lang := range []string{"en", "ru", "sr"} {
		mr, err := BuildMorningReportPayload(lang, itemID, bindingID)
		if err != nil {
			t.Fatalf("morning report payload (%s): %v", lang, err)
		}
		var mrObj map[string]any
		if err := json.Unmarshal(mr, &mrObj); err != nil {
			t.Fatal(err)
		}
		if mrObj["kind"] != storage.InboxKindMorningReport || mrObj["inbox_item_id"] != itemID.String() || mrObj["reg_id"] != bindingID.String() {
			t.Fatalf("unexpected morning payload (%s): %s", lang, string(mr))
		}
		if _, hasDate := mrObj["date"]; hasDate {
			t.Fatalf("morning payload must not contain date: %s", string(mr))
		}

		ci, err := BuildCheckinPayload(lang, itemID, bindingID)
		if err != nil {
			t.Fatalf("checkin payload (%s): %v", lang, err)
		}
		var ciObj map[string]any
		if err := json.Unmarshal(ci, &ciObj); err != nil {
			t.Fatal(err)
		}
		aps := ciObj["aps"].(map[string]any)
		if aps["category"] != CategoryCheckinMorning || ciObj["kind"] != storage.InboxKindPromptCheckin || ciObj["prompt_id"] != itemID.String() || ciObj["reg_id"] != bindingID.String() {
			t.Fatalf("unexpected checkin payload (%s): %s", lang, string(ci))
		}
	}
}

func TestSenderHTTP2ResponsesAnd410Timestamp(t *testing.T) {
	key, pemStr := testECKey(t)
	appleMillis := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC).UnixMilli()
	var gotTopic, gotCollapse, gotPushType string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTopic = r.Header.Get("apns-topic")
		gotCollapse = r.Header.Get("apns-collapse-id")
		gotPushType = r.Header.Get("apns-push-type")
		w.Header().Set("apns-id", r.Header.Get("apns-id"))
		switch {
		case strings.HasSuffix(r.URL.Path, strings.Repeat("a", 64)):
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, strings.Repeat("b", 64)):
			w.WriteHeader(http.StatusGone)
			_ = json.NewEncoder(w).Encode(map[string]any{"reason": "Unregistered", "timestamp": appleMillis})
		case strings.HasSuffix(r.URL.Path, strings.Repeat("c", 64)):
			w.Header().Set("retry-after", "1200")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"reason": "ServiceUnavailable"})
		}
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	sender := &Sender{
		Topic:          DefaultTopic,
		SandboxBaseURL: srv.URL,
		Client:         srv.Client(),
		Credential:     Credential{KeyID: "ABC1234567", TeamID: "EMVJLWPLVL", PrivateKey: pemStr},
		Key:            key,
	}

	resOK, err := sender.Send(context.Background(), storage.PushEnvSandbox, strings.Repeat("a", 64), uuid.NewString(), "alert", "mr-test", 10, time.Now().Add(time.Hour), []byte(`{"aps":{}}`))
	if err != nil || resOK.Status != 200 || gotTopic != DefaultTopic || gotCollapse != "mr-test" || gotPushType != "alert" {
		t.Fatalf("200 send failed: res=%+v err=%v headers=(%s,%s,%s)", resOK, err, gotTopic, gotCollapse, gotPushType)
	}

	res410, err := sender.Send(context.Background(), storage.PushEnvSandbox, strings.Repeat("b", 64), uuid.NewString(), "alert", "mr-test", 10, time.Now().Add(time.Hour), []byte(`{"aps":{}}`))
	if err != nil || res410.Status != 410 || res410.Reason != "Unregistered" || res410.Timestamp == nil || res410.Timestamp.UnixMilli() != appleMillis {
		t.Fatalf("410 send failed: res=%+v err=%v", res410, err)
	}

	res503, err := sender.Send(context.Background(), storage.PushEnvSandbox, strings.Repeat("c", 64), uuid.NewString(), "alert", "mr-test", 10, time.Now().Add(time.Hour), []byte(`{"aps":{}}`))
	if err != nil || res503.Status != 503 || res503.RetryAfter != 1200*time.Second {
		t.Fatalf("503 send failed: res=%+v err=%v", res503, err)
	}
}

func TestClassifyBackoff5xxMinimum15Minutes(t *testing.T) {
	now := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	d := &storage.ClaimedPushDelivery{
		ID:         uuid.New(),
		LeaseToken: uuid.New(),
		Attempts:   1,
		ExpiresAt:  now.Add(6 * time.Hour),
	}
	out := classifyAPNsOutcome(d, uuid.New(), Result{Status: 503, Reason: "ServiceUnavailable"}, nil, now)
	if out.DeliveryState != storage.PushDeliveryStateRetryWait || out.RetryAfter < 15*time.Minute {
		t.Fatalf("5xx retry delay = %v (state=%s), want >= 15m retry_wait", out.RetryAfter, out.DeliveryState)
	}
}

type mockTransport struct {
	mu      sync.Mutex
	calls   int
	handler func(token string, payload []byte) (Result, error)
}

func (m *mockTransport) Send(_ context.Context, _, token, _, _, _ string, _ int, _ time.Time, payload []byte) (Result, error) {
	m.mu.Lock()
	m.calls++
	h := m.handler
	m.mu.Unlock()
	return h(token, payload)
}

func setupPushWorkerTestDB(t *testing.T, prefix string) (*storage.DB, func()) {
	t.Helper()
	dsn := testdb.DSN(t)
	ctx := context.Background()
	bootstrap, err := testdb.NewPool(ctx, dsn, "")
	if err != nil {
		t.Fatal(err)
	}
	schema := testdb.SchemaName(prefix)
	if err := testdb.CreateSchema(ctx, bootstrap, schema); err != nil {
		bootstrap.Close()
		t.Fatal(err)
	}
	pool, err := testdb.NewPool(ctx, dsn, schema)
	if err != nil {
		_ = testdb.DropSchema(ctx, bootstrap, schema)
		bootstrap.Close()
		t.Fatal(err)
	}
	db := storage.NewFromPool(pool)
	if err := db.MigrateSchemaContractContext(ctx); err != nil {
		db.Close()
		_ = testdb.DropSchema(ctx, bootstrap, schema)
		bootstrap.Close()
		t.Fatal(err)
	}
	return db, func() {
		db.Close()
		_ = testdb.DropSchema(context.Background(), bootstrap, schema)
		bootstrap.Close()
	}
}

func TestWorkerDeliversSkipsAnsweredAndClassifiesTransportErrors(t *testing.T) {
	t.Setenv("IN_APP_INBOX_ENABLED", "")
	t.Setenv("APNS_PUSH_ENABLED", "")
	db, cleanup := setupPushWorkerTestDB(t, "apns_worker")
	defer cleanup()
	ctx := context.Background()

	if err := db.SaveSettings(map[string]string{
		"timezone":                       "UTC",
		"report_lang":                    "en",
		storage.SettingInAppInboxEnabled: "true",
		storage.SettingAPNSPushEnabled:   "true",
	}); err != nil {
		t.Fatal(err)
	}
	tok := strings.Repeat("d", 64)
	dev, err := db.UpsertPushDevice(ctx, storage.UpsertPushDeviceInput{
		DeviceID:  uuid.New(),
		APNsToken: tok,
		APNsEnv:   storage.PushEnvProduction,
		BundleID:  DefaultTopic,
		Locale:    "ru",
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	today := now.Format("2006-01-02")

	// 1. Morning report -> accepted (200) with reg_id == dev.BindingID.
	key := "report:morning:" + today
	if _, _, err := db.PublishMorningReportToInbox(ctx, &storage.ReportHistory{
		DeliveryKey: &key, Kind: "morning", LocalDate: today, Timezone: "UTC", Language: "ru", PlainBody: "Sleep: 8h",
	}, false); err != nil {
		t.Fatal(err)
	}
	var sentPayload []byte
	mock := &mockTransport{handler: func(_ string, payload []byte) (Result, error) {
		sentPayload = append([]byte(nil), payload...)
		return Result{Status: 200}, nil
	}}
	if err := ProcessTenantDeliveries(ctx, db, mock, storage.NotifyConfig{Timezone: "UTC"}, now, 20); err != nil {
		t.Fatal(err)
	}
	if mock.calls != 1 {
		t.Fatalf("expected 1 APNs send, got %d", mock.calls)
	}
	if !strings.Contains(string(sentPayload), dev.BindingID.String()) || strings.Contains(string(sentPayload), "Sleep: 8h") {
		t.Fatalf("payload privacy/binding violation: %s", string(sentPayload))
	}

	// 2. Check-in answered BEFORE worker runs -> must expire without calling APNs!
	promptID, _, _, err := db.EnsureCanonicalCheckinPrompt(ctx, today, now, now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AnswerCheckinByPromptID(ctx, promptID, today, storage.CheckinChannelWeb, storage.CheckinAnswerGreat, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := ProcessTenantDeliveries(ctx, db, mock, storage.NotifyConfig{Timezone: "UTC"}, now.Add(2*time.Minute), 20); err != nil {
		t.Fatal(err)
	}
	if mock.calls != 1 {
		t.Fatalf("already-answered check-in must NOT call APNs: calls=%d", mock.calls)
	}

	// 3. Transport error on next day's morning report -> ambiguous, never retried.
	tomorrow := now.AddDate(0, 0, 1).Format("2006-01-02")
	key2 := "report:morning:" + tomorrow
	if _, _, err := db.PublishMorningReportToInbox(ctx, &storage.ReportHistory{
		DeliveryKey: &key2, Kind: "morning", LocalDate: tomorrow, Timezone: "UTC", Language: "en", PlainBody: "Tomorrow",
	}, false); err != nil {
		t.Fatal(err)
	}
	mockErr := &mockTransport{handler: func(_ string, _ []byte) (Result, error) {
		return Result{}, errors.New("connection reset")
	}}
	if err := ProcessTenantDeliveries(ctx, db, mockErr, storage.NotifyConfig{Timezone: "UTC"}, now, 20); err != nil {
		t.Fatal(err)
	}
	if err := ProcessTenantDeliveries(ctx, db, mockErr, storage.NotifyConfig{Timezone: "UTC"}, now.Add(time.Minute), 20); err != nil {
		t.Fatal(err)
	}
	if mockErr.calls != 1 {
		t.Fatalf("ambiguous transport error must never retry blindly: calls=%d", mockErr.calls)
	}
}
