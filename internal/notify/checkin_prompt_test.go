package notify

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"health-receiver/internal/storage"
)

func TestCheckinPromptButtons(t *testing.T) {
	rows, _ := buildCheckinPromptButtons("ru", "2026-05-18")
	if len(rows) != 2 || len(rows[0]) != 2 || len(rows[1]) != 2 {
		t.Fatalf("expected 2x2 keyboard, got %d rows", len(rows))
	}
	want := []string{
		"checkin:great:2026-05-18",
		"checkin:ok:2026-05-18",
		"checkin:meh:2026-05-18",
		"checkin:sick:2026-05-18",
	}
	got := []string{
		rows[0][0].CallbackData,
		rows[0][1].CallbackData,
		rows[1][0].CallbackData,
		rows[1][1].CallbackData,
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("button %d callback: got %q want %q", i, got[i], want[i])
		}
	}
	// At least the first button text should differ between en and ru.
	rowsEN, _ := buildCheckinPromptButtons("en", "2026-05-18")
	if rowsEN[0][0].Text == rows[0][0].Text {
		t.Errorf("en and ru labels collided: %q", rowsEN[0][0].Text)
	}
}

func TestParseCheckinCallback(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		answer string
		date   string
		ok     bool
	}{
		{"valid great", "checkin:great:2026-05-18", "great", "2026-05-18", true},
		{"valid sick", "checkin:sick:2026-01-01", "sick", "2026-01-01", true},
		{"wrong prefix", "ping:great:2026-05-18", "", "", false},
		{"bad answer", "checkin:wonderful:2026-05-18", "", "", false},
		{"missing date", "checkin:great", "", "", false},
		{"empty", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answer, date, ok := parseCheckinCallback(tc.input)
			if ok != tc.ok || answer != tc.answer || date != tc.date {
				t.Fatalf("got (%q, %q, %v), want (%q, %q, %v)", answer, date, ok, tc.answer, tc.date, tc.ok)
			}
		})
	}
}

// SendCheckinPrompt orchestration tests use fakes.

type fakeBot struct {
	lastText  string
	lastRows  [][]InlineButton
	msgID     int64
	sendErr   error
	answerErr error
}

func (f *fakeBot) SendInlineKeyboard(text string, rows [][]InlineButton) (int64, error) {
	f.lastText = text
	f.lastRows = rows
	return f.msgID, f.sendErr
}
func (f *fakeBot) AnswerCallbackQuery(qid, text string) error { return f.answerErr }

type fakeCheckinStore struct {
	saved    bool
	saveErr  error
	lastDate string
	lastSrc  string
	lastMsg  int64
	lastExp  time.Time
}

func (s *fakeCheckinStore) SaveCheckinPrompted(date, source string, msgID int64, promptedAt, expiresAt time.Time) error {
	s.saved = true
	s.lastDate = date
	s.lastSrc = source
	s.lastMsg = msgID
	s.lastExp = expiresAt
	return s.saveErr
}

type fakeDurableCheckinStore struct {
	fakeCheckinStore
	reserved     bool
	token        uuid.UUID
	completed    bool
	completion   string
	completeCode string
	completeErr  error
}

func (s *fakeDurableCheckinStore) ReserveNotificationDelivery(context.Context, string) (uuid.UUID, bool, error) {
	s.reserved = true
	s.token = uuid.New()
	return s.token, true, nil
}

func (s *fakeDurableCheckinStore) CompleteNotificationDelivery(_ context.Context, _ string, token uuid.UUID, status, code string) error {
	if token != s.token {
		return errors.New("unexpected delivery token")
	}
	s.completed = true
	s.completion = status
	s.completeCode = code
	return s.completeErr
}

func TestSendCheckinPrompt_HappyPath(t *testing.T) {
	bot := &fakeBot{msgID: 42}
	store := &fakeCheckinStore{}
	now := time.Date(2026, 5, 18, 8, 0, 0, 0, time.UTC)
	cap := time.Date(2026, 5, 18, 11, 0, 0, 0, time.UTC)
	if err := SendCheckinPrompt(bot, store, "ru", "2026-05-18", now, cap); err != nil {
		t.Fatalf("send: %v", err)
	}
	if bot.lastText == "" || len(bot.lastRows) != 2 {
		t.Errorf("bot not called correctly: text=%q rows=%v", bot.lastText, bot.lastRows)
	}
	if !store.saved {
		t.Fatalf("store not invoked")
	}
	if store.lastMsg != 42 || !store.lastExp.Equal(cap) || store.lastSrc != storage.CheckinSourceTelegram {
		t.Errorf("store payload off: %+v", store)
	}
}

func TestSendCheckinPrompt_TelegramErrSkipsStore(t *testing.T) {
	bot := &fakeBot{sendErr: errors.New("boom")}
	store := &fakeCheckinStore{}
	now := time.Date(2026, 5, 18, 8, 0, 0, 0, time.UTC)
	err := SendCheckinPrompt(bot, store, "ru", "2026-05-18", now, now.Add(3*time.Hour))
	if err == nil {
		t.Fatal("expected error")
	}
	if store.saved {
		t.Fatal("store must not be written when Telegram send fails")
	}
}

func TestSendCheckinPrompt_SaveFailureCompletesDeliveryAsSent(t *testing.T) {
	saveErr := errors.New("save prompted row")
	bot := &fakeBot{msgID: 42}
	store := &fakeDurableCheckinStore{fakeCheckinStore: fakeCheckinStore{saveErr: saveErr}}
	now := time.Date(2026, 5, 18, 8, 0, 0, 0, time.UTC)

	err := SendCheckinPrompt(bot, store, "en", "2026-05-18", now, now.Add(3*time.Hour))
	if !errors.Is(err, saveErr) {
		t.Fatalf("send error = %v, want original persistence error", err)
	}
	if !store.reserved || !store.completed || store.completion != "sent" {
		t.Fatalf("successful external send left delivery dangling: reserved=%v completed=%v status=%q", store.reserved, store.completed, store.completion)
	}
}
