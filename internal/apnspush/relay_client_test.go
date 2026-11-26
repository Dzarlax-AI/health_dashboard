package apnspush

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"health-receiver/internal/storage"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestValidateRelayURLSSRFProtection(t *testing.T) {
	valid := []struct {
		in   string
		want string
	}{
		{"https://push.dzarlax.dev", "https://push.dzarlax.dev"},
		{"https://PUSH.DZARLAX.DEV/", "https://push.dzarlax.dev"},
		{"https://push.dzarlax.dev:8443", "https://push.dzarlax.dev:8443"},
	}
	for _, tc := range valid {
		got, err := ValidateRelayURL(tc.in, false)
		if err != nil || got != tc.want {
			t.Fatalf("ValidateRelayURL(%q) = (%q, %v), want (%q, nil)", tc.in, got, err, tc.want)
		}
	}
	invalid := []string{
		"",
		"http://push.dzarlax.dev",
		"https://localhost:8081",
		"https://127.0.0.1:8081",
		"https://10.0.0.5",
		"https://192.168.1.10",
		"https://169.254.169.254",
		"https://[::1]",
		"https://user:pass@push.dzarlax.dev",
		"https://push.dzarlax.dev/custom/path",
		"https://push.dzarlax.dev?foo=bar",
	}
	for _, raw := range invalid {
		if got, err := ValidateRelayURL(raw, false); err == nil {
			t.Fatalf("expected ValidateRelayURL(%q) to fail, got %q", raw, got)
		}
	}
}

func TestRelayClientForbidsRedirectsAndClassifiesAmbiguous504(t *testing.T) {
	redirectTargetCalled := false
	client, err := NewRelayClient("https://push.dzarlax.dev", DefaultTopic, false)
	if err != nil {
		t.Fatalf("NewRelayClient: %v", err)
	}
	client.Client = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host == "evil.example.com" {
				redirectTargetCalled = true
				rec := httptest.NewRecorder()
				rec.WriteHeader(http.StatusOK)
				return rec.Result(), nil
			}
			rec := httptest.NewRecorder()
			switch r.Header.Get("Authorization") {
			case "Bearer redirect-secret-123456":
				rec.Header().Set("Location", "https://evil.example.com/steal")
				rec.WriteHeader(http.StatusTemporaryRedirect)
			case "Bearer timeout-secret-123456":
				rec.Header().Set("Content-Type", "application/json")
				rec.WriteHeader(http.StatusGatewayTimeout)
				_ = json.NewEncoder(rec).Encode(RelayDeliveryResult{
					Status:  http.StatusGatewayTimeout,
					Outcome: RelayOutcomeAmbiguous,
					Reason:  "apns_transport_ambiguous",
				})
			default:
				rec.Header().Set("Content-Type", "application/json")
				rec.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(rec).Encode(RelayDeliveryResult{
					Status:     http.StatusOK,
					Outcome:    RelayOutcomeAccepted,
					APNsID:     uuid.New().String(),
					APNsStatus: http.StatusOK,
				})
			}
			return rec.Result(), nil
		}),
	}

	req := RelayDeliveryRequest{
		Kind:        storage.InboxKindMorningReport,
		Locale:      "en",
		InboxItemID: uuid.New().String(),
		RegID:       uuid.New().String(),
		APNsID:      uuid.New().String(),
		ExpiresAt:   time.Now().Add(time.Hour).Unix(),
	}

	// 1. Redirect must be refused without forwarding Authorization header.
	req.GrantSecret = "redirect-secret-123456"
	if _, err := client.SendRelay(context.Background(), req); err == nil {
		t.Fatal("expected redirect to fail")
	}
	if redirectTargetCalled {
		t.Fatal("redirect target was called; Bearer secret could have leaked")
	}

	// 2. 504 ambiguous returns error so worker marks delivery ambiguous without retry.
	req.GrantSecret = "timeout-secret-123456"
	res, err := client.SendRelay(context.Background(), req)
	if err == nil || res.Outcome != RelayOutcomeAmbiguous {
		t.Fatalf("expected ambiguous error for 504, got res=%+v err=%v", res, err)
	}

	// 3. 200 accepted succeeds.
	req.GrantSecret = "ok-secret-1234567890"
	res, err = client.SendRelay(context.Background(), req)
	if err != nil || res.Outcome != RelayOutcomeAccepted {
		t.Fatalf("expected accepted, got res=%+v err=%v", res, err)
	}
}

func TestClassifyRelayOutcomeMatrix(t *testing.T) {
	now := time.Date(2026, 11, 26, 19, 0, 0, 0, time.UTC)
	d := &storage.ClaimedPushDelivery{
		ID:                  uuid.New(),
		LeaseToken:          uuid.New(),
		DeviceID:            uuid.New(),
		RegistrationVersion: 3,
		TokenHash:           []byte("hash"),
		Attempts:            1,
		ExpiresAt:           now.Add(6 * time.Hour),
	}
	attemptID := uuid.New()

	// 1. 504 ambiguous -> ambiguous (no retry, no invalidation).
	amb := classifyRelayOutcome(d, attemptID, RelayDeliveryResult{Status: 504, Outcome: RelayOutcomeAmbiguous, Reason: "apns_transport_ambiguous"}, nil, now)
	if amb.DeliveryState != storage.PushDeliveryStateAmbiguous || amb.InvalidateDevice {
		t.Fatalf("ambiguous outcome mismatch: %+v", amb)
	}

	// 2. grant_invalid -> rejected + InvalidateDevice.
	inv := classifyRelayOutcome(d, attemptID, RelayDeliveryResult{Status: 410, Outcome: RelayOutcomeGrantInvalid, Reason: "GrantRevoked"}, nil, now)
	if inv.DeliveryState != storage.PushDeliveryStateRejected || !inv.InvalidateDevice {
		t.Fatalf("grant_invalid outcome mismatch: %+v", inv)
	}

	// 3. retryable_token_rotated -> retry_wait (30s) WITHOUT InvalidateDevice.
	rot := classifyRelayOutcome(d, attemptID, RelayDeliveryResult{Status: 409, Outcome: RelayOutcomeRetryableTokenRotated, Reason: "TokenRotatedDuringDelivery", RetryAfter: 30 * time.Second}, nil, now)
	if rot.DeliveryState != storage.PushDeliveryStateRetryWait || rot.InvalidateDevice || rot.RetryAfter != 30*time.Second {
		t.Fatalf("retryable_token_rotated outcome mismatch: %+v", rot)
	}

	// 4. retryable_apns with 500 -> >=15m Apple 5xx backoff.
	apns500 := classifyRelayOutcome(d, attemptID, RelayDeliveryResult{Status: 502, Outcome: RelayOutcomeRetryableAPNs, APNsStatus: 500}, nil, now)
	if apns500.DeliveryState != storage.PushDeliveryStateRetryWait || apns500.RetryAfter < 15*time.Minute || apns500.InvalidateDevice {
		t.Fatalf("retryable_apns 500 backoff mismatch: %+v", apns500)
	}

	// The relay's daily quota can require waiting beyond the provider's 429 cap.
	d.ExpiresAt = now.Add(5 * time.Hour)
	quota := classifyRelayOutcome(d, attemptID, RelayDeliveryResult{
		Status: 429, Outcome: RelayOutcomeRetryablePreSend, RetryAfter: 4 * time.Hour,
	}, nil, now)
	if quota.DeliveryState != storage.PushDeliveryStateRetryWait || quota.RetryAfter != 4*time.Hour {
		t.Fatalf("unexpected Relay quota retry: %+v", quota)
	}

	// 5. rejected (e.g. stale 410 timestamp) -> rejected WITHOUT InvalidateDevice.
	rej := classifyRelayOutcome(d, attemptID, RelayDeliveryResult{Status: 409, Outcome: RelayOutcomeRejected, Reason: "StaleAPNsInvalidationTimestamp"}, nil, now)
	if rej.DeliveryState != storage.PushDeliveryStateRejected || rej.InvalidateDevice {
		t.Fatalf("rejected outcome mismatch: %+v", rej)
	}
}
