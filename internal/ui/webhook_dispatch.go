package ui

import (
	"context"
	"log"
	"net/http"

	"health-receiver/internal/notify"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
)

// shouldFirstClearDelete decides whether to synthesize a delete diff
// against the env/default-effective old token. Pure function so the
// branch policy is exercised by table-driven tests without spinning
// up a DB or registrar.
//
// Returns true iff ALL of:
//   - tokenPosted=true: the client explicitly included telegram_token
//     in the POST body. Partial saves that omit the field must NOT
//     trigger a delete — the operator hasn't expressed any intent
//     about the token on this request.
//   - oldTokenRowExists=false: the row was absent before save, i.e.
//     the bot was sourced from env/default fallback, not from a
//     per-tenant override the operator previously set.
//   - newRawToken=="": after save, the stored row is empty — the
//     operator's intent on THIS request is "no per-tenant token".
//   - oldEffectiveToken!="": env/default actually provided a token
//     before the save — there's something to clean up Telegram-side.
//
// All four matter. Dropping tokenPosted is the bug PR #122 round 3
// review caught: a partial POST of unrelated fields (e.g. only
// timezone) on an env-backed tenant would silently trip the delete
// branch and unregister the env bot's webhook.
func shouldFirstClearDelete(tokenPosted, oldTokenRowExists bool, newRawToken, oldEffectiveToken string) bool {
	return tokenPosted && !oldTokenRowExists && newRawToken == "" && oldEffectiveToken != ""
}

// dispatchWebhookDiffRaw is the service-layer side effect that follows
// a successful settings write. Bridges storage (clean, synchronous,
// deterministic) and Telegram HTTP (best-effort, async, may fail).
//
// Inputs are the RAW pre/post per-tenant settings (no env/default
// fallback applied). Diffing on raw values means an explicit clear of
// a per-tenant token triggers deleteWebhook even when env provides a
// fallback that would otherwise mask the transition. See the comment
// in userSettings POST for the env-only corner-case caveat.
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
func (h *Handler) dispatchWebhookDiffRaw(ctx context.Context, schema string, oldCfg, newCfg storage.NotifyConfig) {
	if h.webhookRegistrar == nil || h.reg == nil {
		return
	}
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

// webhookStatus handles GET /api/webhook-status — returns the current
// per-tenant webhook registration status as JSON. Available to all
// users (badge is per-tenant, no cross-tenant disclosure: we only
// read the caller's own schema).
func (h *Handler) webhookStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.reg == nil {
		// Legacy bootstrap without registry — webhook integration
		// unavailable. Reply with a soft "unknown" rather than 500.
		jsonResponse(w, map[string]any{
			"state":      registry.StateUnknown,
			"reason":     "",
			"updated_at": nil,
		})
		return
	}
	schema := h.tenantSchema(r)
	st := h.reg.GetWebhookStatus(r.Context(), schema)
	resp := map[string]any{
		"state":  st.State,
		"reason": st.Reason,
	}
	if !st.UpdatedAt.IsZero() {
		resp["updated_at"] = st.UpdatedAt
	} else {
		resp["updated_at"] = nil
	}
	jsonResponse(w, resp)
}

// webhookStatusRetry handles POST /api/webhook-status/retry — admin-only
// trigger to re-run the registrar for the caller's tenant. Used by
// the Retry button in the badge UI when status is failed.
//
// Builds a synthetic diff from (empty, currentCfg) when the tenant
// currently HAS a token (re-register) or (currentCfg, empty) when it
// doesn't (we have nothing to do). Both arms reuse the same async
// path as dispatchWebhookDiff so the badge transitions through
// pending → ok/failed identically.
func (h *Handler) webhookStatusRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.webhookRegistrar == nil || h.reg == nil {
		http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
		return
	}
	schema := h.tenantSchema(r)
	db := h.tenantDB(r)
	if db == nil {
		http.Error(w, "tenant DB unavailable", http.StatusInternalServerError)
		return
	}
	cfg := db.GetNotifyConfig(h.mgr.NotifyDefaultsFor(schema))
	if cfg.Token == "" {
		// No token configured for this tenant — nothing to register.
		// Mark deleted (or leave alone if already deleted/unknown).
		jsonResponse(w, map[string]string{"status": "noop", "reason": "no token configured"})
		return
	}

	if err := h.reg.SetWebhookStatus(r.Context(), schema, registry.StatePending, ""); err != nil {
		log.Printf("webhookStatusRetry: set pending for %s: %v", schema, err)
	}
	secret := h.reg.GetGlobalSetting(r.Context(), "webhook_secret")
	tokenHeader := h.reg.GetGlobalSetting(r.Context(), "webhook_token_header")
	url := h.webhookBaseURL + "/api/telegram/webhook/" + secret

	// Synthetic register-only diff: we're re-running setWebhook on the
	// current token. OldToken is empty because there's nothing to
	// clean up — this path only fires when the operator explicitly
	// asked to retry on a tenant that DOES have a token.
	go h.runWebhookRegistrar(schema, notify.TelegramDiff{NeedsRegister: true, NewToken: cfg.Token}, url, tokenHeader)

	jsonResponse(w, map[string]string{"status": "pending"})
}


