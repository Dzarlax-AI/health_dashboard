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

const (
	CategoryCheckinMorning = "CHECKIN_MORNING"
	CategoryPromptOpen     = "PROMPT_OPEN"
)

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
				"title": title,
				"body":  body,
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

// BuildPayloadForKind constructs a strictly neutral APNs alert payload (no PII,
// no health metrics, no scores, no local dates, and no static aps.badge so
// delayed deliveries never revert the client's live unread badge count).
func BuildPayloadForKind(kind, locale string, inboxItemID, bindingID uuid.UUID) ([]byte, error) {
	switch kind {
	case storage.InboxKindMorningReport:
		return BuildMorningReportPayload(locale, inboxItemID, bindingID)
	case storage.InboxKindPromptCheckin:
		return BuildCheckinPayload(locale, inboxItemID, bindingID)
	case storage.InboxKindEveningReport:
		title, body := "Evening Report", "Your evening report is ready."
		switch locale {
		case "ru":
			title, body = "Вечерний отчёт", "Вечерний отчёт готов."
		case "sr":
			title, body = "Večernji izveštaj", "Večernji izveštaj je spreman."
		}
		return buildNeutralInboxPayload(kind, title, body, "", inboxItemID, bindingID, false)
	case storage.InboxKindSleepUpdate:
		title, body := "Sleep Analysis Updated", "Updated sleep analysis is ready."
		switch locale {
		case "ru":
			title, body = "Обновление анализа сна", "Доступен обновлённый разбор сна."
		case "sr":
			title, body = "Ažurirana analiza sna", "Dostupna je ažurirana analiza sna."
		}
		return buildNeutralInboxPayload(kind, title, body, "", inboxItemID, bindingID, false)
	case storage.InboxKindGuidanceChange:
		title, body := "Guidance Updated", "Today's guidance has been updated."
		switch locale {
		case "ru":
			title, body = "Обновление рекомендации", "Рекомендация на сегодня обновилась."
		case "sr":
			title, body = "Ažurirana preporuka", "Današnja preporuka je ažurirana."
		}
		return buildNeutralInboxPayload(kind, title, body, "", inboxItemID, bindingID, false)
	case storage.InboxKindWeeklyDigest:
		title, body := "Weekly Digest", "Your weekly data quality summary is ready."
		switch locale {
		case "ru":
			title, body = "Еженедельный дайджест", "Доступна сводка качества данных."
		case "sr":
			title, body = "Nedeljni pregled", "Dostupan je nedeljni pregled kvaliteta podataka."
		}
		return buildNeutralInboxPayload(kind, title, body, "", inboxItemID, bindingID, false)
	case storage.InboxKindNudge:
		title, body := "Health Sync", "You have a new note in Inbox."
		switch locale {
		case "ru":
			title, body = "Health Sync", "Есть новая рекомендация в Inbox."
		case "sr":
			title, body = "Health Sync", "Imate novu preporuku u Inbox-u."
		}
		return buildNeutralInboxPayload(kind, title, body, "", inboxItemID, bindingID, false)
	case storage.InboxKindPromptWakeFeedback:
		title, body := "Wake Time", "Confirm your wake time in Inbox."
		switch locale {
		case "ru":
			title, body = "Время пробуждения", "Подтвердите время пробуждения."
		case "sr":
			title, body = "Vreme buđenja", "Potvrdite vreme buđenja."
		}
		return buildNeutralInboxPayload(kind, title, body, CategoryPromptOpen, inboxItemID, bindingID, true)
	case storage.InboxKindPromptContext:
		title, body := "Quick Question", "You have a short question in Inbox."
		switch locale {
		case "ru":
			title, body = "Контекст дня", "Есть короткий вопрос в Inbox."
		case "sr":
			title, body = "Kontekst dana", "Imate kratko pitanje u Inbox-u."
		}
		return buildNeutralInboxPayload(kind, title, body, CategoryPromptOpen, inboxItemID, bindingID, true)
	default:
		return nil, errors.New("unknown push kind")
	}
}

func buildNeutralInboxPayload(kind, title, body, category string, itemID, bindingID uuid.UUID, isPrompt bool) ([]byte, error) {
	aps := map[string]any{
		"alert": map[string]string{
			"title": title,
			"body":  body,
		},
		"sound": "default",
	}
	if category != "" {
		aps["category"] = category
	}
	payload := map[string]any{
		"aps":           aps,
		"v":             1,
		"kind":          kind,
		"inbox_item_id": itemID.String(),
		"reg_id":        bindingID.String(),
	}
	if isPrompt {
		payload["prompt_id"] = itemID.String()
	}
	return json.Marshal(payload)
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
		// An opt-out may have occurred while the preceding transport call was in flight.
		if i > 0 && (!storage.IsInAppInboxEnabled(db) || !storage.IsAPNSPushEnabled(db)) {
			return nil
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
	// Verify device still supports the inbox contract version required by this push kind
	// (e.g. if the user reinstalled an older TestFlight 33 build after enqueue).
	if d.DeviceInboxContractVersion < storage.RequiredInboxContractVersion(d.Kind) {
		if _, err := completeWithBudget(ctx, db, storage.CompletePushDeliveryInput{
			DeliveryID:    d.ID,
			LeaseToken:    d.LeaseToken,
			DeliveryState: storage.PushDeliveryStateRejected,
			Reason:        "device_contract_downgraded",
		}); err != nil {
			log.Printf("apns: complete device_contract_downgraded delivery=%s: %v", d.ID, err)
		}
		return
	}

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

	// Re-check per-category push preference right before sending.
	if !storage.IsPushCategoryEnabled(db, d.Kind) {
		if _, err := completeWithBudget(ctx, db, storage.CompletePushDeliveryInput{
			DeliveryID:    d.ID,
			LeaseToken:    d.LeaseToken,
			DeliveryState: storage.PushDeliveryStateRejected,
			Reason:        "category_disabled",
		}); err != nil {
			log.Printf("apns: complete category_disabled delivery=%s: %v", d.ID, err)
		}
		return
	}

	// Verify interactive prompts are still active before touching APNs.
	switch d.Kind {
	case storage.InboxKindPromptCheckin:
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
	case storage.InboxKindPromptWakeFeedback:
		if d.WakeFeedbackResponse != "" || d.LocalDate != today {
			if _, err := completeWithBudget(ctx, db, storage.CompletePushDeliveryInput{
				DeliveryID:    d.ID,
				LeaseToken:    d.LeaseToken,
				DeliveryState: storage.PushDeliveryStateExpired,
				Reason:        "prompt_no_longer_active",
			}); err != nil {
				log.Printf("apns: complete expired wake feedback delivery=%s: %v", d.ID, err)
			}
			return
		}
	case storage.InboxKindPromptContext:
		if d.ContextPromptStatus != storage.ContextPromptStatusPrompted ||
			(d.ContextExpiresAt != nil && !now.Before(*d.ContextExpiresAt)) {
			if _, err := completeWithBudget(ctx, db, storage.CompletePushDeliveryInput{
				DeliveryID:    d.ID,
				LeaseToken:    d.LeaseToken,
				DeliveryState: storage.PushDeliveryStateExpired,
				Reason:        "prompt_no_longer_active",
			}); err != nil {
				log.Printf("apns: complete expired context prompt delivery=%s: %v", d.ID, err)
			}
			return
		}
	}

	payload, err := BuildPayloadForKind(d.Kind, d.Locale, d.InboxItemID, d.BindingID)
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
		if errors.Is(err, storage.ErrPushDeliveryIneligible) {
			if _, finishErr := completeWithBudget(ctx, db, storage.CompletePushDeliveryInput{
				DeliveryID: d.ID, LeaseToken: d.LeaseToken, DeliveryState: storage.PushDeliveryStateExpired,
				Reason: "no_longer_eligible",
			}); finishErr != nil {
				log.Printf("apns: complete ineligible delivery=%s: %v", d.ID, finishErr)
			}
		} else if !errors.Is(err, storage.ErrPushLeaseLost) {
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
