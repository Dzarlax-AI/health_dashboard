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

const ContextPromptCallbackPrefix = "ctx"

var contextChoiceCodes = map[string]string{
	"poor":    storage.ContextPromptCategoryPoorSleep,
	"stress":  storage.ContextPromptCategoryStress,
	"travel":  storage.ContextPromptCategoryTravel,
	"unknown": storage.ContextPromptCategoryUnknown,
	"skip":    storage.ContextPromptCategorySkip,
}

var contextCategoryCodes = map[string]string{
	storage.ContextPromptCategoryPoorSleep: "poor",
	storage.ContextPromptCategoryStress:    "stress",
	storage.ContextPromptCategoryTravel:    "travel",
	storage.ContextPromptCategoryUnknown:   "unknown",
	storage.ContextPromptCategorySkip:      "skip",
}

type ContextPromptBot interface {
	SendInlineKeyboard(text string, rows [][]InlineButton) (int64, error)
}

type ContextPromptStore interface {
	MarkContextPromptSent(promptID string, msgID int64, promptedAt time.Time) error
	MarkContextPromptSendFailed(promptID string, failedAt time.Time) error
}

func buildContextPromptButtons(lang string, prompt storage.ContextPromptInteraction) ([][]InlineButton, string) {
	t := health.GetStrings(lang)
	buttonFor := func(category string) InlineButton {
		code := contextCategoryCodes[category]
		return InlineButton{
			Text:         t["context_prompt_btn_"+category],
			CallbackData: fmt.Sprintf("%s:%s:%s", ContextPromptCallbackPrefix, prompt.PromptID, code),
		}
	}
	row1 := []InlineButton{}
	row2 := []InlineButton{}
	for i, category := range prompt.AllowedCategories {
		if _, ok := contextCategoryCodes[category]; !ok {
			continue
		}
		if i < 3 {
			row1 = append(row1, buttonFor(category))
		} else {
			row2 = append(row2, buttonFor(category))
		}
	}
	rows := [][]InlineButton{}
	if len(row1) > 0 {
		rows = append(rows, row1)
	}
	if len(row2) > 0 {
		rows = append(rows, row2)
	}
	return rows, t["context_prompt_low_sleep_text"]
}

func parseContextPromptCallback(payload string) (string, string, bool) {
	parts := strings.Split(payload, ":")
	if len(parts) != 3 || parts[0] != ContextPromptCallbackPrefix {
		return "", "", false
	}
	if parts[1] == "" {
		return "", "", false
	}
	category, ok := contextChoiceCodes[parts[2]]
	if !ok {
		return "", "", false
	}
	return parts[1], category, true
}

// ContextPromptOptions returns the localized title and ordered answer options
// for a context prompt, shared across Telegram, iOS, and Web.
func ContextPromptOptions(lang string, allowedCategories []string) (string, []CheckinOption) {
	t := health.GetStrings(lang)
	if len(allowedCategories) == 0 {
		allowedCategories = storage.DefaultContextPromptCategories
	}
	opts := make([]CheckinOption, 0, len(allowedCategories))
	for _, category := range allowedCategories {
		if _, ok := contextCategoryCodes[category]; !ok {
			continue
		}
		opts = append(opts, CheckinOption{
			ID:    category,
			Label: t["context_prompt_btn_"+category],
		})
	}
	return t["context_prompt_low_sleep_text"], opts
}

// DeliverContextPromptToTelegram sends an already-committed Inbox context
// prompt to Telegram under its durable delivery key, guarded by the live
// telegram_notifications_enabled barrier.
func DeliverContextPromptToTelegram(bot ContextPromptBot, db *storage.DB, lang string, prompt *storage.ContextPromptInteraction) error {
	if bot == nil || db == nil || prompt == nil {
		return nil
	}
	if enabled, checkErr := checkTelegramNotificationsEnabled(db); checkErr != nil {
		return checkErr
	} else if !enabled {
		return nil
	}
	rows, text := buildContextPromptButtons(lang, *prompt)
	if len(rows) == 0 {
		return nil
	}
	key := "prompt:context:" + prompt.PromptID
	ctx, cancel := context.WithTimeout(context.Background(), notificationDeliveryTimeout)
	token, reserved, err := db.ReserveNotificationDelivery(ctx, key)
	cancel()
	if err != nil || !reserved {
		return err
	}
	if enabled, checkErr := checkTelegramNotificationsEnabled(db); checkErr != nil {
		return errors.Join(checkErr, completeNotificationDelivery(db, key, token, "failed", "archive_unavailable"))
	} else if !enabled {
		_ = completeNotificationDelivery(db, key, token, "failed", "telegram_disabled")
		return nil
	}
	msgID, err := bot.SendInlineKeyboard(text, rows)
	if err != nil {
		status, code := deliveryFailureStatus(err)
		return errors.Join(err, completeNotificationDelivery(db, key, token, status, code))
	}
	attachCtx, attachCancel := context.WithTimeout(context.Background(), notificationDeliveryTimeout)
	attachErr := db.AttachContextPromptTelegramMessageID(attachCtx, prompt.PromptID, msgID)
	attachCancel()
	if attachErr != nil {
		return errors.Join(attachErr, completeNotificationDelivery(db, key, token, "sent", ""))
	}
	return completeNotificationDelivery(db, key, token, "sent", "")
}

func SendContextPrompt(bot ContextPromptBot, store ContextPromptStore, lang string, prompt *storage.ContextPromptInteraction, now time.Time) error {
	if prompt == nil {
		return nil
	}
	if db, ok := store.(*storage.DB); ok {
		if enabled, checkErr := checkTelegramNotificationsEnabled(db); checkErr != nil {
			return checkErr
		} else if !enabled {
			return nil
		}
	}
	rows, text := buildContextPromptButtons(lang, *prompt)
	if len(rows) == 0 {
		return nil
	}
	var delivery notificationDeliveryStore
	var token uuid.UUID
	key := "prompt:context:" + prompt.PromptID
	if candidate, ok := store.(notificationDeliveryStore); ok {
		delivery = candidate
		var reserved bool
		var err error
		ctx, cancel := context.WithTimeout(context.Background(), notificationDeliveryTimeout)
		token, reserved, err = delivery.ReserveNotificationDelivery(ctx, key)
		cancel()
		if err != nil || !reserved {
			return err
		}
	}
	if db, ok := store.(*storage.DB); ok {
		if enabled, checkErr := checkTelegramNotificationsEnabled(db); checkErr != nil {
			if delivery != nil {
				return errors.Join(checkErr, completeNotificationDelivery(delivery, key, token, "failed", "archive_unavailable"))
			}
			return checkErr
		} else if !enabled {
			if delivery != nil {
				_ = completeNotificationDelivery(delivery, key, token, "failed", "telegram_disabled")
			}
			return nil
		}
	}
	msgID, err := bot.SendInlineKeyboard(text, rows)
	if err != nil {
		status, code := deliveryFailureStatus(err)
		if delivery != nil {
			_ = completeNotificationDelivery(delivery, key, token, status, code)
		}
		if status == "failed" {
			_ = store.MarkContextPromptSendFailed(prompt.PromptID, now)
		}
		return err
	}
	if err = store.MarkContextPromptSent(prompt.PromptID, msgID, now); err != nil {
		if delivery != nil {
			return errors.Join(err, completeNotificationDelivery(delivery, key, token, "sent", ""))
		}
		return err
	}
	if delivery != nil {
		return completeNotificationDelivery(delivery, key, token, "sent", "")
	}
	return nil
}
