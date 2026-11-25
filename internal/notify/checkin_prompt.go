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

// CheckinOption is a single localized answer choice in canonical server order.
type CheckinOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// CheckinPromptOptions returns the localized title and canonical ordered
// answer options shared across Telegram, iOS, and Web.
func CheckinPromptOptions(lang string) (string, []CheckinOption) {
	t := health.GetStrings(lang)
	return t["checkin_prompt_text"], []CheckinOption{
		{ID: storage.CheckinAnswerGreat, Label: t["checkin_btn_great"]},
		{ID: storage.CheckinAnswerOK, Label: t["checkin_btn_ok"]},
		{ID: storage.CheckinAnswerMeh, Label: t["checkin_btn_meh"]},
		{ID: storage.CheckinAnswerSick, Label: t["checkin_btn_sick"]},
	}
}

// buildCheckinPromptButtons returns the 2x2 inline keyboard and the
// localised prompt text for the given lang. `date` is embedded in each
// callback_data so the webhook can validate the user is answering
// today's prompt, not a stale one from a chat scrollback.
func buildCheckinPromptButtons(lang, date string) ([][]InlineButton, string) {
	text, opts := CheckinPromptOptions(lang)
	row1 := []InlineButton{
		{Text: opts[0].Label, CallbackData: fmt.Sprintf("%s:%s:%s", CheckinCallbackPrefix, opts[0].ID, date)},
		{Text: opts[1].Label, CallbackData: fmt.Sprintf("%s:%s:%s", CheckinCallbackPrefix, opts[1].ID, date)},
	}
	row2 := []InlineButton{
		{Text: opts[2].Label, CallbackData: fmt.Sprintf("%s:%s:%s", CheckinCallbackPrefix, opts[2].ID, date)},
		{Text: opts[3].Label, CallbackData: fmt.Sprintf("%s:%s:%s", CheckinCallbackPrefix, opts[3].ID, date)},
	}
	return [][]InlineButton{row1, row2}, text
}

// DeliverCheckinPromptToTelegram delivers an already-committed canonical
// check-in question to Telegram using its durable delivery key. Failures never
// roll back the committed subjective_checkins / inbox_items rows.
func DeliverCheckinPromptToTelegram(bot CheckinBot, db *storage.DB, lang, date string) error {
	if bot == nil || db == nil || date == "" {
		return nil
	}
	key := "prompt:checkin:" + date
	ctx, cancel := context.WithTimeout(context.Background(), notificationDeliveryTimeout)
	token, reserved, err := db.ReserveNotificationDelivery(ctx, key)
	cancel()
	if err != nil || !reserved {
		return err
	}
	rows, text := buildCheckinPromptButtons(lang, date)
	msgID, err := bot.SendInlineKeyboard(text, rows)
	if err != nil {
		status, code := deliveryFailureStatus(err)
		return errors.Join(err, completeNotificationDelivery(db, key, token, status, code))
	}
	attachCtx, attachCancel := context.WithTimeout(context.Background(), notificationDeliveryTimeout)
	attachErr := db.AttachCheckinTelegramMessageID(attachCtx, date, msgID)
	attachCancel()
	if attachErr != nil {
		return errors.Join(attachErr, completeNotificationDelivery(db, key, token, "sent", ""))
	}
	return completeNotificationDelivery(db, key, token, "sent", "")
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
// expiresAt is retained for source compatibility; new rows use the
// independent two-hour answer window.
//
// Store write is skipped when the Telegram send fails so we don't
// claim "we prompted them" when no message ever arrived.
func SendCheckinPrompt(bot CheckinBot, store CheckinStore, lang, date string, now, expiresAt time.Time) error {
	_ = expiresAt
	expiresAt = CheckinPromptExpiry(now)
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
