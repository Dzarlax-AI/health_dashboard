package apnspush

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/google/uuid"

	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

const CategoryCheckinMorning = "CHECKIN_MORNING"

var (
	min5xxBackoff = 15 * time.Minute
	maxRetryDelay = 60 * time.Minute
	min429Backoff = 60 * time.Second
	finishTimeout = 5 * time.Second
)

// TransportSender is implemented by *Sender and mock senders in tests.
type TransportSender interface {
	Send(ctx context.Context, environment, token, requestID, pushType, collapse string, priority int, expiration time.Time, payload []byte) (Result, error)
}

// BuildMorningReportPayload constructs a strictly neutral APNs alert payload
// with no health metrics, scores, user text, or local dates.
func BuildMorningReportPayload(locale string, inboxItemID, bindingID uuid.UUID) ([]byte, error) {
	title, body := "Morning Report", "Your morning report is ready."
	switch locale {
	case "ru":
		title, body = "Утренний отчёт", "Утренний отчёт готов."
	case "sr":
		title, body = "Jutarnji izveštaj", "Jutarnji izveštaj je spreman."
	}
	return json.Marshal(map[string]any{
		"aps": map[string]any{
			"alert": map[string]string{
				"title": title,
				"body":  body,
			},
			"sound": "default",
		},
		"v":             1,
		"kind":          storage.InboxKindMorningReport,
		"inbox_item_id": inboxItemID.String(),
		"reg_id":        bindingID.String(),
	})
}

// BuildCheckinPayload constructs a strictly neutral APNs alert payload for the
// morning subjective check-in prompt with category CHECKIN_MORNING.
func BuildCheckinPayload(locale string, promptID, bindingID uuid.UUID) ([]byte, error) {
	title := "Check-in"
	switch locale {
	case "ru":
		title = "Чек-ин"
	case "sr":
		title = "Ček-in"
	}
	stringsMap := health.GetStrings(locale)
	body := stringsMap["checkin_prompt_text"]
	if body == "" {
		body = "How are you feeling today?"
	}
	return json.Marshal(map[string]any{
		"aps": map[string]any{
			"alert": map[string]string{
				"title":    title,
				"body":     body,
			},
			"sound":    "default",
			"category": CategoryCheckinMorning,
		},
		"v":         1,
		"kind":      storage.InboxKindPromptCheckin,
		"prompt_id": promptID.String(),
		"reg_id":    bindingID.String(),
	})
}

// ProcessTenantDeliveries runs push housekeeping and sends bounded due
// push_deliveries rows for one tenant. It never backfills new deliveries.
func ProcessTenantDeliveries(ctx context.Context, db *storage.DB, sender TransportSender, defaults storage.NotifyConfig, now time.Time, limit int) error {
	if db == nil {
		return nil
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	if err := db.HousekeepPushDeliveries(ctx, limit); err != nil {
		return err
	}
	if sender == nil || !storage.IsInAppInboxEnabled(db) || !storage.IsAPNSPushEnabled(db) {
		return nil
	}
	today := db.TenantLocalDate(defaults, now)
	for i := 0; i < limit; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		d, err := db.ClaimNextPushDelivery(ctx)
		if err != nil {
			return err
		}
		if d == nil {
			return nil
		}
		deliverClaimed(ctx, db, sender, d, today, now)
	}
	return nil
}

func completeWithBudget(ctx context.Context, db *storage.DB, in storage.CompletePushDeliveryInput) (bool, error) {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	return db.CompletePushDelivery(finishCtx, in)
}

func deliverClaimed(ctx context.Context, db *storage.DB, sender TransportSender, d *storage.ClaimedPushDelivery, today string, now time.Time) {
	// Verify device registration is still active and matches the enqueued binding/version/token.
	if !d.DeviceEnabled ||
		d.DeviceInvalidatedAt != nil ||
		d.DeviceBindingID != d.BindingID ||
		d.DeviceRegistrationVersion != d.RegistrationVersion ||
		!bytes.Equal(d.DeviceTokenHash, d.TokenHash) {
		if _, err := completeWithBudget(ctx, db, storage.CompletePushDeliveryInput{
			DeliveryID:    d.ID,
			LeaseToken:    d.LeaseToken,
			DeliveryState: storage.PushDeliveryStateRejected,
			Reason:        "device_changed",
		}); err != nil {
			log.Printf("apns: complete device_changed delivery=%s: %v", d.ID, err)
		}
		return
	}

	// Verify check-in prompt is still active for today before touching APNs.
	if d.Kind == storage.InboxKindPromptCheckin {
		if d.CheckinStatus != storage.CheckinStatusPrompted ||
			d.LocalDate != today ||
			(d.CheckinExpiresAt != nil && !now.Before(*d.CheckinExpiresAt)) {
			if _, err := completeWithBudget(ctx, db, storage.CompletePushDeliveryInput{
				DeliveryID:    d.ID,
				LeaseToken:    d.LeaseToken,
				DeliveryState: storage.PushDeliveryStateExpired,
				Reason:        "prompt_no_longer_active",
			}); err != nil {
				log.Printf("apns: complete expired checkin delivery=%s: %v", d.ID, err)
			}
			return
		}
	}

	var payload []byte
	var err error
	switch d.Kind {
	case storage.InboxKindMorningReport:
		payload, err = BuildMorningReportPayload(d.Locale, d.InboxItemID, d.BindingID)
	case storage.InboxKindPromptCheckin:
		payload, err = BuildCheckinPayload(d.Locale, d.InboxItemID, d.BindingID)
	default:
		err = errors.New("unknown push kind")
	}
	if err != nil {
		_, _ = completeWithBudget(ctx, db, storage.CompletePushDeliveryInput{
			DeliveryID:    d.ID,
			LeaseToken:    d.LeaseToken,
			DeliveryState: storage.PushDeliveryStateRejected,
			Reason:        "invalid_payload",
		})
		return
	}

	// Record durable attempt strictly under active unexpired lease_token BEFORE calling APNs.
	attemptID, err := db.StartPushDeliveryAttempt(ctx, d.ID, d.LeaseToken)
	if err != nil {
		if !errors.Is(err, storage.ErrPushLeaseLost) {
			log.Printf("apns: start attempt delivery=%s: %v", d.ID, err)
		}
		return
	}

	res, sendErr := sender.Send(ctx, d.APNsEnv, d.DeviceAPNsToken, d.APNsID.String(), "alert", d.CollapseID, 10, d.ExpiresAt, payload)
	outcome := classifyAPNsOutcome(d, attemptID, res, sendErr, now)
	// Start the finalization timeout budget AFTER APNs responds so slow HTTP
	// responses (up to the 15s client timeout) never exhaust the DB write budget.
	if _, err := completeWithBudget(ctx, db, outcome); err != nil {
		log.Printf("apns: complete delivery=%s state=%s: %v", d.ID, outcome.DeliveryState, err)
	}
}

func classifyAPNsOutcome(d *storage.ClaimedPushDelivery, attemptID uuid.UUID, res Result, sendErr error, now time.Time) storage.CompletePushDeliveryInput {
	out := storage.CompletePushDeliveryInput{
		DeliveryID:          d.ID,
		LeaseToken:          d.LeaseToken,
		AttemptID:           attemptID,
		DeviceID:            d.DeviceID,
		RegistrationVersion: d.RegistrationVersion,
		TokenHash:           d.TokenHash,
	}
	if sendErr != nil {
		out.DeliveryState = storage.PushDeliveryStateAmbiguous
		out.AttemptStatus = storage.PushAttemptStatusAmbiguous
		out.Reason = "transport_ambiguous"
		return out
	}

	code := res.Status
	out.StatusCode = &code
	out.Reason = res.Reason

	switch {
	case res.Status == 200:
		out.DeliveryState = storage.PushDeliveryStateAccepted
		out.AttemptStatus = storage.PushAttemptStatusAccepted
		return out

	case res.Status == 410:
		out.DeliveryState = storage.PushDeliveryStateRejected
		out.AttemptStatus = storage.PushAttemptStatusRejected
		if out.Reason == "" {
			out.Reason = "Unregistered"
		}
		out.InvalidateDevice = true
		out.AppleInvalidatedAt = res.Timestamp
		return out

	case res.Status == 400 && (res.Reason == "BadDeviceToken" || res.Reason == "DeviceTokenNotForTopic" || res.Reason == "Unregistered"):
		out.DeliveryState = storage.PushDeliveryStateRejected
		out.AttemptStatus = storage.PushAttemptStatusRejected
		out.InvalidateDevice = true
		return out

	case res.Status == 429:
		delay := retryDelay429(d.Attempts, res.RetryAfter)
		out.AttemptStatus = storage.PushAttemptStatusRetryableFailed
		if d.Attempts < storage.PushMaxAttempts && now.Add(delay).Before(d.ExpiresAt) {
			out.DeliveryState = storage.PushDeliveryStateRetryWait
			out.RetryAfter = delay
		} else {
			out.DeliveryState = storage.PushDeliveryStateRejected
		}
		return out

	case res.Status >= 500:
		delay := retryDelay5xx(d.Attempts, res.RetryAfter)
		out.AttemptStatus = storage.PushAttemptStatusRetryableFailed
		if d.Attempts < storage.PushMaxAttempts && now.Add(delay).Before(d.ExpiresAt) {
			out.DeliveryState = storage.PushDeliveryStateRetryWait
			out.RetryAfter = delay
		} else {
			out.DeliveryState = storage.PushDeliveryStateRejected
		}
		return out

	default:
		out.DeliveryState = storage.PushDeliveryStateRejected
		out.AttemptStatus = storage.PushAttemptStatusRejected
		return out
	}
}

func retryDelay429(attempts int, retryAfter time.Duration) time.Duration {
	shift := attempts - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 6 {
		shift = 6
	}
	delay := min429Backoff << shift
	if retryAfter > delay {
		delay = retryAfter
	}
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}
	return delay
}

// retryDelay5xx enforces Apple's recommended >= 15 minute pause on APNs 5xx.
func retryDelay5xx(attempts int, retryAfter time.Duration) time.Duration {
	shift := attempts - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 2 {
		shift = 2
	}
	delay := min5xxBackoff << shift
	if retryAfter > delay {
		delay = retryAfter
	}
	if delay < min5xxBackoff {
		delay = min5xxBackoff
	}
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}
	return delay
}
