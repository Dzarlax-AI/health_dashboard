package notify

import (
	"strings"
	"testing"
)

// classifyTelegramAPIError maps raw Telegram API responses to short,
// stable reason codes the UI shows in the failed-badge.
//
// Pure mapping — tests pin the exact code so future copy changes in
// the Telegram API description don't drift the operator-visible badge.
func TestClassifyTelegramAPIError(t *testing.T) {
	cases := []struct {
		name string
		http int
		ok   bool
		desc string
		want string
	}{
		{"happy path", 200, true, "", "ok"},
		{"401 unauthorized — bad token", 401, false, "Unauthorized", "unauthorized"},
		{"404 not found — token typo", 404, false, "Not Found", "not_found"},
		{"400 bad request — bad URL", 400, false, "Bad Request: bad webhook: HTTPS url must be provided", "bad_request"},
		{"400 already set elsewhere", 400, false, "Bad Request: another webhook is being set", "bad_request"},
		{"429 rate-limited", 429, false, "Too Many Requests", "rate_limited"},
		{"500 telegram-side outage", 500, false, "Internal Server Error", "telegram_5xx"},
		{"502 bad gateway", 502, false, "Bad Gateway", "telegram_5xx"},
		// ok=false with HTTP 200 happens for logical errors like "query
		// is too old". Still a failure from our perspective.
		{"200 with ok=false", 200, false, "query is too old and response timeout expired", "rejected"},
		{"unknown — fallback to truncated description", 418, false, "I'm a teapot", "418"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyTelegramAPIError(tc.http, tc.ok, tc.desc)
			if got != tc.want {
				t.Errorf("classify(%d, ok=%v, %q) = %q, want %q", tc.http, tc.ok, tc.desc, got, tc.want)
			}
		})
	}
}

// classifyTelegramAPIError must always return a short, stable, kebab-
// or snake-style token suitable for embedding into the badge — never a
// raw user-visible Telegram description (which could be very long and
// might contain HTML/markdown).
func TestClassifyTelegramAPIError_AlwaysShortToken(t *testing.T) {
	got := classifyTelegramAPIError(400, false, strings.Repeat("very long telegram description ", 50))
	if len(got) > 32 {
		t.Errorf("classify returned long string (%d chars): %q", len(got), got)
	}
	if strings.Contains(got, " ") {
		t.Errorf("classify returned text with spaces (not a stable token): %q", got)
	}
}
