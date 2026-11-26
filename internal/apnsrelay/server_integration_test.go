package apnsrelay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"health-receiver/internal/apnspush"
	"health-receiver/internal/storage"
)

type mockAPNsSender struct {
	mu       sync.Mutex
	calls    []mockAPNsCall
	handler  func(call mockAPNsCall) (apnspush.Result, error)
}

type mockAPNsCall struct {
	Env        string
	Token      string
	RequestID  string
	PushType   string
	CollapseID string
	Priority   int
	Expiration time.Time
	Payload    []byte
}

func (m *mockAPNsSender) Send(_ context.Context, environment, token, requestID, pushType, collapse string, priority int, expiration time.Time, payload []byte) (apnspush.Result, error) {
	call := mockAPNsCall{
		Env:        environment,
		Token:      token,
		RequestID:  requestID,
		PushType:   pushType,
		CollapseID: collapse,
		Priority:   priority,
		Expiration: expiration,
		Payload:    append([]byte(nil), payload...),
	}
	m.mu.Lock()
	m.calls = append(m.calls, call)
	h := m.handler
	m.mu.Unlock()
	if h != nil {
		return h(call)
	}
	return apnspush.Result{Status: 200, APNsID: requestID}, nil
}

func (m *mockAPNsSender) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockAPNsSender) lastCall() mockAPNsCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[len(m.calls)-1]
}

type otherAppPolicy struct{}

func (otherAppPolicy) ValidateDelivery(kind, locale string, _ int) error {
	if kind != "other_alert" || locale != "en" {
		return errors.New("unsupported")
	}
	return nil
}

func (otherAppPolicy) BuildPayload(kind, _ string, itemID, regID uuid.UUID) ([]byte, error) {
	return json.Marshal(map[string]any{"kind": kind, "item": itemID.String(), "reg_id": regID.String()})
}

func (otherAppPolicy) BuildCollapseID(kind string, itemID uuid.UUID) string {
	return kind + ":" + itemID.String()
}

func openRelayTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("HEALTH_DB_TESTS") != "1" {
		t.Skip("set HEALTH_DB_TESTS=1 to run PostgreSQL relay integration tests")
	}
	dsn := strings.TrimSpace(os.Getenv("HEALTH_TEST_DATABASE_URL"))
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if dsn == "" {
		t.Skip("HEALTH_TEST_DATABASE_URL or DATABASE_URL is required")
	}
	ctx := context.Background()
	schema := "relay_test_" + strings.ReplaceAll(uuid.New().String()[:8], "-", "")
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect admin db: %v", err)
	}
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		adminPool.Close()
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adminPool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		adminPool.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect schema pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type relayHarness struct {
	t            *testing.T
	srv          *Server
	handler      http.Handler
	store        *Store
	ca           *testAttestCA
	healthSender *mockAPNsSender
	otherSender  *mockAPNsSender
	nowMu        sync.Mutex
	curNow       time.Time
}

func newRelayHarness(t *testing.T) *relayHarness {
	t.Helper()
	pool := openRelayTestPool(t)
	now := time.Date(2026, 11, 26, 19, 0, 0, 0, time.UTC)
	ca := newTestAttestCA(t, now)
	cipher, err := ParseTokenCipherKeys("v1:" + strings.Repeat("aa", 32))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	h := &relayHarness{
		t:            t,
		ca:           ca,
		healthSender: &mockAPNsSender{},
		otherSender:  &mockAPNsSender{},
		curNow:       now,
	}
	nowFn := func() time.Time {
		h.nowMu.Lock()
		defer h.nowMu.Unlock()
		return h.curNow
	}
	verifier := &AppAttestVerifier{RootPool: ca.pool, Now: nowFn}
	store := NewStore(pool, cipher, verifier)
	store.now = nowFn
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	reg, err := NewRegistry(
		&AppConfig{
			AppID:            AppIDHealthSync,
			AppleAppID:       DefaultHealthAppleAppID,
			APNsTopic:        apnspush.DefaultTopic,
			AllowSandbox:     true,
			ProductionSender: h.healthSender,
			Policy:           HealthSyncPayloadPolicy{},
		},
		&AppConfig{
			AppID:            "other_app",
			AppleAppID:       "EMVJLWPLVL.com.dzarlax.other-app",
			APNsTopic:        "com.dzarlax.other-app",
			AllowSandbox:     false,
			ProductionSender: h.otherSender,
			Policy:           otherAppPolicy{},
		},
	)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	srv := NewServer(reg, store)
	srv.SetNow(nowFn)
	h.srv = srv
	h.store = store
	h.handler = srv.Routes()
	return h
}

func (h *relayHarness) advance(d time.Duration) {
	h.nowMu.Lock()
	h.curNow = h.curNow.Add(d)
	h.nowMu.Unlock()
}

func (h *relayHarness) doJSON(method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	h.t.Helper()
	var raw []byte
	if body != nil {
		switch v := body.(type) {
		case []byte:
			raw = v
		default:
			var err error
			raw, err = json.Marshal(v)
			if err != nil {
				h.t.Fatalf("marshal body: %v", err)
			}
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.RemoteAddr = "203.0.113.10:12345"
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func (h *relayHarness) challenge(appID, keyID, op string) ChallengeResponse {
	h.t.Helper()
	rec := h.doJSON(http.MethodPost, "/v1/attest/challenge", ChallengeRequest{
		AppID: appID,
		KeyID: keyID,
		Op:    op,
	}, nil)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("challenge(%s,%s) status=%d body=%s", appID, op, rec.Code, rec.Body.String())
	}
	var out ChallengeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		h.t.Fatalf("decode challenge: %v", err)
	}
	return out
}

func (h *relayHarness) registerDevice(appID, appleAppID string, dev *testDeviceKey, aaguid []byte) {
	h.t.Helper()
	ch := h.challenge(appID, "", OpAttestRegister)
	canonical := CanonicalRegisterPayload(appID, dev.keyID, ch.ChallengeID.String(), ch.Nonce)
	hash := sha256.Sum256(canonical)
	h.nowMu.Lock()
	now := h.curNow
	h.nowMu.Unlock()
	attObj := h.ca.buildAttestationObject(h.t, dev, appleAppID, hash[:], aaguid, 0, now)
	rec := h.doJSON(http.MethodPost, "/v1/attest/register", AttestRegisterRequest{
		AppID:             appID,
		KeyID:             dev.keyID,
		ChallengeID:       ch.ChallengeID.String(),
		AttestationObject: base64.StdEncoding.EncodeToString(attObj),
	}, nil)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("registerDevice status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func (h *relayHarness) signedRequest(method, path, appID, appleAppID string, dev *testDeviceKey, op string, bodyBuilder func(ch ChallengeResponse) any) *httptest.ResponseRecorder {
	h.t.Helper()
	ch := h.challenge(appID, dev.keyID, op)
	raw, err := json.Marshal(bodyBuilder(ch))
	if err != nil {
		h.t.Fatalf("marshal signed body: %v", err)
	}
	dev.signCount++
	bodyHash := sha256.Sum256(raw)
	assertion := dev.buildAssertion(h.t, appleAppID, bodyHash[:], dev.signCount)
	return h.doJSON(method, path, raw, map[string]string{
		"X-App-Attest-Key-ID":    dev.keyID,
		"X-App-Attest-Assertion": base64.StdEncoding.EncodeToString(assertion),
	})
}

func TestRelayEndToEndGrantIdempotencyBoundariesAndMultiApp(t *testing.T) {
	h := newRelayHarness(t)
	dev := newTestDeviceKey(t)
	h.registerDevice(AppIDHealthSync, DefaultHealthAppleAppID, dev, aaguidProduction)

	clientKey := uuid.New()
	regID := uuid.New()
	secret := "super-secret-delivery-token-32bytes-01"
	secretSum := sha256.Sum256([]byte(secret))
	secretHashB64 := base64.RawURLEncoding.EncodeToString(secretSum[:])
	tokA := strings.Repeat("a1", 32)

	createRec := h.signedRequest(http.MethodPost, "/v1/grants", AppIDHealthSync, DefaultHealthAppleAppID, dev, OpGrantCreate, func(ch ChallengeResponse) any {
		return CreateGrantRequest{
			AppID:                AppIDHealthSync,
			Op:                   OpGrantCreate,
			ResourceID:           clientKey.String(),
			ChallengeID:          ch.ChallengeID.String(),
			Nonce:                ch.Nonce,
			ClientGrantKey:       clientKey.String(),
			SecretHash:           secretHashB64,
			RegID:                regID.String(),
			InboxContractVersion: storage.InboxContractV2,
			APNsToken:            tokA,
			APNsEnv:              storage.PushEnvProduction,
		}
	})
	if createRec.Code != http.StatusOK {
		t.Fatalf("create grant status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	var g1 struct {
		GrantID   uuid.UUID `json:"grant_id"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	_ = json.Unmarshal(createRec.Body.Bytes(), &g1)
	if g1.GrantID == uuid.Nil {
		t.Fatal("expected non-nil grant_id")
	}

	// Retry with same client_grant_key + same secret_hash (simulating lost response) returns identical grant_id without rotating secret.
	replayRec := h.signedRequest(http.MethodPost, "/v1/grants", AppIDHealthSync, DefaultHealthAppleAppID, dev, OpGrantCreate, func(ch ChallengeResponse) any {
		return CreateGrantRequest{
			AppID:                AppIDHealthSync,
			Op:                   OpGrantCreate,
			ResourceID:           clientKey.String(),
			ChallengeID:          ch.ChallengeID.String(),
			Nonce:                ch.Nonce,
			ClientGrantKey:       clientKey.String(),
			SecretHash:           secretHashB64,
			RegID:                regID.String(),
			InboxContractVersion: storage.InboxContractV2,
			APNsToken:            tokA,
			APNsEnv:              storage.PushEnvProduction,
		}
	})
	if replayRec.Code != http.StatusOK {
		t.Fatalf("replay grant status=%d body=%s", replayRec.Code, replayRec.Body.String())
	}
	var g2 struct {
		GrantID uuid.UUID `json:"grant_id"`
	}
	_ = json.Unmarshal(replayRec.Body.Bytes(), &g2)
	if g2.GrantID != g1.GrantID {
		t.Fatalf("replay grant_id=%s, want %s", g2.GrantID, g1.GrantID)
	}

	// Delivery succeeds, builds neutral payload + opaque collapse_id without dates.
	itemID := uuid.New()
	apnsID := uuid.New()
	exp := h.curNow.Add(6 * time.Hour).Unix()
	delRec := h.doJSON(http.MethodPost, "/v1/deliveries", DeliveryRequest{
		Kind:        storage.InboxKindMorningReport,
		Locale:      "ru",
		InboxItemID: itemID.String(),
		RegID:       regID.String(),
		APNsID:      apnsID.String(),
		ExpiresAt:   exp,
	}, map[string]string{"Authorization": "Bearer " + secret})
	if delRec.Code != http.StatusOK {
		t.Fatalf("deliver status=%d body=%s", delRec.Code, delRec.Body.String())
	}
	var delResp DeliveryResponse
	_ = json.Unmarshal(delRec.Body.Bytes(), &delResp)
	if delResp.Outcome != OutcomeAccepted {
		t.Fatalf("outcome=%q, want accepted", delResp.Outcome)
	}
	call := h.healthSender.lastCall()
	if call.CollapseID != "morning_report:"+itemID.String() {
		t.Fatalf("collapse_id=%q, want opaque morning_report:%s", call.CollapseID, itemID)
	}
	if strings.Contains(call.CollapseID, "2026-") || strings.Contains(string(call.Payload), "2026-") {
		t.Fatalf("local date leaked into collapse_id or payload: %s / %s", call.CollapseID, string(call.Payload))
	}

	// Wrong reg_id is rejected (403) without calling APNs.
	beforeCalls := h.healthSender.callCount()
	badRegRec := h.doJSON(http.MethodPost, "/v1/deliveries", DeliveryRequest{
		Kind:        storage.InboxKindMorningReport,
		Locale:      "ru",
		InboxItemID: itemID.String(),
		RegID:       uuid.New().String(),
		APNsID:      uuid.New().String(),
		ExpiresAt:   exp,
	}, map[string]string{"Authorization": "Bearer " + secret})
	if badRegRec.Code != http.StatusForbidden || h.healthSender.callCount() != beforeCalls {
		t.Fatalf("expected 403 and no APNs call for reg_id mismatch, got %d", badRegRec.Code)
	}

	// Health Sync grant cannot deliver other_app's kind, and other_app sender is untouched.
	badKindRec := h.doJSON(http.MethodPost, "/v1/deliveries", DeliveryRequest{
		Kind:        "other_alert",
		Locale:      "en",
		InboxItemID: itemID.String(),
		RegID:       regID.String(),
		APNsID:      uuid.New().String(),
		ExpiresAt:   exp,
	}, map[string]string{"Authorization": "Bearer " + secret})
	if badKindRec.Code != http.StatusBadRequest || h.otherSender.callCount() != 0 {
		t.Fatalf("expected 400 and 0 calls to other_app sender, got %d calls=%d", badKindRec.Code, h.otherSender.callCount())
	}
}

func TestRelayConditionalInvalidationAndTokenRotation(t *testing.T) {
	h := newRelayHarness(t)
	dev := newTestDeviceKey(t)
	h.registerDevice(AppIDHealthSync, DefaultHealthAppleAppID, dev, aaguidProduction)

	clientKey := uuid.New()
	regID := uuid.New()
	secret := "secret-token-rotation-test-32bytes-001"
	secretSum := sha256.Sum256([]byte(secret))
	tokA := strings.Repeat("aa", 32)
	tokB := strings.Repeat("bb", 32)

	rec := h.signedRequest(http.MethodPost, "/v1/grants", AppIDHealthSync, DefaultHealthAppleAppID, dev, OpGrantCreate, func(ch ChallengeResponse) any {
		return CreateGrantRequest{
			AppID:                AppIDHealthSync,
			Op:                   OpGrantCreate,
			ResourceID:           clientKey.String(),
			ChallengeID:          ch.ChallengeID.String(),
			Nonce:                ch.Nonce,
			ClientGrantKey:       clientKey.String(),
			SecretHash:           base64.RawURLEncoding.EncodeToString(secretSum[:]),
			RegID:                regID.String(),
			InboxContractVersion: storage.InboxContractV2,
			APNsToken:            tokA,
			APNsEnv:              storage.PushEnvProduction,
		}
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create grant: %d %s", rec.Code, rec.Body.String())
	}

	// Case 1: While delivery to token A is in flight, iPhone rotates device token to B;
	// Apple returns 410 for token A -> grant MUST stay active and return 409 retryable_token_rotated.
	h.healthSender.handler = func(call mockAPNsCall) (apnspush.Result, error) {
		if call.Token == tokA {
			h.advance(10 * time.Second)
			rotRec := h.signedRequest(http.MethodPut, "/v1/attest/token", AppIDHealthSync, DefaultHealthAppleAppID, dev, OpDeviceUpdateToken, func(ch ChallengeResponse) any {
				return UpdateDeviceTokenRequest{
					AppID:       AppIDHealthSync,
					Op:          OpDeviceUpdateToken,
					ResourceID:  dev.keyID,
					ChallengeID: ch.ChallengeID.String(),
					Nonce:       ch.Nonce,
					APNsToken:   tokB,
					APNsEnv:     storage.PushEnvProduction,
				}
			})
			if rotRec.Code != http.StatusOK {
				t.Errorf("rotate token failed: %d %s", rotRec.Code, rotRec.Body.String())
			}
			ts := h.curNow.Add(-5 * time.Second)
			return apnspush.Result{Status: 410, Reason: "Unregistered", Timestamp: &ts}, nil
		}
		return apnspush.Result{Status: 200, APNsID: call.RequestID}, nil
	}

	delRec := h.doJSON(http.MethodPost, "/v1/deliveries", DeliveryRequest{
		Kind:        storage.InboxKindMorningReport,
		Locale:      "en",
		InboxItemID: uuid.New().String(),
		RegID:       regID.String(),
		APNsID:      uuid.New().String(),
		ExpiresAt:   h.curNow.Add(6 * time.Hour).Unix(),
	}, map[string]string{"Authorization": "Bearer " + secret})
	var delResp DeliveryResponse
	_ = json.Unmarshal(delRec.Body.Bytes(), &delResp)
	if delRec.Code != http.StatusConflict || delResp.Outcome != OutcomeRetryableTokenRotated {
		t.Fatalf("expected 409 retryable_token_rotated, got %d %+v", delRec.Code, delResp)
	}

	// Subsequent delivery succeeds on token B!
	delRec2 := h.doJSON(http.MethodPost, "/v1/deliveries", DeliveryRequest{
		Kind:        storage.InboxKindMorningReport,
		Locale:      "en",
		InboxItemID: uuid.New().String(),
		RegID:       regID.String(),
		APNsID:      uuid.New().String(),
		ExpiresAt:   h.curNow.Add(6 * time.Hour).Unix(),
	}, map[string]string{"Authorization": "Bearer " + secret})
	if delRec2.Code != http.StatusOK || h.healthSender.lastCall().Token != tokB {
		t.Fatalf("expected 200 on rotated token B, got %d token=%s", delRec2.Code, h.healthSender.lastCall().Token)
	}

	// Case 2: 410 with Apple timestamp OLDER than token_updated_at on same token B ->
	// MUST NOT invalidate grant and MUST return outcome="rejected" (not retryable_token_rotated).
	h.healthSender.handler = func(call mockAPNsCall) (apnspush.Result, error) {
		staleTS := h.curNow.Add(-5 * time.Minute)
		return apnspush.Result{Status: 410, Reason: "Unregistered", Timestamp: &staleTS}, nil
	}
	staleRec := h.doJSON(http.MethodPost, "/v1/deliveries", DeliveryRequest{
		Kind:        storage.InboxKindMorningReport,
		Locale:      "en",
		InboxItemID: uuid.New().String(),
		RegID:       regID.String(),
		APNsID:      uuid.New().String(),
		ExpiresAt:   h.curNow.Add(6 * time.Hour).Unix(),
	}, map[string]string{"Authorization": "Bearer " + secret})
	var staleResp DeliveryResponse
	_ = json.Unmarshal(staleRec.Body.Bytes(), &staleResp)
	if staleRec.Code != http.StatusConflict || staleResp.Outcome != OutcomeRejected {
		t.Fatalf("expected 409 rejected for stale 410 timestamp, got %d %+v", staleRec.Code, staleResp)
	}

	// Case 3: 400 BadDeviceToken (no timestamp) matching current token B -> invalidates grant (410 grant_invalid).
	h.healthSender.handler = func(call mockAPNsCall) (apnspush.Result, error) {
		return apnspush.Result{Status: 400, Reason: "BadDeviceToken"}, nil
	}
	invRec := h.doJSON(http.MethodPost, "/v1/deliveries", DeliveryRequest{
		Kind:        storage.InboxKindMorningReport,
		Locale:      "en",
		InboxItemID: uuid.New().String(),
		RegID:       regID.String(),
		APNsID:      uuid.New().String(),
		ExpiresAt:   h.curNow.Add(6 * time.Hour).Unix(),
	}, map[string]string{"Authorization": "Bearer " + secret})
	var invResp DeliveryResponse
	_ = json.Unmarshal(invRec.Body.Bytes(), &invResp)
	if invRec.Code != http.StatusGone || invResp.Outcome != OutcomeGrantInvalid {
		t.Fatalf("expected 410 grant_invalid on matching 400 BadDeviceToken, got %d %+v", invRec.Code, invResp)
	}

	// Subsequent delivery must be rejected by Relay BEFORE touching APNs.
	before := h.healthSender.callCount()
	afterRec := h.doJSON(http.MethodPost, "/v1/deliveries", DeliveryRequest{
		Kind:        storage.InboxKindMorningReport,
		Locale:      "en",
		InboxItemID: uuid.New().String(),
		RegID:       regID.String(),
		APNsID:      uuid.New().String(),
		ExpiresAt:   h.curNow.Add(6 * time.Hour).Unix(),
	}, map[string]string{"Authorization": "Bearer " + secret})
	if afterRec.Code != http.StatusGone || h.healthSender.callCount() != before {
		t.Fatalf("expected 410 without APNs call after invalidation, got %d calls=%d", afterRec.Code, h.healthSender.callCount()-before)
	}

	// Verify invalidated grant returns 410 Gone on refresh and is absent from PUT /v1/attest/token active_grant_ids.
	var createdGrant struct {
		GrantID uuid.UUID `json:"grant_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &createdGrant)
	refRec := h.signedRequest(http.MethodPut, "/v1/grants/"+createdGrant.GrantID.String()+"/refresh", AppIDHealthSync, DefaultHealthAppleAppID, dev, OpGrantRefresh, func(ch ChallengeResponse) any {
		return RefreshGrantRequest{
			AppID:       AppIDHealthSync,
			Op:          OpGrantRefresh,
			ResourceID:  createdGrant.GrantID.String(),
			ChallengeID: ch.ChallengeID.String(),
			Nonce:       ch.Nonce,
		}
	})
	if refRec.Code != http.StatusGone {
		t.Fatalf("expected 410 Gone refreshing invalidated grant, got %d %s", refRec.Code, refRec.Body.String())
	}
	rotAfterInv := h.signedRequest(http.MethodPut, "/v1/attest/token", AppIDHealthSync, DefaultHealthAppleAppID, dev, OpDeviceUpdateToken, func(ch ChallengeResponse) any {
		return UpdateDeviceTokenRequest{
			AppID:       AppIDHealthSync,
			Op:          OpDeviceUpdateToken,
			ResourceID:  dev.keyID,
			ChallengeID: ch.ChallengeID.String(),
			Nonce:       ch.Nonce,
			APNsToken:   strings.Repeat("ff", 32),
			APNsEnv:     storage.PushEnvProduction,
		}
	})
	if rotAfterInv.Code != http.StatusOK {
		t.Fatalf("rotate after invalidation status=%d %s", rotAfterInv.Code, rotAfterInv.Body.String())
	}
	var rotResp struct {
		Updated        int      `json:"updated"`
		ActiveGrantIDs []string `json:"active_grant_ids"`
	}
	_ = json.Unmarshal(rotAfterInv.Body.Bytes(), &rotResp)
	if rotResp.Updated != 0 || len(rotResp.ActiveGrantIDs) != 0 {
		t.Fatalf("expected updated=0 and empty active_grant_ids after invalidation, got %+v", rotResp)
	}
}

func TestRelayAmbiguousVsRetryableOutcomes(t *testing.T) {
	h := newRelayHarness(t)
	dev := newTestDeviceKey(t)
	h.registerDevice(AppIDHealthSync, DefaultHealthAppleAppID, dev, aaguidProduction)

	clientKey := uuid.New()
	regID := uuid.New()
	secret := "secret-outcomes-test-32bytes-0000001"
	secretSum := sha256.Sum256([]byte(secret))
	rec := h.signedRequest(http.MethodPost, "/v1/grants", AppIDHealthSync, DefaultHealthAppleAppID, dev, OpGrantCreate, func(ch ChallengeResponse) any {
		return CreateGrantRequest{
			AppID:                AppIDHealthSync,
			Op:                   OpGrantCreate,
			ResourceID:           clientKey.String(),
			ChallengeID:          ch.ChallengeID.String(),
			Nonce:                ch.Nonce,
			ClientGrantKey:       clientKey.String(),
			SecretHash:           base64.RawURLEncoding.EncodeToString(secretSum[:]),
			RegID:                regID.String(),
			InboxContractVersion: storage.InboxContractV2,
			APNsToken:            strings.Repeat("cc", 32),
			APNsEnv:              storage.PushEnvProduction,
		}
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create grant: %d", rec.Code)
	}

	deliver := func() (int, DeliveryResponse) {
		r := h.doJSON(http.MethodPost, "/v1/deliveries", DeliveryRequest{
			Kind:        storage.InboxKindEveningReport,
			Locale:      "en",
			InboxItemID: uuid.New().String(),
			RegID:       regID.String(),
			APNsID:      uuid.New().String(),
			ExpiresAt:   h.curNow.Add(6 * time.Hour).Unix(),
		}, map[string]string{"Authorization": "Bearer " + secret})
		var out DeliveryResponse
		_ = json.Unmarshal(r.Body.Bytes(), &out)
		return r.Code, out
	}

	// 1. Transport error between Relay and Apple -> 504 ambiguous (never retryable).
	h.healthSender.handler = func(_ mockAPNsCall) (apnspush.Result, error) {
		return apnspush.Result{}, errors.New("APNs transport outcome unknown")
	}
	code, resp := deliver()
	if code != http.StatusGatewayTimeout || resp.Outcome != OutcomeAmbiguous {
		t.Fatalf("expected 504 ambiguous, got %d %+v", code, resp)
	}

	// 2. Confirmed 500 from Apple -> 502 retryable_apns.
	h.healthSender.handler = func(_ mockAPNsCall) (apnspush.Result, error) {
		return apnspush.Result{Status: 500, Reason: "InternalServerError"}, nil
	}
	code, resp = deliver()
	if code != http.StatusBadGateway || resp.Outcome != OutcomeRetryableAPNs || resp.APNsStatus != 500 {
		t.Fatalf("expected 502 retryable_apns(500), got %d %+v", code, resp)
	}

	// 3. Confirmed 429 from Apple -> 429 retryable_apns.
	h.healthSender.handler = func(_ mockAPNsCall) (apnspush.Result, error) {
		return apnspush.Result{Status: 429, Reason: "TooManyRequests", RetryAfter: 90 * time.Second}, nil
	}
	code, resp = deliver()
	if code != http.StatusTooManyRequests || resp.Outcome != OutcomeRetryableAPNs || resp.RetryAfterSeconds != 90 {
		t.Fatalf("expected 429 retryable_apns(90s), got %d %+v", code, resp)
	}
}

func TestRelayCancelByClientKeySurvivesCleanupForeverWhileKeyLives(t *testing.T) {
	h := newRelayHarness(t)
	dev := newTestDeviceKey(t)
	h.registerDevice(AppIDHealthSync, DefaultHealthAppleAppID, dev, aaguidProduction)

	// Cancel a client_grant_key BEFORE any grant is created (e.g. lost POST /v1/grants or racing disable).
	cancelledKey := uuid.New()
	cancelRec := h.signedRequest(http.MethodDelete, "/v1/grants/by-client-key/"+cancelledKey.String(), AppIDHealthSync, DefaultHealthAppleAppID, dev, OpGrantCancelByClientKey, func(ch ChallengeResponse) any {
		return RevokeGrantRequest{
			AppID:       AppIDHealthSync,
			Op:          OpGrantCancelByClientKey,
			ResourceID:  cancelledKey.String(),
			ChallengeID: ch.ChallengeID.String(),
			Nonce:       ch.Nonce,
		}
	})
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("cancel by client key status=%d body=%s", cancelRec.Code, cancelRec.Body.String())
	}

	// Advance clock by 45 days (> 30d cleanup retention) and run Cleanup():
	// cancelled_client_grant_keys MUST survive for the lifetime of attest_keys!
	h.advance(45 * 24 * time.Hour)
	if err := h.store.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	secretSum := sha256.Sum256([]byte("should-never-resurrect-secret-32bytes"))
	resurrectRec := h.signedRequest(http.MethodPost, "/v1/grants", AppIDHealthSync, DefaultHealthAppleAppID, dev, OpGrantCreate, func(ch ChallengeResponse) any {
		return CreateGrantRequest{
			AppID:                AppIDHealthSync,
			Op:                   OpGrantCreate,
			ResourceID:           cancelledKey.String(),
			ChallengeID:          ch.ChallengeID.String(),
			Nonce:                ch.Nonce,
			ClientGrantKey:       cancelledKey.String(),
			SecretHash:           base64.RawURLEncoding.EncodeToString(secretSum[:]),
			RegID:                uuid.New().String(),
			InboxContractVersion: storage.InboxContractV2,
			APNsToken:            strings.Repeat("dd", 32),
			APNsEnv:              storage.PushEnvProduction,
		}
	})
	if resurrectRec.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for cancelled client_grant_key after 45d cleanup, got %d %s", resurrectRec.Code, resurrectRec.Body.String())
	}

	// Also verify cancelling an ALREADY-created grant via client_grant_key immediately revokes delivery.
	activeKey := uuid.New()
	regID := uuid.New()
	secret2 := fmt.Sprintf("secret-cancel-existing-%s", activeKey.String())
	s2Hash := sha256.Sum256([]byte(secret2))
	createRec := h.signedRequest(http.MethodPost, "/v1/grants", AppIDHealthSync, DefaultHealthAppleAppID, dev, OpGrantCreate, func(ch ChallengeResponse) any {
		return CreateGrantRequest{
			AppID:                AppIDHealthSync,
			Op:                   OpGrantCreate,
			ResourceID:           activeKey.String(),
			ChallengeID:          ch.ChallengeID.String(),
			Nonce:                ch.Nonce,
			ClientGrantKey:       activeKey.String(),
			SecretHash:           base64.RawURLEncoding.EncodeToString(s2Hash[:]),
			RegID:                regID.String(),
			InboxContractVersion: storage.InboxContractV2,
			APNsToken:            strings.Repeat("ee", 32),
			APNsEnv:              storage.PushEnvProduction,
		}
	})
	if createRec.Code != http.StatusOK {
		t.Fatalf("create grant 2: %d %s", createRec.Code, createRec.Body.String())
	}
	cancelRec2 := h.signedRequest(http.MethodDelete, "/v1/grants/by-client-key/"+activeKey.String(), AppIDHealthSync, DefaultHealthAppleAppID, dev, OpGrantCancelByClientKey, func(ch ChallengeResponse) any {
		return RevokeGrantRequest{
			AppID:       AppIDHealthSync,
			Op:          OpGrantCancelByClientKey,
			ResourceID:  activeKey.String(),
			ChallengeID: ch.ChallengeID.String(),
			Nonce:       ch.Nonce,
		}
	})
	if cancelRec2.Code != http.StatusOK {
		t.Fatalf("cancel existing grant by client key: %d %s", cancelRec2.Code, cancelRec2.Body.String())
	}
	delRec := h.doJSON(http.MethodPost, "/v1/deliveries", DeliveryRequest{
		Kind:        storage.InboxKindMorningReport,
		Locale:      "en",
		InboxItemID: uuid.New().String(),
		RegID:       regID.String(),
		APNsID:      uuid.New().String(),
		ExpiresAt:   h.curNow.Add(6 * time.Hour).Unix(),
	}, map[string]string{"Authorization": "Bearer " + secret2})
	if delRec.Code != http.StatusGone {
		t.Fatalf("expected 410 GrantRevoked after cancel by client key, got %d %s", delRec.Code, delRec.Body.String())
	}
}

func TestRelayCreateGrantReplayUpdatesRotatedAPNsToken(t *testing.T) {
	h := newRelayHarness(t)
	dev := newTestDeviceKey(t)
	h.registerDevice(AppIDHealthSync, DefaultHealthAppleAppID, dev, aaguidProduction)

	clientKey := uuid.New()
	regID := uuid.New()
	secret := "secret-replay-token-rotate-32bytes-01"
	secretSum := sha256.Sum256([]byte(secret))
	secretHashB64 := base64.RawURLEncoding.EncodeToString(secretSum[:])
	tokA := strings.Repeat("1a", 32)
	tokB := strings.Repeat("2b", 32)

	// 1. Initial POST /v1/grants succeeds on Relay with token A (response lost on client).
	rec1 := h.signedRequest(http.MethodPost, "/v1/grants", AppIDHealthSync, DefaultHealthAppleAppID, dev, OpGrantCreate, func(ch ChallengeResponse) any {
		return CreateGrantRequest{
			AppID:                AppIDHealthSync,
			Op:                   OpGrantCreate,
			ResourceID:           clientKey.String(),
			ChallengeID:          ch.ChallengeID.String(),
			Nonce:                ch.Nonce,
			ClientGrantKey:       clientKey.String(),
			SecretHash:           secretHashB64,
			RegID:                regID.String(),
			InboxContractVersion: storage.InboxContractV2,
			APNsToken:            tokA,
			APNsEnv:              storage.PushEnvProduction,
		}
	})
	if rec1.Code != http.StatusOK {
		t.Fatalf("initial grant status=%d body=%s", rec1.Code, rec1.Body.String())
	}
	var g1 struct {
		GrantID   uuid.UUID `json:"grant_id"`
		TokenHash string    `json:"token_hash"`
	}
	_ = json.Unmarshal(rec1.Body.Bytes(), &g1)

	// 2. Device rotates APNs token A -> B before retrying POST /v1/grants with the same client_grant_key.
	h.advance(5 * time.Second)
	rec2 := h.signedRequest(http.MethodPost, "/v1/grants", AppIDHealthSync, DefaultHealthAppleAppID, dev, OpGrantCreate, func(ch ChallengeResponse) any {
		return CreateGrantRequest{
			AppID:                AppIDHealthSync,
			Op:                   OpGrantCreate,
			ResourceID:           clientKey.String(),
			ChallengeID:          ch.ChallengeID.String(),
			Nonce:                ch.Nonce,
			ClientGrantKey:       clientKey.String(),
			SecretHash:           secretHashB64,
			RegID:                regID.String(),
			InboxContractVersion: storage.InboxContractV2,
			APNsToken:            tokB,
			APNsEnv:              storage.PushEnvProduction,
		}
	})
	if rec2.Code != http.StatusOK {
		t.Fatalf("replay grant with rotated token status=%d body=%s", rec2.Code, rec2.Body.String())
	}
	var g2 struct {
		GrantID   uuid.UUID `json:"grant_id"`
		TokenHash string    `json:"token_hash"`
	}
	_ = json.Unmarshal(rec2.Body.Bytes(), &g2)
	tokBSum := sha256.Sum256([]byte(tokB))
	wantTokBHex := fmt.Sprintf("%x", tokBSum[:])
	if g2.GrantID != g1.GrantID || g2.TokenHash != wantTokBHex {
		t.Fatalf("replay grant mismatch: got id=%s hash=%q, want id=%s hash=%q", g2.GrantID, g2.TokenHash, g1.GrantID, wantTokBHex)
	}

	// 3. Delivery MUST go to rotated token B, never stale token A!
	delRec := h.doJSON(http.MethodPost, "/v1/deliveries", DeliveryRequest{
		Kind:        storage.InboxKindMorningReport,
		Locale:      "en",
		InboxItemID: uuid.New().String(),
		RegID:       regID.String(),
		APNsID:      uuid.New().String(),
		ExpiresAt:   h.curNow.Add(6 * time.Hour).Unix(),
	}, map[string]string{"Authorization": "Bearer " + secret})
	if delRec.Code != http.StatusOK {
		t.Fatalf("deliver after replay token rotation status=%d body=%s", delRec.Code, delRec.Body.String())
	}
	if gotTok := h.healthSender.lastCall().Token; gotTok != tokB {
		t.Fatalf("expected delivery to rotated token B %q, got stale token %q", tokB, gotTok)
	}
}
