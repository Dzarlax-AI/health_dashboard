package registry

import (
	"strings"
	"testing"
	"time"
)

// parseWebhookStatus is the load-bearing safe parser. Its only job is to
// return a usable WebhookStatus on ANY input, never to crash startup or
// the runtime path because someone hand-edited global_settings.value in
// psql.
func TestParseWebhookStatus_MalformedYieldsUnknown(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty string", ""},
		{"random text", "not-json-at-all"},
		{"partial JSON missing brace", `{"state":"ok"`},
		{"valid JSON but missing state", `{"reason":"x","updated_at":"2026-05-18T12:00:00Z"}`},
		{"unknown state value", `{"state":"bogus_state"}`},
		{"json null", `null`},
		{"json array", `["ok"]`},
		{"binary garbage", "\x00\x01\x02"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := parseWebhookStatus(tc.raw)
			if err == nil {
				t.Errorf("expected parse error for %q, got nil", tc.raw)
			}
			if s.State != StateUnknown {
				t.Errorf("malformed input should yield State=unknown, got %q", s.State)
			}
		})
	}
}

func TestParseWebhookStatus_HappyPath(t *testing.T) {
	raw := `{"state":"ok","reason":"","updated_at":"2026-05-18T12:00:00Z"}`
	s, err := parseWebhookStatus(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.State != StateOK {
		t.Errorf("State = %q, want ok", s.State)
	}
	if !s.UpdatedAt.Equal(time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("UpdatedAt = %v, want 2026-05-18T12:00:00Z", s.UpdatedAt)
	}
}

func TestParseWebhookStatus_FailedWithReason(t *testing.T) {
	raw := `{"state":"failed","reason":"401 unauthorized","updated_at":"2026-05-18T12:00:00Z"}`
	s, err := parseWebhookStatus(raw)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if s.State != StateFailed || s.Reason != "401 unauthorized" {
		t.Errorf("got state=%q reason=%q", s.State, s.Reason)
	}
}

func TestSerialiseWebhookStatus_RoundTrip(t *testing.T) {
	want := WebhookStatus{
		State:     StateOK,
		Reason:    "",
		UpdatedAt: time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	}
	raw, err := serialiseWebhookStatus(want)
	if err != nil {
		t.Fatalf("serialise: %v", err)
	}
	got, err := parseWebhookStatus(raw)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if got.State != want.State || got.Reason != want.Reason || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("round-trip drifted: got %+v want %+v", got, want)
	}
}

// classifyPendingRows is the pure decision used by ResetPendingOnStartup.
// Load-bearing: a single malformed value must not poison the entire pass.
// Listing this test in the regression set explicitly so reviewers see the
// contract — startup never crashes on bad JSON.
func TestClassifyPendingRows_SurvivesMalformedRows(t *testing.T) {
	rows := []webhookStatusRow{
		{Key: "webhook_status_health", Value: `{"state":"pending","reason":"","updated_at":"2026-05-18T12:00:00Z"}`},
		{Key: "webhook_status_health_mariia", Value: `{"state":"pending","reason":"","updated_at":"2026-05-18T12:01:00Z"}`},
		{Key: "webhook_status_corrupt", Value: "not-json-at-all"},
		{Key: "webhook_status_ok_tenant", Value: `{"state":"ok","updated_at":"2026-05-18T11:00:00Z"}`},
		{Key: "webhook_status_failed_tenant", Value: `{"state":"failed","reason":"timeout","updated_at":"2026-05-18T11:30:00Z"}`},
	}
	toReset, skipped := classifyPendingRows(rows)
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the corrupt row)", skipped)
	}
	if len(toReset) != 2 {
		t.Fatalf("toReset len = %d, want 2; got %v", len(toReset), toReset)
	}
	// Order-independent membership check.
	resetSet := map[string]bool{}
	for _, k := range toReset {
		resetSet[k] = true
	}
	if !resetSet["webhook_status_health"] || !resetSet["webhook_status_health_mariia"] {
		t.Errorf("toReset missing expected keys: %v", toReset)
	}
	if resetSet["webhook_status_corrupt"] {
		t.Error("corrupt row must NOT be in toReset — leave it alone")
	}
	if resetSet["webhook_status_ok_tenant"] || resetSet["webhook_status_failed_tenant"] {
		t.Error("non-pending rows must NOT be in toReset")
	}
}

func TestClassifyPendingRows_AllOK(t *testing.T) {
	rows := []webhookStatusRow{
		{Key: "webhook_status_health", Value: `{"state":"ok","updated_at":"2026-05-18T12:00:00Z"}`},
	}
	toReset, skipped := classifyPendingRows(rows)
	if len(toReset) != 0 || skipped != 0 {
		t.Errorf("got toReset=%v skipped=%d, want both zero", toReset, skipped)
	}
}

func TestWebhookStatusKeyForSchema(t *testing.T) {
	got := webhookStatusKey("health")
	if !strings.HasPrefix(got, "webhook_status_") || !strings.HasSuffix(got, "health") {
		t.Errorf("key %q doesn't follow webhook_status_<schema> shape", got)
	}
	if webhookStatusKey("") != "" {
		t.Error("empty schema should yield empty key, not webhook_status_")
	}
}
