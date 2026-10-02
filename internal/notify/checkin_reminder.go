package notify

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"health-receiver/internal/storage"
)

// CheckinReminderStore combines a fresh check-in read with durable delivery
// reservation. Reminder sends fail closed when durable delivery is absent.
type CheckinReminderStore interface {
	GetTodayCheckin(date, source string) (*storage.CheckinRow, error)
	ReserveNotificationDelivery(context.Context, string) (uuid.UUID, bool, error)
	CompleteNotificationDelivery(context.Context, string, uuid.UUID, string, string) error
}

// SendCheckinReminder re-reads the row before attempting the durable unique
// delivery key. Callers serialize this with answer callbacks and initial
// prompts using their per-tenant check-in mutex.
func SendCheckinReminder(bot CheckinBot, store CheckinReminderStore, lang, date string, now time.Time) error {
	_, err := SendCheckinReminderOnce(bot, store, lang, date, now)
	return err
}

// SendCheckinReminderOnce reports whether it reached the external send call.
// A true result suppresses other ancillary prompts for this scheduler tick,
// even when Telegram returns an ambiguous error.
func SendCheckinReminderOnce(bot CheckinBot, store CheckinReminderStore, lang, date string, now time.Time) (attempted bool, err error) {
	if bot == nil || store == nil || date == "" {
		return false, errors.New("check-in reminder requires bot, store, and date")
	}
	row, err := store.GetTodayCheckin(date, storage.CheckinSourceTelegram)
	if err != nil {
		return false, err
	}
	if row == nil || row.Date != date || !CheckinReminderDue(row, now, now.Location()) {
		return false, nil
	}

	key := "prompt:checkin_reminder:" + date
	ctx, cancel := context.WithTimeout(context.Background(), notificationDeliveryTimeout)
	token, reserved, err := store.ReserveNotificationDelivery(ctx, key)
	cancel()
	if err != nil || !reserved {
		return false, err
	}

	buttons, _ := buildCheckinPromptButtons(lang, date)
	msgID, sendErr := bot.SendInlineKeyboard(checkinReminderText(lang), buttons)
	_ = msgID // The reminder reuses the original answer row and message id.
	if sendErr != nil {
		status, code := deliveryFailureStatus(sendErr)
		return true, errors.Join(sendErr, completeNotificationDelivery(store, key, token, status, code))
	}
	return true, completeNotificationDelivery(store, key, token, "sent", "")
}

func checkinReminderText(lang string) string {
	switch lang {
	case "ru":
		return "Как вы себя чувствуете? Можно ответить одним нажатием. Утренний отчёт не зависит от ответа."
	case "sr":
		return "Kako se osećate? Možete odgovoriti jednim dodirom. Jutarnji izveštaj ne zavisi od odgovora."
	default:
		return "How are you feeling? You can answer with one tap. The morning report doesn’t depend on your answer."
	}
}
