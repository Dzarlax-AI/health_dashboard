package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

const WakeFeedbackCallbackPrefix = "wake"

type WakeFeedbackStore interface {
	GetDerivedMetric(metricName, metricDate string) (*storage.DerivedMetric, error)
	HasRecentDerivedMetricFeedback(metricName, channel, beforeDate string, days int) (bool, error)
	SaveDerivedMetricFeedbackPrompted(feedback storage.DerivedMetricFeedback) (bool, error)
}

// WakeFeedbackPromptOptions returns the localized prompt text and canonical
// ordered answer options shared across Telegram, iOS, and Web.
func WakeFeedbackPromptOptions(lang string, wake time.Time) (string, []CheckinOption) {
	t := health.GetStrings(lang)
	opts := []CheckinOption{
		{ID: storage.WakeFeedbackConfirmed, Label: t["wake_feedback_btn_yes"]},
		{ID: storage.WakeFeedbackEarlier, Label: t["wake_feedback_btn_earlier"]},
		{ID: storage.WakeFeedbackLater, Label: t["wake_feedback_btn_later"]},
		{ID: storage.WakeFeedbackReturnedSleep, Label: t["wake_feedback_btn_returned"]},
	}
	text := t["wake_feedback_prompt"]
	if !wake.IsZero() {
		text = fmt.Sprintf(t["wake_feedback_prompt"], wake.Format("15:04"))
	}
	return text, opts
}

func buildWakeFeedbackPrompt(lang, date string, wake time.Time) ([][]InlineButton, string) {
	text, opts := WakeFeedbackPromptOptions(lang, wake)
	rows := [][]InlineButton{
		{
			{Text: opts[0].Label, CallbackData: fmt.Sprintf("%s:%s:%s", WakeFeedbackCallbackPrefix, opts[0].ID, date)},
			{Text: opts[1].Label, CallbackData: fmt.Sprintf("%s:%s:%s", WakeFeedbackCallbackPrefix, opts[1].ID, date)},
		},
		{
			{Text: opts[2].Label, CallbackData: fmt.Sprintf("%s:%s:%s", WakeFeedbackCallbackPrefix, opts[2].ID, date)},
			{Text: opts[3].Label, CallbackData: fmt.Sprintf("%s:%s:%s", WakeFeedbackCallbackPrefix, opts[3].ID, date)},
		},
	}
	return rows, text
}

// DeliverWakeFeedbackToTelegram delivers an already-committed canonical wake
// feedback prompt to Telegram using its durable delivery key, guarded by the
// live telegram_notifications_enabled barrier.
func DeliverWakeFeedbackToTelegram(bot CheckinBot, db *storage.DB, lang, date string, wake time.Time) error {
	if bot == nil || db == nil || date == "" {
		return nil
	}
	if enabled, checkErr := checkTelegramNotificationsEnabled(db); checkErr != nil {
		return checkErr
	} else if !enabled {
		return nil
	}
	key := "prompt:wake_feedback:" + date
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
	rows, text := buildWakeFeedbackPrompt(lang, date, wake)
	msgID, err := bot.SendInlineKeyboard(text, rows)
	if err != nil {
		status, code := deliveryFailureStatus(err)
		return errors.Join(err, completeNotificationDelivery(db, key, token, status, code))
	}
	attachCtx, attachCancel := context.WithTimeout(context.Background(), notificationDeliveryTimeout)
	attachErr := db.AttachWakeFeedbackTelegramMessageID(attachCtx, date, msgID)
	attachCancel()
	if attachErr != nil {
		return errors.Join(attachErr, completeNotificationDelivery(db, key, token, "sent", ""))
	}
	return completeNotificationDelivery(db, key, token, "sent", "")
}

func parseWakeFeedbackCallback(payload string) (response, date string, ok bool) {
	parts := strings.Split(payload, ":")
	if len(parts) != 3 || parts[0] != WakeFeedbackCallbackPrefix {
		return "", "", false
	}
	if err := storage.ValidateWakeFeedbackResponse(parts[1]); err != nil {
		return "", "", false
	}
	if _, err := time.Parse("2006-01-02", parts[2]); err != nil {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// SendWakeFeedbackPrompt optionally asks for a calibration label after the
// morning report. It returns sent=false for disabled, missing, duplicate, or
// high-confidence-cadence skips.
func SendWakeFeedbackPrompt(bot CheckinBot, store WakeFeedbackStore, lang, date string, now time.Time) (bool, error) {
	if db, ok := store.(*storage.DB); ok {
		if enabled, checkErr := checkTelegramNotificationsEnabled(db); checkErr != nil {
			return false, checkErr
		} else if !enabled {
			return false, nil
		}
	}
	metric, err := store.GetDerivedMetric(storage.DerivedMetricWakeTime, date)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil || metric == nil || metric.ValueTimestamp == nil {
		return false, err
	}
	var metadata struct {
		Confidence string `json:"confidence"`
	}
	_ = json.Unmarshal(metric.Metadata, &metadata)
	if metadata.Confidence == storage.WakeConfidenceHigh {
		recent, err := store.HasRecentDerivedMetricFeedback(
			storage.DerivedMetricWakeTime, "", date, 7,
		)
		if err != nil || recent {
			return false, err
		}
	}

	rows, text := buildWakeFeedbackPrompt(lang, date, metric.ValueTimestamp.In(now.Location()))
	var delivery notificationDeliveryStore
	var token uuid.UUID
	if candidate, ok := store.(notificationDeliveryStore); ok {
		delivery = candidate
		ctx, cancel := context.WithTimeout(context.Background(), notificationDeliveryTimeout)
		var reserved bool
		var reserveErr error
		token, reserved, reserveErr = delivery.ReserveNotificationDelivery(ctx, "prompt:wake_feedback:"+date)
		cancel()
		if reserveErr != nil || !reserved {
			return false, reserveErr
		}
	}
	if db, ok := store.(*storage.DB); ok {
		if enabled, checkErr := checkTelegramNotificationsEnabled(db); checkErr != nil {
			if delivery != nil {
				return false, errors.Join(checkErr, completeNotificationDelivery(delivery, "prompt:wake_feedback:"+date, token, "failed", "archive_unavailable"))
			}
			return false, checkErr
		} else if !enabled {
			if delivery != nil {
				_ = completeNotificationDelivery(delivery, "prompt:wake_feedback:"+date, token, "failed", "telegram_disabled")
			}
			return false, nil
		}
	}
	messageID, err := bot.SendInlineKeyboard(text, rows)
	if err != nil {
		if delivery != nil {
			status, code := deliveryFailureStatus(err)
			_ = completeNotificationDelivery(delivery, "prompt:wake_feedback:"+date, token, status, code)
			return status == "ambiguous", err
		}
		return false, err
	}
	proposed, _ := json.Marshal(metric.ValueTimestamp.Format(time.RFC3339))
	_, err = store.SaveDerivedMetricFeedbackPrompted(storage.DerivedMetricFeedback{
		MetricName:      storage.DerivedMetricWakeTime,
		MetricDate:      date,
		Channel:         storage.DerivedMetricFeedbackTelegram,
		ProposedValue:   proposed,
		PromptMessageID: &messageID,
		PromptedAt:      now,
		Metadata:        json.RawMessage(fmt.Sprintf(`{"confidence":%q}`, metadata.Confidence)),
	})
	if err != nil {
		if delivery != nil {
			return true, fmt.Errorf("save wake feedback prompt after delivery: %w", err)
		}
		return true, err
	}
	if delivery != nil {
		if err := completeNotificationDelivery(delivery, "prompt:wake_feedback:"+date, token, "sent", ""); err != nil {
			return true, err
		}
	}
	// Telegram delivery already happened. Return true even if a pre-existing
	// feedback row made the persistence insert a no-op, so the caller does not
	// send a second inline context prompt in the same morning.
	return true, nil
}

func wakeFeedbackAckText(lang, response string) string {
	t := health.GetStrings(lang)
	switch response {
	case storage.WakeFeedbackConfirmed:
		return t["wake_feedback_ack_yes"]
	case storage.WakeFeedbackEarlier, storage.WakeFeedbackLater, storage.WakeFeedbackReturnedSleep:
		return t["wake_feedback_ack_adjust"]
	default:
		return ""
	}
}
