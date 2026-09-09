package notify

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"health-receiver/internal/storage"
)

type wakeFeedbackBot struct {
	rows [][]InlineButton
	text string
	err  error
}

func (b *wakeFeedbackBot) SendInlineKeyboard(text string, rows [][]InlineButton) (int64, error) {
	b.text, b.rows = text, rows
	return 42, b.err
}

type durableWakeFeedbackStore struct {
	*wakeFeedbackStore
	reserved  bool
	token     uuid.UUID
	completed bool
	status    string
}

func (s *durableWakeFeedbackStore) ReserveNotificationDelivery(context.Context, string) (uuid.UUID, bool, error) {
	s.token = uuid.New()
	return s.token, s.reserved, nil
}

func (s *durableWakeFeedbackStore) CompleteNotificationDelivery(_ context.Context, _ string, token uuid.UUID, status, _ string) error {
	if token != s.token {
		return errors.New("unexpected delivery token")
	}
	s.completed = true
	s.status = status
	return nil
}

type wakeFeedbackStore struct {
	metric   *storage.DerivedMetric
	recent   bool
	saved    storage.DerivedMetricFeedback
	inserted bool
}

func (s *wakeFeedbackStore) GetDerivedMetric(_, _ string) (*storage.DerivedMetric, error) {
	return s.metric, nil
}
func (s *wakeFeedbackStore) HasRecentDerivedMetricFeedback(_, _, _ string, _ int) (bool, error) {
	return s.recent, nil
}
func (s *wakeFeedbackStore) SaveDerivedMetricFeedbackPrompted(value storage.DerivedMetricFeedback) (bool, error) {
	s.saved = value
	return s.inserted, nil
}

func TestSendWakeFeedbackPromptBuildsLocalizedButtonsAndPersists(t *testing.T) {
	loc := time.FixedZone("CEST", 2*60*60)
	wake := time.Date(2026, 8, 5, 7, 58, 0, 0, loc)
	store := &wakeFeedbackStore{
		metric: &storage.DerivedMetric{
			ValueTimestamp: &wake,
			Metadata:       json.RawMessage(`{"confidence":"medium"}`),
		},
		inserted: true,
	}
	bot := &wakeFeedbackBot{}
	sent, err := SendWakeFeedbackPrompt(bot, store, "ru", "2026-08-05", wake.Add(time.Hour))
	if err != nil || !sent {
		t.Fatalf("sent=%v err=%v", sent, err)
	}
	if bot.text != "Мы определили, что вы проснулись в 07:58. Верно?" {
		t.Fatalf("text=%q", bot.text)
	}
	if len(bot.rows) != 2 || len(bot.rows[0]) != 2 || bot.rows[1][1].CallbackData != "wake:returned_to_sleep:2026-08-05" {
		t.Fatalf("buttons=%+v", bot.rows)
	}
	if store.saved.PromptMessageID == nil || *store.saved.PromptMessageID != 42 {
		t.Fatalf("saved prompt=%+v", store.saved)
	}
}

func TestSendWakeFeedbackPromptThrottlesHighConfidence(t *testing.T) {
	wake := time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)
	store := &wakeFeedbackStore{
		metric: &storage.DerivedMetric{
			ValueTimestamp: &wake,
			Metadata:       json.RawMessage(`{"confidence":"high"}`),
		},
		recent:   true,
		inserted: true,
	}
	bot := &wakeFeedbackBot{}
	sent, err := SendWakeFeedbackPrompt(bot, store, "en", "2026-08-05", wake.Add(time.Hour))
	if err != nil || sent || bot.text != "" {
		t.Fatalf("sent=%v err=%v bot=%+v", sent, err, bot)
	}
}

func TestSendWakeFeedbackPromptHonorsDurableReservation(t *testing.T) {
	wake := time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)
	store := &durableWakeFeedbackStore{
		wakeFeedbackStore: &wakeFeedbackStore{
			metric:   &storage.DerivedMetric{ValueTimestamp: &wake},
			inserted: true,
		},
		reserved: false,
	}
	bot := &wakeFeedbackBot{}
	sent, err := SendWakeFeedbackPrompt(bot, store, "en", "2026-08-05", wake.Add(time.Hour))
	if err != nil || sent || bot.text != "" {
		t.Fatalf("sent=%v err=%v bot=%+v", sent, err, bot)
	}
}

func TestSendWakeFeedbackPromptMarksAmbiguousTransport(t *testing.T) {
	wake := time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)
	store := &durableWakeFeedbackStore{
		wakeFeedbackStore: &wakeFeedbackStore{
			metric:   &storage.DerivedMetric{ValueTimestamp: &wake},
			inserted: true,
		},
		reserved: true,
	}
	bot := &wakeFeedbackBot{err: telegramTransportError{cause: errors.New("connection reset")}}
	sent, err := SendWakeFeedbackPrompt(bot, store, "en", "2026-08-05", wake.Add(time.Hour))
	if err == nil || !sent || !store.completed || store.status != "ambiguous" {
		t.Fatalf("sent=%v err=%v completed=%v status=%q", sent, err, store.completed, store.status)
	}
}

func TestParseWakeFeedbackCallback(t *testing.T) {
	response, date, ok := parseWakeFeedbackCallback("wake:later:2026-08-05")
	if !ok || response != storage.WakeFeedbackLater || date != "2026-08-05" {
		t.Fatalf("response=%q date=%q ok=%v", response, date, ok)
	}
	for _, malformed := range []string{"wake:nope:2026-08-05", "wake:later:05-08-2026", "checkin:later:2026-08-05"} {
		if _, _, ok := parseWakeFeedbackCallback(malformed); ok {
			t.Fatalf("accepted %q", malformed)
		}
	}
}
