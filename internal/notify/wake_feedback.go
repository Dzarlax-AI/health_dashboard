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

func buildWakeFeedbackPrompt(lang, date string, wake time.Time) ([][]InlineButton, string) {
	t := health.GetStrings(lang)
	rows := [][]InlineButton{
		{
			{Text: t["wake_feedback_btn_yes"], CallbackData: fmt.Sprintf("%s:%s:%s", WakeFeedbackCallbackPrefix, storage.WakeFeedbackConfirmed, date)},
			{Text: t["wake_feedback_btn_earlier"], CallbackData: fmt.Sprintf("%s:%s:%s", WakeFeedbackCallbackPrefix, storage.WakeFeedbackEarlier, date)},
		},
		{
			{Text: t["wake_feedback_btn_later"], CallbackData: fmt.Sprintf("%s:%s:%s", WakeFeedbackCallbackPrefix, storage.WakeFeedbackLater, date)},
			{Text: t["wake_feedback_btn_returned"], CallbackData: fmt.Sprintf("%s:%s:%s", WakeFeedbackCallbackPrefix, storage.WakeFeedbackReturnedSleep, date)},
		},
	}
	return rows, fmt.Sprintf(t["wake_feedback_prompt"], wake.Format("15:04"))
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
			storage.DerivedMetricWakeTime, storage.DerivedMetricFeedbackTelegram, date, 7,
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
