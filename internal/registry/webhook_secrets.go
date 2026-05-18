package registry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
)

// Keys for the webhook secret pair in global_settings.
const (
	globalKeyWebhookSecret      = "webhook_secret"
	globalKeyWebhookTokenHeader = "webhook_token_header"
)

// resolveWebhookSecretsDecision is the pure decision the lazy-init
// orchestration uses. Returns:
//   - secret, token: the values to use (empty when source="generate")
//   - source:  "env" | "global" | "generate"
//   - persist: whether the caller should write to global_settings
//
// Contract:
//   - Both env vars set → env wins. Operator intent always overrides
//     cached values, so we persist forward (rotation works).
//   - Env empty/partial AND both global vars set → use global.
//   - Otherwise → generate. The half-set env case (only one var) is
//     treated as not-set so we don't run with a mismatched pair.
//
// Pinned by TestResolveWebhookSecretsDecision.
func resolveWebhookSecretsDecision(globalSecret, globalToken, envSecret, envToken string) (
	secret, token, source string, persist bool,
) {
	envBothSet := envSecret != "" && envToken != ""
	globalBothSet := globalSecret != "" && globalToken != ""

	switch {
	case envBothSet:
		return envSecret, envToken, "env", true
	case globalBothSet:
		return globalSecret, globalToken, "global", false
	default:
		return "", "", "generate", true
	}
}

// generateRandomSecret returns a 32-byte hex-encoded secret suitable
// for the Telegram URL-path secret and X-Telegram-Bot-Api-Secret-Token
// header. 32 bytes = 256 bits of entropy, well above Telegram's
// 1..256 char requirement.
func generateRandomSecret() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("crypto/rand: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

// ResolveOrGenerateWebhookSecrets is the public entry point used at
// server startup by registerCheckinWebhook. Three-step lookup:
//   1. global_settings (persisted from previous boot or operator save)
//   2. env vars (this-process configuration, or first-boot bootstrap)
//   3. generate a fresh pair
//
// When env wins (operator rotation case), the env values are also
// persisted to global_settings so removing env vars later doesn't
// break the webhook on the next restart. When we generate, the new
// values are persisted too.
//
// Returns (secret, tokenHeader, source). source is "env" | "global"
// | "generated" for the startup log line — operators want to know
// at a glance where the secret came from.
//
// On hard DB errors during persist, logs and returns the resolved
// values anyway — the webhook still works for THIS process; the
// operator sees the log and can investigate.
func (r *Registry) ResolveOrGenerateWebhookSecrets(ctx context.Context, envSecret, envToken string) (secret, tokenHeader, source string) {
	globalSecret := r.GetGlobalSetting(ctx, globalKeyWebhookSecret)
	globalToken := r.GetGlobalSetting(ctx, globalKeyWebhookTokenHeader)

	resolved, token, src, persist := resolveWebhookSecretsDecision(
		globalSecret, globalToken, envSecret, envToken)

	if src == "generate" {
		var gerr error
		resolved, gerr = generateRandomSecret()
		if gerr != nil {
			log.Printf("ResolveOrGenerateWebhookSecrets: gen secret: %v", gerr)
			return "", "", "error"
		}
		token, gerr = generateRandomSecret()
		if gerr != nil {
			log.Printf("ResolveOrGenerateWebhookSecrets: gen token: %v", gerr)
			return "", "", "error"
		}
		src = "generated"
	}

	if persist {
		if err := r.SaveGlobalSettings(ctx, map[string]string{
			globalKeyWebhookSecret:      resolved,
			globalKeyWebhookTokenHeader: token,
		}); err != nil {
			log.Printf("ResolveOrGenerateWebhookSecrets: persist secrets: %v (using anyway)", err)
		}
	}

	return resolved, token, src
}
