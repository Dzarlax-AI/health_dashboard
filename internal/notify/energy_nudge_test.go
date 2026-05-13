package notify

import (
	"strings"
	"testing"
)

func TestFormatEnergyBackfillNudge_SubstitutesPlaceholders(t *testing.T) {
	msg := formatEnergyBackfillNudge(280, 3, "en", "https://health.example.dev")
	if !strings.Contains(msg, "280") {
		t.Errorf("expected complete count 280 in message, got: %q", msg)
	}
	if !strings.Contains(msg, "3 EnergyBank snapshots") && !strings.Contains(msg, "only 3") {
		t.Errorf("expected backfilled count 3 in message, got: %q", msg)
	}
	if !strings.Contains(msg, "https://health.example.dev/settings") {
		t.Errorf("expected settings link with baseURL, got: %q", msg)
	}
	// Defence against the {complete}/{backfilled} placeholders shipping
	// to users if a future template edit forgets to substitute them.
	if strings.Contains(msg, "{complete}") || strings.Contains(msg, "{backfilled}") {
		t.Errorf("unsubstituted placeholder leaked into message: %q", msg)
	}
}

func TestFormatEnergyBackfillNudge_BaseURLTrailingSlash(t *testing.T) {
	withSlash := formatEnergyBackfillNudge(40, 0, "en", "https://health.example.dev/")
	withoutSlash := formatEnergyBackfillNudge(40, 0, "en", "https://health.example.dev")
	// Both should produce identical settings links — trailing-slash
	// hygiene was a CodeRabbit-class bug in PR #46 (date-string lex
	// compares vs real arithmetic). Concat with a slash twice would
	// produce //settings; trimming first then concat yields a single
	// /settings.
	if !strings.Contains(withSlash, "https://health.example.dev/settings") {
		t.Errorf("trailing-slash baseURL not normalised: %q", withSlash)
	}
	if !strings.Contains(withoutSlash, "https://health.example.dev/settings") {
		t.Errorf("no-trailing-slash baseURL failed: %q", withoutSlash)
	}
	if strings.Contains(withSlash, "//settings") {
		t.Errorf("double-slash leaked: %q", withSlash)
	}
}

func TestFormatEnergyBackfillNudge_LangFallback(t *testing.T) {
	// An unknown language should fall through to English via tr(),
	// not produce empty fields or untranslated keys.
	msg := formatEnergyBackfillNudge(50, 1, "zz", "https://example.com")
	if msg == "" {
		t.Fatalf("empty message for unknown lang")
	}
	if strings.Contains(msg, "tg_energy_backfill_nudge_") {
		t.Errorf("raw i18n key leaked, tr() fallback broken: %q", msg)
	}
}
