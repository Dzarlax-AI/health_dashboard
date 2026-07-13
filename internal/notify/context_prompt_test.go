package notify

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"health-receiver/internal/storage"
)

func TestParseContextPromptCallback(t *testing.T) {
	promptID, category, ok := parseContextPromptCallback("ctx:cp_abcdef:travel")
	if !ok {
		t.Fatal("callback should parse")
	}
	if promptID != "cp_abcdef" || category != storage.ContextPromptCategoryTravel {
		t.Fatalf("got prompt=%q category=%q", promptID, category)
	}
	for _, payload := range []string{
		"",
		"checkin:great:2026-06-20",
		"ctx::travel",
		"ctx:cp_abcdef:illness",
		"ctx:cp_abcdef:travel:2026-06-20",
	} {
		if _, _, ok := parseContextPromptCallback(payload); ok {
			t.Fatalf("payload %q should be rejected", payload)
		}
	}
}

func TestBuildContextPromptButtons_UsesOpaqueCallbackData(t *testing.T) {
	prompt := storage.ContextPromptInteraction{
		PromptID: "cp_abcdef",
		AllowedCategories: []string{
			storage.ContextPromptCategoryPoorSleep,
			storage.ContextPromptCategoryStress,
			storage.ContextPromptCategoryTravel,
			storage.ContextPromptCategoryUnknown,
			storage.ContextPromptCategorySkip,
		},
	}
	rows, text := buildContextPromptButtons("en", prompt)
	if text == "" {
		t.Fatal("prompt text should be localized")
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for _, row := range rows {
		for _, btn := range row {
			if len(btn.CallbackData) > 64 {
				t.Fatalf("callback too long: %q (%d)", btn.CallbackData, len(btn.CallbackData))
			}
			if !strings.HasPrefix(btn.CallbackData, "ctx:cp_abcdef:") {
				t.Fatalf("callback should be opaque prompt id + choice code, got %q", btn.CallbackData)
			}
			if strings.Contains(btn.CallbackData, "2026") || strings.Contains(btn.CallbackData, "low_sleep") || strings.Contains(btn.CallbackData, "context") {
				t.Fatalf("callback leaks semantic context: %q", btn.CallbackData)
			}
		}
	}
}

type fakeDurableContextPromptStore struct {
	markSentErr error
	token       uuid.UUID
	completed   bool
	completion  string
}

func (s *fakeDurableContextPromptStore) MarkContextPromptSent(string, int64, time.Time) error {
	return s.markSentErr
}

func (s *fakeDurableContextPromptStore) MarkContextPromptSendFailed(string, time.Time) error {
	return nil
}

func (s *fakeDurableContextPromptStore) ReserveNotificationDelivery(context.Context, string) (uuid.UUID, bool, error) {
	s.token = uuid.New()
	return s.token, true, nil
}

func (s *fakeDurableContextPromptStore) CompleteNotificationDelivery(_ context.Context, _ string, token uuid.UUID, status, _ string) error {
	if token != s.token {
		return errors.New("unexpected delivery token")
	}
	s.completed = true
	s.completion = status
	return nil
}

func TestSendContextPrompt_MarkSentFailureCompletesDeliveryAsSent(t *testing.T) {
	markErr := errors.New("mark context prompt sent")
	store := &fakeDurableContextPromptStore{markSentErr: markErr}
	bot := &fakeBot{msgID: 73}
	prompt := &storage.ContextPromptInteraction{
		PromptID:          "cp_abcdef",
		AllowedCategories: []string{storage.ContextPromptCategoryStress},
	}

	err := SendContextPrompt(bot, store, "en", prompt, time.Now())
	if !errors.Is(err, markErr) {
		t.Fatalf("send error = %v, want original persistence error", err)
	}
	if !store.completed || store.completion != "sent" {
		t.Fatalf("successful external send left delivery dangling: completed=%v status=%q", store.completed, store.completion)
	}
}
