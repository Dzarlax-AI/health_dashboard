package ui

import (
	"context"
	"log"

	"health-receiver/internal/notify"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
)

// dispatchWebhookDiff is the service-layer side effect that follows a
// successful settings write. It's the bridge between storage (clean,
// synchronous, deterministic) and Telegram HTTP (best-effort, async,
// rate-limited, may fail).
//
// Contract:
//   - Settings save has ALREADY completed by the time this runs.
//     Webhook failure does NOT roll back the save.
//   - When the registrar is not configured or the diff is a no-op
//     (chat_id changed, token same), this returns immediately —
//     no badge update, no HTTP, no goroutine spawn.
//   - When the diff requires action, we write status=pending
//     synchronously (so the UI badge flips fast), then spawn ONE
//     goroutine that calls the registrar and writes the final
//     status (ok | deleted | failed:<reason>). The goroutine uses
//     context.Background() so an aborted request doesn't cancel
//     mid-call to Telegram.
//   - Reg or registrar nil disables the side effect cleanly. Tests
//     and the legacy bootstrap path leave this disabled.
func (h *Handler) dispatchWebhookDiff(ctx context.Context, db *storage.DB, schema string, oldCfg storage.NotifyConfig, notifyDefaults storage.NotifyConfig) {
	if h.webhookRegistrar == nil || h.reg == nil {
		return
	}
	newCfg := db.GetNotifyConfig(notifyDefaults)
	diff := notify.DetectTelegramDiff(oldCfg, newCfg)
	if !diff.NeedsRegister && !diff.NeedsDelete {
		return
	}

	// Mark pending synchronously so the UI badge flips before the
	// goroutine starts. The poll-while-pending loop will pick this
	// up immediately on the next refresh.
	if err := h.reg.SetWebhookStatus(ctx, schema, registry.StatePending, ""); err != nil {
		log.Printf("dispatchWebhookDiff: set pending for %s: %v (proceeding with registrar call anyway)", schema, err)
	}

	secret := h.reg.GetGlobalSetting(ctx, "webhook_secret")
	tokenHeader := h.reg.GetGlobalSetting(ctx, "webhook_token_header")
	webhookURL := h.webhookBaseURL + "/api/telegram/webhook/" + secret

	go h.runWebhookRegistrar(schema, diff, webhookURL, tokenHeader)
}

// runWebhookRegistrar is the goroutine body for the actual HTTP call.
// Isolated from dispatchWebhookDiff so the synchronous bookkeeping
// stays readable. Detached context (Background) so the originating
// HTTP request canceling doesn't kill mid-call.
func (h *Handler) runWebhookRegistrar(schema string, diff notify.TelegramDiff, url, tokenHeader string) {
	bg := context.Background()

	var reason string
	var err error
	switch {
	case diff.NeedsRegister:
		reason, err = h.webhookRegistrar.Register(diff.NewToken, url, tokenHeader)
	case diff.NeedsDelete:
		reason, err = h.webhookRegistrar.Delete(diff.OldToken)
	}

	if err != nil {
		log.Printf("dispatchWebhookDiff: %s registrar failed: reason=%s err=%v", schema, reason, err)
		if perr := h.reg.SetWebhookStatus(bg, schema, registry.StateFailed, reason); perr != nil {
			log.Printf("dispatchWebhookDiff: persist failed-status for %s: %v", schema, perr)
		}
		return
	}

	finalState := registry.StateOK
	if diff.NeedsDelete {
		finalState = registry.StateDeleted
	}
	if perr := h.reg.SetWebhookStatus(bg, schema, finalState, ""); perr != nil {
		log.Printf("dispatchWebhookDiff: persist final status for %s: %v", schema, perr)
		return
	}
	log.Printf("dispatchWebhookDiff: %s → %s", schema, finalState)
}
