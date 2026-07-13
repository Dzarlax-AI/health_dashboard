package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

// CheckinCallbackPrefix marks an inline-keyboard callback as a
// subjective-checkin answer. Telegram caps callback_data at 64 bytes;
// "checkin:<answer>:<YYYY-MM-DD>" is 26 bytes worst case.
const CheckinCallbackPrefix = "checkin"

// buildCheckinPromptButtons returns the 2x2 inline keyboard and the
// localised prompt text for the given lang. `date` is embedded in each
// callback_data so the webhook can validate the user is answering
// today's prompt, not a stale one from a chat scrollback.
func buildCheckinPromptButtons(lang, date string) ([][]InlineButton, string) {
	t := health.GetStrings(lang)
	row1 := []InlineButton{
		{Text: t["checkin_btn_great"], CallbackData: fmt.Sprintf("%s:%s:%s", CheckinCallbackPrefix, storage.CheckinAnswerGreat, date)},
		{Text: t["checkin_btn_ok"], CallbackData: fmt.Sprintf("%s:%s:%s", CheckinCallbackPrefix, storage.CheckinAnswerOK, date)},
	}
	row2 := []InlineButton{
		{Text: t["checkin_btn_meh"], CallbackData: fmt.Sprintf("%s:%s:%s", CheckinCallbackPrefix, storage.CheckinAnswerMeh, date)},
		{Text: t["checkin_btn_sick"], CallbackData: fmt.Sprintf("%s:%s:%s", CheckinCallbackPrefix, storage.CheckinAnswerSick, date)},
	}
	return [][]InlineButton{row1, row2}, t["checkin_prompt_text"]
}

// parseCheckinCallback returns (answer, date, ok) for a callback
// payload of the form `checkin:<answer>:<YYYY-MM-DD>`. ok=false on any
// deviation: unknown prefix, unknown answer, missing date.
func parseCheckinCallback(payload string) (string, string, bool) {
	parts := strings.Split(payload, ":")
	if len(parts) != 3 || parts[0] != CheckinCallbackPrefix {
		return "", "", false
	}
	if err := storage.ValidateCheckinAnswer(parts[1]); err != nil {
		return "", "", false
	}
	if parts[2] == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// CheckinBot abstracts the subset of *Bot that SendCheckinPrompt needs.
// Keeps the test boundary thin.
type CheckinBot interface {
	SendInlineKeyboard(text string, rows [][]InlineButton) (int64, error)
}

// CheckinStore abstracts the subset of *storage.DB the prompt path
// needs.
type CheckinStore interface {
	SaveCheckinPrompted(date, source string, msgID int64, promptedAt, expiresAt time.Time) error
}

// SendCheckinPrompt builds the 2x2 inline keyboard, POSTs it to
// Telegram, and persists the resulting message_id in a `prompted` row.
// expiresAt is the morning-cap time — once the wall clock passes it,
// the row gets transitioned to `expired` by the scheduler and the
// morning report sends with a soft note.
//
// Store write is skipped when the Telegram send fails so we don't
// claim "we prompted them" when no message ever arrived.
func SendCheckinPrompt(bot CheckinBot, store CheckinStore, lang, date string, now, expiresAt time.Time) error {
	rows, text := buildCheckinPromptButtons(lang, date)
	var delivery notificationDeliveryStore
	var token uuid.UUID
	if candidate, ok := store.(notificationDeliveryStore); ok {
		delivery = candidate
		var reserved bool
		var err error
		ctx, cancel := context.WithTimeout(context.Background(), notificationDeliveryTimeout)
		token, reserved, err = delivery.ReserveNotificationDelivery(ctx, "prompt:checkin:"+date)
		cancel()
		if err != nil || !reserved {
			return err
		}
	}
	msgID, err := bot.SendInlineKeyboard(text, rows)
	if err != nil {
		if delivery != nil {
			status, code := deliveryFailureStatus(err)
			_ = completeNotificationDelivery(delivery, "prompt:checkin:"+date, token, status, code)
		}
		return err
	}
	if err = store.SaveCheckinPrompted(date, storage.CheckinSourceTelegram, msgID, now, expiresAt); err != nil {
		if delivery != nil {
			return errors.Join(err, completeNotificationDelivery(delivery, "prompt:checkin:"+date, token, "sent", ""))
		}
		return err
	}
	if delivery != nil {
		return completeNotificationDelivery(delivery, "prompt:checkin:"+date, token, "sent", "")
	}
	return nil
}
