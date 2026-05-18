package notify

import (
	"strings"
	"testing"
	"time"

	"health-receiver/internal/health"
)

// formatMorning renders a soft expired-checkin footer only when the
// cap-path explicitly passes checkinExpired=true. Pins the contract
// promised by the PR description (and the existing checkin_expired_note
// i18n string) so future refactors don't accidentally drop the note.
func TestFormatMorning_AppendsExpiredNote(t *testing.T) {
	briefing := &health.BriefingResponse{Date: "2026-05-18"}
	loc, _ := time.LoadLocation("UTC")

	t.Run("checkinExpired=true → note appears", func(t *testing.T) {
		out := formatMorning(briefing, nil, "ru", loc, freshness{}, true)
		// The RU note text starts with "Хотите". Any future copy change
		// keeps the same key, so look for the i18n marker by checking
		// the key resolves and its prefix lands in the output.
		want := "Хотите"
		if !strings.Contains(out, want) {
			t.Errorf("expired note missing from output:\n%s\n\nwanted substring %q", out, want)
		}
	})

	t.Run("checkinExpired=false → no note", func(t *testing.T) {
		out := formatMorning(briefing, nil, "ru", loc, freshness{}, false)
		if strings.Contains(out, "Хотите") {
			t.Errorf("expired note rendered when checkinExpired=false:\n%s", out)
		}
	})

	t.Run("english locale also renders", func(t *testing.T) {
		out := formatMorning(briefing, nil, "en", loc, freshness{}, true)
		want := "Want the report"
		if !strings.Contains(out, want) {
			t.Errorf("EN expired note missing:\n%s\n\nwanted substring %q", out, want)
		}
	})
}
