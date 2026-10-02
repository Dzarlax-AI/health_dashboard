package notify

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"health-receiver/internal/storage"
)

type reminderStoreFake struct {
	mu        sync.Mutex
	row       *storage.CheckinRow
	readErr   error
	keys      map[string]bool
	statuses  map[string]string
	reserveN  int
	completeN int
}

func (s *reminderStoreFake) GetTodayCheckin(date, source string) (*storage.CheckinRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readErr != nil {
		return nil, s.readErr
	}
	if s.row == nil {
		return nil, nil
	}
	copy := *s.row
	return &copy, nil
}

func (s *reminderStoreFake) ReserveNotificationDelivery(_ context.Context, key string) (uuid.UUID, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reserveN++
	if s.keys == nil {
		s.keys = make(map[string]bool)
	}
	if s.keys[key] {
		return uuid.Nil, false, nil
	}
	s.keys[key] = true
	return uuid.New(), true, nil
}

func (s *reminderStoreFake) CompleteNotificationDelivery(_ context.Context, key string, _ uuid.UUID, status, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completeN++
	if s.statuses == nil {
		s.statuses = make(map[string]string)
	}
	s.statuses[key] = status
	return nil
}

type reminderBotFake struct {
	mu    sync.Mutex
	texts []string
	rows  [][][]InlineButton
	err   error
}

func (b *reminderBotFake) SendInlineKeyboard(text string, rows [][]InlineButton) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.texts = append(b.texts, text)
	b.rows = append(b.rows, rows)
	return int64(len(b.texts) + 100), b.err
}

func TestSendCheckinReminder_AtMostOnceAndReusesKeyboard(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	prompted := time.Date(2026, 10, 1, 8, 0, 0, 0, loc)
	store := &reminderStoreFake{row: &storage.CheckinRow{
		Date: "2026-10-01", Status: storage.CheckinStatusExpired,
		PromptedAt: prompted, ExpiresAt: prompted.Add(9 * time.Minute),
	}}
	bot := &reminderBotFake{}
	now := prompted.Add(70 * time.Minute)

	buttons, _ := buildCheckinPromptButtons("ru", "2026-10-01")
	if err := SendCheckinReminder(bot, store, "ru", "2026-10-01", now); err != nil {
		t.Fatal(err)
	}
	// A new scheduler instance uses the same durable store and key.
	attempted, err := SendCheckinReminderOnce(bot, store, "ru", "2026-10-01", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if attempted {
		t.Fatal("duplicate durable reservation must not count as an attempted send")
	}
	if len(bot.texts) != 1 || len(bot.rows) != 1 {
		t.Fatalf("reminder sends=%d, want exactly one", len(bot.texts))
	}
	if bot.texts[0] == "" || !sameKeyboard(bot.rows[0], buttons) {
		t.Fatalf("reminder text or keyboard incorrect: text=%q rows=%+v", bot.texts[0], bot.rows[0])
	}
	key := "prompt:checkin_reminder:2026-10-01"
	if store.keys[key] != true || store.statuses[key] != "sent" {
		t.Fatalf("durable key state keys=%v statuses=%v", store.keys, store.statuses)
	}
}

func TestSendCheckinReminder_RechecksAndFailsClosed(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Belgrade")
	prompted := time.Date(2026, 10, 1, 18, 0, 0, 0, loc)
	now := prompted.Add(time.Hour)
	for _, tc := range []struct {
		name string
		row  *storage.CheckinRow
		err  error
	}{
		{"answered", &storage.CheckinRow{Date: "2026-10-01", Status: storage.CheckinStatusAnswered, AnsweredAt: now.Add(-time.Minute), PromptedAt: prompted}, nil},
		{"wrong date", &storage.CheckinRow{Date: "2026-09-30", Status: storage.CheckinStatusPrompted, PromptedAt: prompted}, nil},
		{"read failure", nil, errors.New("database unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &reminderStoreFake{row: tc.row, readErr: tc.err}
			bot := &reminderBotFake{}
			err := SendCheckinReminder(bot, store, "en", "2026-10-01", now)
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("err=%v want %v", err, tc.err)
			}
			if tc.err == nil && err != nil {
				t.Fatal(err)
			}
			if store.reserveN != 0 || len(bot.texts) != 0 {
				t.Fatalf("must not reserve/send: reserves=%d sends=%d", store.reserveN, len(bot.texts))
			}
		})
	}
}

func TestSendCheckinReminder_ConcurrentWorkersShareDurableGate(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Belgrade")
	prompted := time.Date(2026, 10, 1, 8, 0, 0, 0, loc)
	store := &reminderStoreFake{row: &storage.CheckinRow{Date: "2026-10-01", Status: storage.CheckinStatusPrompted, PromptedAt: prompted}}
	bot := &reminderBotFake{}
	now := prompted.Add(time.Hour)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := SendCheckinReminder(bot, store, "en", "2026-10-01", now); err != nil {
				t.Errorf("send reminder: %v", err)
			}
		}()
	}
	wg.Wait()
	if len(bot.texts) != 1 || store.completeN != 1 {
		t.Fatalf("concurrent workers duplicated reminder: sends=%d completions=%d", len(bot.texts), store.completeN)
	}
}

func TestSendCheckinReminder_AmbiguousSendIsNotRetried(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Belgrade")
	prompted := time.Date(2026, 10, 1, 8, 0, 0, 0, loc)
	store := &reminderStoreFake{row: &storage.CheckinRow{Date: "2026-10-01", Status: storage.CheckinStatusPrompted, PromptedAt: prompted}}
	bot := &reminderBotFake{err: telegramTransportError{cause: errors.New("timeout")}}
	attempted, err := SendCheckinReminderOnce(bot, store, "en", "2026-10-01", prompted.Add(time.Hour))
	if err == nil {
		t.Fatal("expected transport error")
	}
	if !attempted {
		t.Fatal("an ambiguous send attempt must suppress ancillary sends for this tick")
	}
	key := "prompt:checkin_reminder:2026-10-01"
	if store.statuses[key] != "ambiguous" {
		t.Fatalf("delivery status=%q, want ambiguous", store.statuses[key])
	}
	bot.err = nil
	if err := SendCheckinReminder(bot, store, "en", "2026-10-01", prompted.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(bot.texts) != 1 {
		t.Fatalf("ambiguous reminder retried: sends=%d", len(bot.texts))
	}
}

func sameKeyboard(a, b [][]InlineButton) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}
